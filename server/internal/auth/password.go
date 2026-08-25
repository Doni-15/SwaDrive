// Package auth contains SwaDrive's security-sensitive authentication primitives.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordHashAlgorithm = "argon2id"
	maxEncodedHashLength  = 512
	minimumMemoryKiB      = 8 * 1024
	maximumMemoryKiB      = 256 * 1024
	maximumIterations     = 10
	maximumParallelism    = 16
	minimumSaltLength     = 16
	maximumSaltLength     = 64
	minimumKeyLength      = 16
	maximumKeyLength      = 64
	DefaultArgon2Limit    = 4
	MaximumArgon2Limit    = 64
	argon2AdmissionFactor = 4
)

var (
	ErrInvalidPasswordHash = errors.New("invalid encoded password hash")
	ErrInvalidArgon2Limit  = errors.New("invalid Argon2 concurrency limit")
	ErrArgon2Busy          = errors.New("Argon2 work queue is full")
)

type passwordDeriver func(password, salt []byte, iterations, memory uint32, parallelism uint8, keyLength uint32) []byte

// PasswordManager is the process-local resource boundary for every Argon2id
// hash and verification. Waiting callers do not spawn helper goroutines and
// can abandon the wait through their context.
type PasswordManager struct {
	slots      chan struct{}
	admissions chan struct{}
	parameters PasswordParameters
	random     io.Reader
	derive     passwordDeriver
}

func NewPasswordManager(maxConcurrent int) (*PasswordManager, error) {
	if maxConcurrent == 0 {
		maxConcurrent = DefaultArgon2Limit
	}
	if maxConcurrent < 1 || maxConcurrent > MaximumArgon2Limit {
		return nil, ErrInvalidArgon2Limit
	}
	return &PasswordManager{
		slots:      make(chan struct{}, maxConcurrent),
		admissions: make(chan struct{}, maxConcurrent*argon2AdmissionFactor),
		parameters: DefaultPasswordParameters(),
		random:     rand.Reader,
		derive:     argon2.IDKey,
	}, nil
}

// PasswordParameters are embedded into each generated password hash so they
// can be changed for new passwords without breaking verification of old ones.
type PasswordParameters struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultPasswordParameters returns the parameters used for newly created
// password hashes.
func DefaultPasswordParameters() PasswordParameters {
	return PasswordParameters{
		MemoryKiB:   64 * 1024,
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// Hash returns a PHC-style, self-describing Argon2id password hash.
func (manager *PasswordManager) Hash(ctx context.Context, password string) (string, error) {
	return manager.hash(ctx, password, manager.parameters, manager.random)
}

// Verify checks password against an encoded hash. A malformed or
// defensively out-of-bounds hash returns ErrInvalidPasswordHash.
func (manager *PasswordManager) Verify(ctx context.Context, password, encodedHash string) (bool, error) {
	parameters, salt, expectedHash, err := parsePasswordHash(encodedHash)
	if err != nil {
		return false, err
	}

	actualHash, err := manager.deriveKey(ctx, []byte(password), salt, parameters)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(actualHash, expectedHash) == 1, nil
}

func (manager *PasswordManager) hash(ctx context.Context, password string, parameters PasswordParameters, random io.Reader) (string, error) {
	if err := validatePasswordParameters(parameters); err != nil {
		return "", err
	}

	salt := make([]byte, parameters.SaltLength)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}

	derivedHash, err := manager.deriveKey(ctx, []byte(password), salt, parameters)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"$%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		passwordHashAlgorithm,
		argon2.Version,
		parameters.MemoryKiB,
		parameters.Iterations,
		parameters.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(derivedHash),
	), nil
}

func (manager *PasswordManager) deriveKey(ctx context.Context, password, salt []byte, parameters PasswordParameters) ([]byte, error) {
	select {
	case manager.admissions <- struct{}{}:
		defer func() { <-manager.admissions }()
	default:
		return nil, ErrArgon2Busy
	}
	select {
	case manager.slots <- struct{}{}:
		defer func() { <-manager.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return manager.derive(password, salt, parameters.Iterations, parameters.MemoryKiB, parameters.Parallelism, parameters.KeyLength), nil
}

func parsePasswordHash(encodedHash string) (PasswordParameters, []byte, []byte, error) {
	if len(encodedHash) == 0 || len(encodedHash) > maxEncodedHashLength {
		return PasswordParameters{}, nil, nil, invalidPasswordHash("invalid encoded length")
	}

	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != passwordHashAlgorithm {
		return PasswordParameters{}, nil, nil, invalidPasswordHash("invalid format or algorithm")
	}

	versionText, ok := strings.CutPrefix(parts[2], "v=")
	if !ok {
		return PasswordParameters{}, nil, nil, invalidPasswordHash("missing version")
	}
	version, err := strconv.ParseUint(versionText, 10, 32)
	if err != nil || version != argon2.Version {
		return PasswordParameters{}, nil, nil, invalidPasswordHash("unsupported version")
	}

	parameters, err := parseParameterFields(parts[3])
	if err != nil {
		return PasswordParameters{}, nil, nil, err
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return PasswordParameters{}, nil, nil, invalidPasswordHash("invalid salt encoding")
	}
	derivedHash, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return PasswordParameters{}, nil, nil, invalidPasswordHash("invalid hash encoding")
	}
	parameters.SaltLength = uint32(len(salt))
	parameters.KeyLength = uint32(len(derivedHash))
	if err := validatePasswordParameters(parameters); err != nil {
		return PasswordParameters{}, nil, nil, err
	}

	return parameters, salt, derivedHash, nil
}

func parseParameterFields(encoded string) (PasswordParameters, error) {
	fields := strings.Split(encoded, ",")
	if len(fields) != 3 {
		return PasswordParameters{}, invalidPasswordHash("invalid parameter count")
	}

	values := make(map[string]uint64, len(fields))
	for _, field := range fields {
		name, valueText, ok := strings.Cut(field, "=")
		if !ok || name == "" || valueText == "" {
			return PasswordParameters{}, invalidPasswordHash("invalid parameter")
		}
		if _, exists := values[name]; exists {
			return PasswordParameters{}, invalidPasswordHash("duplicate parameter")
		}
		value, err := strconv.ParseUint(valueText, 10, 32)
		if err != nil {
			return PasswordParameters{}, invalidPasswordHash("invalid parameter value")
		}
		values[name] = value
	}

	memory, memoryOK := values["m"]
	iterations, iterationsOK := values["t"]
	parallelism, parallelismOK := values["p"]
	if !memoryOK || !iterationsOK || !parallelismOK || parallelism > 255 {
		return PasswordParameters{}, invalidPasswordHash("missing or unknown parameter")
	}

	return PasswordParameters{
		MemoryKiB:   uint32(memory),
		Iterations:  uint32(iterations),
		Parallelism: uint8(parallelism),
	}, nil
}

func validatePasswordParameters(parameters PasswordParameters) error {
	if parameters.MemoryKiB < minimumMemoryKiB || parameters.MemoryKiB > maximumMemoryKiB {
		return invalidPasswordHash("memory parameter outside allowed bounds")
	}
	if parameters.Iterations == 0 || parameters.Iterations > maximumIterations {
		return invalidPasswordHash("iteration parameter outside allowed bounds")
	}
	if parameters.Parallelism == 0 || parameters.Parallelism > maximumParallelism {
		return invalidPasswordHash("parallelism parameter outside allowed bounds")
	}
	if parameters.MemoryKiB < 8*uint32(parameters.Parallelism) {
		return invalidPasswordHash("memory parameter too small for parallelism")
	}
	if parameters.SaltLength < minimumSaltLength || parameters.SaltLength > maximumSaltLength {
		return invalidPasswordHash("salt length outside allowed bounds")
	}
	if parameters.KeyLength < minimumKeyLength || parameters.KeyLength > maximumKeyLength {
		return invalidPasswordHash("hash length outside allowed bounds")
	}
	return nil
}

func invalidPasswordHash(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidPasswordHash, reason)
}

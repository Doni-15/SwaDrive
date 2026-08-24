// Package auth contains SwaDrive's security-sensitive authentication primitives.
package auth

import (
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
)

var ErrInvalidPasswordHash = errors.New("invalid encoded password hash")

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

// HashPassword returns a PHC-style, self-describing Argon2id password hash.
func HashPassword(password string) (string, error) {
	return hashPassword(password, DefaultPasswordParameters(), rand.Reader)
}

// VerifyPassword checks password against an encoded hash. A malformed or
// defensively out-of-bounds hash returns ErrInvalidPasswordHash.
func VerifyPassword(password, encodedHash string) (bool, error) {
	parameters, salt, expectedHash, err := parsePasswordHash(encodedHash)
	if err != nil {
		return false, err
	}

	actualHash := argon2.IDKey(
		[]byte(password),
		salt,
		parameters.Iterations,
		parameters.MemoryKiB,
		parameters.Parallelism,
		parameters.KeyLength,
	)
	return subtle.ConstantTimeCompare(actualHash, expectedHash) == 1, nil
}

func hashPassword(password string, parameters PasswordParameters, random io.Reader) (string, error) {
	if err := validatePasswordParameters(parameters); err != nil {
		return "", err
	}

	salt := make([]byte, parameters.SaltLength)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}

	derivedHash := argon2.IDKey(
		[]byte(password),
		salt,
		parameters.Iterations,
		parameters.MemoryKiB,
		parameters.Parallelism,
		parameters.KeyLength,
	)

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

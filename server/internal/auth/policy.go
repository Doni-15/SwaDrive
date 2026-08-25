package auth

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MinimumUsernameLength  = 1
	MaximumUsernameLength  = 64
	MinimumPasswordRunes   = 12
	MaximumPasswordBytes   = 1024
	MaximumClientNameBytes = 256
)

var (
	ErrInvalidUsername   = errors.New("invalid username")
	ErrInvalidPassword   = errors.New("invalid password")
	ErrInvalidClientName = errors.New("invalid client name")
)

func CanonicalizeUsername(username string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(username))
	if len(canonical) < MinimumUsernameLength || len(canonical) > MaximumUsernameLength {
		return "", ErrInvalidUsername
	}

	for index := 0; index < len(canonical); index++ {
		character := canonical[index]
		alphanumeric := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if index == 0 || index == len(canonical)-1 {
			if !alphanumeric {
				return "", ErrInvalidUsername
			}
			continue
		}
		if !alphanumeric && character != '.' && character != '_' && character != '-' {
			return "", ErrInvalidUsername
		}
	}
	return canonical, nil
}

func ValidateNewPassword(password string) error {
	if !utf8.ValidString(password) || len(password) > MaximumPasswordBytes || utf8.RuneCountInString(password) < MinimumPasswordRunes {
		return ErrInvalidPassword
	}
	return nil
}

func validateLoginPassword(password string) error {
	if !utf8.ValidString(password) || len(password) == 0 || len(password) > MaximumPasswordBytes {
		return ErrInvalidPassword
	}
	return nil
}

func canonicalizeClientName(clientName string) (string, error) {
	canonical := strings.TrimSpace(clientName)
	if canonical == "" || len(canonical) > MaximumClientNameBytes || !utf8.ValidString(canonical) {
		return "", ErrInvalidClientName
	}
	for _, character := range canonical {
		if character < 0x20 || character == 0x7f {
			return "", ErrInvalidClientName
		}
	}
	return canonical, nil
}

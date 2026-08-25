// Package storage provides traversal-resistant access to SwaDrive's storage tree.
package storage

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaximumPathBytes      = 4096
	MaximumComponentBytes = 255
	MaximumPathDepth      = 128
)

var ErrInvalidPath = errors.New("invalid logical path")

type Path struct {
	value string
}

func ParsePath(value string, allowRoot bool) (Path, error) {
	if value == "" {
		if allowRoot {
			return Path{}, nil
		}
		return Path{}, ErrInvalidPath
	}
	if len(value) > MaximumPathBytes || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return Path{}, ErrInvalidPath
	}

	components := strings.Split(value, "/")
	if len(components) > MaximumPathDepth {
		return Path{}, ErrInvalidPath
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." || len(component) > MaximumComponentBytes {
			return Path{}, ErrInvalidPath
		}
		for _, character := range component {
			if character == 0 || unicode.IsControl(character) {
				return Path{}, ErrInvalidPath
			}
		}
	}
	return Path{value: value}, nil
}

func (path Path) String() string {
	return path.value
}

func (path Path) rootName() string {
	if path.value == "" {
		return "."
	}
	return path.value
}

func validInternalName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 256 {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

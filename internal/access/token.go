// Package access verifies the shared token required to enter a Voiced room.
package access

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	ErrInvalidToken     = errors.New("access token is invalid")
	ErrTokenUnavailable = errors.New("access token is unavailable")
)

// Verifier checks a token supplied by a browser.
type Verifier interface {
	Verify(token string) error
}

// FileVerifier reads the expected token for every verification. Replacing the
// file therefore rotates access without restarting the server.
type FileVerifier struct {
	path string
}

func NewFileVerifier(path string) (*FileVerifier, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("access token file path is required")
	}
	return &FileVerifier{path: path}, nil
}

func (v *FileVerifier) Verify(token string) error {
	expected, err := os.ReadFile(v.path)
	if err != nil {
		return ErrTokenUnavailable
	}

	expected = []byte(strings.TrimSpace(string(expected)))
	provided := []byte(strings.TrimSpace(token))
	if len(expected) == 0 {
		return ErrTokenUnavailable
	}
	if len(provided) == 0 || subtle.ConstantTimeCompare(expected, provided) != 1 {
		return ErrInvalidToken
	}
	return nil
}

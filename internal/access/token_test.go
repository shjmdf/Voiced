package access

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileVerifierUsesCurrentFileContents(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "access-token")
	if err := os.WriteFile(tokenFile, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	verifier, err := NewFileVerifier(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify("first-token"); err != nil {
		t.Fatalf("Verify(first-token) error = %v", err)
	}
	if err := verifier.Verify("wrong-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify(wrong-token) error = %v, want ErrInvalidToken", err)
	}

	if err := os.WriteFile(tokenFile, []byte("second-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify("first-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify(first-token) after rotation error = %v, want ErrInvalidToken", err)
	}
	if err := verifier.Verify("second-token"); err != nil {
		t.Fatalf("Verify(second-token) error = %v", err)
	}

	if err := os.Remove(tokenFile); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify("second-token"); !errors.Is(err, ErrTokenUnavailable) {
		t.Fatalf("Verify after removal error = %v, want ErrTokenUnavailable", err)
	}
}

func TestNewFileVerifierRequiresPath(t *testing.T) {
	if _, err := NewFileVerifier("  "); err == nil {
		t.Fatal("NewFileVerifier accepted an empty path")
	}
}

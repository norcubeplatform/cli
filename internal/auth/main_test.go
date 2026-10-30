package auth

import (
	"os"
	"testing"
)

// TestMain keeps the discovery cache out of the user's cache directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "norcube-auth-test-")
	if err != nil {
		panic(err)
	}
	metadataCacheDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

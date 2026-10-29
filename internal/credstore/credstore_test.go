package credstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	s := File{Dir: dir}
	if _, err := s.Get("profile:work"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if err := s.Set("profile:work", `{"refresh_token":"r"}`); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("profile:work"); err != nil || v != `{"refresh_token":"r"}` {
		t.Fatalf("%q %v", v, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "profile-work.json")); err != nil {
		t.Fatalf("readable file name expected: %v", err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "profile-work.json"))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("file mode %v", st.Mode().Perm())
		}
		dst, _ := os.Stat(dir)
		if dst.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v", dst.Mode().Perm())
		}
	}
	// Keys that aren't plain names are hashed, never used as paths.
	if err := s.Set("https://auth.test|refresh", "x"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get("https://auth.test|refresh"); v != "x" {
		t.Fatalf("%q", v)
	}
	if err := s.Delete("profile:work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("profile:work"); err != nil {
		t.Fatalf("deleting a missing key: %v", err)
	}
	if _, err := s.Get("profile:work"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestFallbackNoticeOnce(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	warnOnce(dir, &out)
	warnOnce(dir, &out)
	if n := bytes.Count(out.Bytes(), []byte("no OS keyring")); n != 1 {
		t.Fatalf("notice printed %d times", n)
	}
}

func TestOpenFile(t *testing.T) {
	s, kind, err := Open("file", t.TempDir(), nil)
	if err != nil || kind != "file" {
		t.Fatalf("%v %q", err, kind)
	}
	if _, ok := s.(File); !ok {
		t.Fatalf("%T", s)
	}
	if _, _, err := Open("env", t.TempDir(), nil); err == nil {
		t.Fatal("env store opened for writing")
	}
}

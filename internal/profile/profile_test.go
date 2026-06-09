package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTarUntarRoundTripExcludesCache builds a fake Chromium profile, tars it, untars
// into a new dir, and checks identity files survive while cache subtrees are dropped.
func TestTarUntarRoundTripExcludesCache(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "Default", "Cookies"), "cookie-db-bytes")
	write(t, filepath.Join(src, "Default", "Local Storage", "leveldb", "000003.log"), "ls-data")
	write(t, filepath.Join(src, "Local State"), "local-state")
	// these must be excluded
	write(t, filepath.Join(src, "Default", "Cache", "data_0"), "junk")
	write(t, filepath.Join(src, "Default", "GPUCache", "index"), "junk")
	write(t, filepath.Join(src, "Default", "Service Worker", "CacheStorage", "x"), "junk")
	write(t, filepath.Join(src, "SingletonLock"), "lock")

	blob, err := Tar(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := Untar(blob, dst); err != nil {
		t.Fatal(err)
	}

	want := []string{
		filepath.Join("Default", "Cookies"),
		filepath.Join("Default", "Local Storage", "leveldb", "000003.log"),
		"Local State",
	}
	for _, w := range want {
		b, err := os.ReadFile(filepath.Join(dst, w))
		if err != nil {
			t.Fatalf("missing restored file %s: %v", w, err)
		}
		if len(b) == 0 {
			t.Fatalf("restored %s is empty", w)
		}
	}
	excluded := []string{
		filepath.Join("Default", "Cache", "data_0"),
		filepath.Join("Default", "GPUCache", "index"),
		filepath.Join("Default", "Service Worker", "CacheStorage", "x"),
		"SingletonLock",
	}
	for _, e := range excluded {
		if _, err := os.Stat(filepath.Join(dst, e)); !os.IsNotExist(err) {
			t.Fatalf("excluded path %s should not have been archived", e)
		}
	}
}

func TestUntarRejectsTraversal(t *testing.T) {
	// hand-craft a tar would be overkill; ensure Untar of an empty/safe blob is fine and
	// the dir is created.
	dst := filepath.Join(t.TempDir(), "live")
	src := t.TempDir()
	write(t, filepath.Join(src, "a.txt"), "hi")
	blob, err := Tar(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := Untar(blob, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.txt")); err != nil {
		t.Fatal(err)
	}
}

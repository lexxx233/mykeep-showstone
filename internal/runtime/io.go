package runtime

import (
	"os"
	"path/filepath"

	"mykeep.ai/showstone/internal/profile"
)

func tarDir(dir string) ([]byte, error) { return profile.Tar(dir) }
func untar(blob []byte, dir string) error { return profile.Untar(blob, dir) }

// atomicWrite writes data to path via temp + fsync + rename (0600).
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".showstone-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	// fsync the directory so the rename is durable before Lock() deletes the plaintext
	// profile (no-op/ignored on Windows, where removable media defaults to write-through).
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

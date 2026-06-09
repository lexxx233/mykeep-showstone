// Package paths resolves Showstone's data directory from the binary's own location on
// the USB drive (the portability keystone), mirroring the other mykeep components so
// they agree on where mykeep_kb/ lives.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

const dataDirName = "mykeep_kb"

// Layout is the resolved data directory.
type Layout struct {
	DataDir  string
	Portable bool // false if we fell back to a host-local dir
}

// Resolve picks the data dir: MYKEEP_DATA_DIR override, else mykeep_kb/ beside the
// binary, else os.UserConfigDir()/mykeep/mykeep_kb (non-portable).
func Resolve() (Layout, error) {
	if dir := os.Getenv("MYKEEP_DATA_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Layout{}, err
		}
		return Layout{DataDir: dir, Portable: true}, nil
	}
	if base, ok := binaryDir(); ok {
		dataDir := filepath.Join(base, dataDirName)
		if writable(dataDir) {
			return Layout{DataDir: dataDir, Portable: true}, nil
		}
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return Layout{}, err
	}
	dataDir := filepath.Join(cfg, "mykeep", dataDirName)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Layout{}, err
	}
	return Layout{DataDir: dataDir, Portable: false}, nil
}

// ProfileEncPath is the sealed browser profile.
func (l Layout) ProfileEncPath() string {
	return filepath.Join(l.DataDir, "showstone", "showstone.profile.enc")
}

// LiveDir is the plaintext working --user-data-dir that exists only while unlocked.
func (l Layout) LiveDir() string { return filepath.Join(l.DataDir, ".showstone-live") }

// ChromeDir is where downloaded Chrome-for-Testing builds live.
func (l Layout) ChromeDir() string { return filepath.Join(l.DataDir, "showstone", "chrome") }

// ConfigPath is the standalone config (envelope + settings); absent under the suite.
func (l Layout) ConfigPath() string {
	return filepath.Join(l.DataDir, "showstone", "showstone.config.json")
}

func binaryDir() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if strings.HasPrefix(exe, os.TempDir()) || os.Getenv("MYKEEP_DEV") != "" {
		return "", false
	}
	if strings.Contains(dir, "/AppTranslocation/") {
		return "", false
	}
	return dir, true
}

func writable(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".mykeep-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

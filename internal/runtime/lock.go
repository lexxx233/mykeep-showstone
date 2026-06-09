package runtime

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrAlreadyRunning means another Showstone instance holds this profile.
var ErrAlreadyRunning = errors.New("showstone: another instance has this profile open")

// instanceLock is a portable single-writer guard: an exclusive lock file storing the
// owner PID. A stale lock (owner dead) is stolen. On unix this is precise; on Windows
// liveness can't be probed, so the lock is best-effort (documented).
type instanceLock struct{ path string }

func acquireLock(path string) (*instanceLock, error) {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
			_ = f.Close()
			return &instanceLock{path: path}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if stale(path) {
			_ = os.Remove(path)
			continue
		}
		return nil, ErrAlreadyRunning
	}
	return nil, ErrAlreadyRunning
}

func (l *instanceLock) release() {
	if l != nil {
		_ = os.Remove(l.path)
	}
}

func stale(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	// Signal(0) probes liveness on unix; on Windows it errors (treat as stale → steal).
	return p.Signal(syscall.Signal(0)) != nil
}

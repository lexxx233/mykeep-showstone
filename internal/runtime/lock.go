package runtime

import (
	"errors"
	"os"
)

// ErrAlreadyRunning means another Showstone instance holds this profile.
var ErrAlreadyRunning = errors.New("showstone: another instance has this profile open")

// instanceLock is a real kernel-held single-writer lock (flock on unix, LockFileEx on
// Windows) kept open for the process lifetime. The kernel releases it automatically if
// the process dies, so there are no stale-steal races and no Windows always-steal hole.
type instanceLock struct{ f *os.File }

func acquireLock(path string) (*instanceLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLocked) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return &instanceLock{f: f}, nil
}

func (l *instanceLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlockFile(l.f)
	_ = l.f.Close()
}

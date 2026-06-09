// Package runtime is Showstone's unlocked lifecycle: it turns an injected DEK + a data
// dir into a launched, sealed browser. Unlock = flock → resolve Chromium → unseal+untar
// the profile onto the stick → launch → restore audit/policy. Lock = kill the browser →
// tar the profile → seal it (+ session state) → atomic write → delete the plaintext →
// zero the DEK. The plaintext profile lives on the stick only while unlocked.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mykeep.ai/showstone/internal/browser"
	"mykeep.ai/showstone/internal/paths"
	"mykeep.ai/showstone/internal/secret"
	"mykeep.ai/showstone/internal/server"
)

// Options configures an unlocked runtime.
type Options struct {
	Version         string
	Headless        bool
	NoSandbox       bool
	EnableLAN       bool
	StrictDefault   bool
	UseToken        string
	ControlToken    string
	ControlSession  string
	SessionCookie   string
	ApprovalTimeout time.Duration
	Addr            string
	OnDownload      func(pct int) // progress for the first-launch Chrome download (may be nil)
}

// Runtime is one unlocked Showstone (browser + server + sealed-profile bookkeeping).
type Runtime struct {
	layout  paths.Layout
	opt     Options
	keys    *secret.KeyStore
	eng     browser.Engine
	srv     *server.Server
	lock    *instanceLock
	liveDir string
}

const (
	// restoreMarker is written into the live dir once a restore completes, so crash
	// recovery can tell a real (post-restore) session from a kill-mid-untar partial dir.
	// It is never sealed (excluded by the profile tar).
	restoreMarker = ".showstone-restore-ok"
	// AAD domains bind each sealed blob to its purpose so the two files (same DEK) can't
	// be swapped for one another.
	domainProfile = "showstone/profile/v1"
	domainState   = "showstone/state/v1"
)

func statePath(dataDir string) string {
	return filepath.Join(dataDir, "showstone", "showstone.state.enc")
}

// Open unlocks Showstone with an injected DEK (adopted, wiped on Lock or on any Open
// failure). It acquires the single-instance lock, recovers any crash-left profile,
// restores the sealed profile onto the stick, launches Chromium, and serves.
func Open(ctx context.Context, layout paths.Layout, dek []byte, opt Options) (rt *Runtime, err error) {
	r := &Runtime{layout: layout, opt: opt, liveDir: layout.LiveDir(), keys: secret.NewKeyStore(dek)}
	// On ANY failure, wipe the adopted DEK and release the lock (success transfers
	// ownership to r.Lock). Named returns make this cover every error path.
	defer func() {
		if err != nil {
			r.keys.Zero()
			r.teardownLock()
		}
	}()

	encPath := layout.ProfileEncPath()
	if merr := os.MkdirAll(filepath.Dir(encPath), 0o700); merr != nil {
		return nil, merr
	}
	lock, lerr := acquireLock(encPath + ".lock")
	if lerr != nil {
		return nil, lerr
	}
	r.lock = lock

	// Crash recovery for a leftover live dir from an UNCLEAN shutdown:
	//  - marker present  → a real post-restore session (newer than .enc) → reseal it.
	//  - marker absent    → a kill-mid-untar partial dir → discard; restore from .enc.
	if dirExists(r.liveDir) {
		if fileExists(filepath.Join(r.liveDir, restoreMarker)) {
			if rerr := r.resealStale(); rerr != nil {
				return nil, fmt.Errorf("recover stale profile: %w", rerr)
			}
		} else if rmErr := os.RemoveAll(r.liveDir); rmErr != nil {
			return nil, rmErr
		}
	}

	chromeBin, cerr := browser.ResolveChrome(ctx, layout.ChromeDir(), opt.OnDownload)
	if cerr != nil {
		return nil, cerr
	}

	if rerr := r.restoreProfile(); rerr != nil {
		_ = os.RemoveAll(r.liveDir) // never leave a partial plaintext dir behind
		return nil, rerr
	}

	eng, oerr := browser.OpenRod(ctx, browser.RodOptions{
		ChromeBin: chromeBin, LiveDir: r.liveDir, Headless: opt.Headless, NoSandbox: opt.NoSandbox})
	if oerr != nil {
		_ = os.RemoveAll(r.liveDir)
		return nil, oerr
	}
	r.eng = eng

	r.srv = server.New(eng, server.Options{
		EnableLAN: opt.EnableLAN, UseToken: opt.UseToken, ControlToken: opt.ControlToken,
		ControlSession: opt.ControlSession, SessionCookie: opt.SessionCookie,
		StrictDefault: opt.StrictDefault, ApprovalTimeout: opt.ApprovalTimeout,
		Version: opt.Version, Addr: opt.Addr,
	})
	r.loadState()
	return r, nil
}

// Server is the HTTP front (nil-safe only after Open).
func (r *Runtime) Server() *server.Server { return r.srv }

// Lock kills the browser, reseals the profile + session state, deletes the plaintext,
// and zeroizes the DEK. Idempotent.
func (r *Runtime) Lock() error {
	if r.keys == nil {
		return nil
	}
	if r.eng != nil {
		_ = r.eng.Close() // graceful close → flush leveldb, then kill; releases file handles
		r.eng = nil
	}
	var firstErr error
	if err := r.sealProfile(); err != nil {
		firstErr = err
	}
	if err := r.sealState(); err != nil && firstErr == nil {
		firstErr = err
	}
	// Only delete plaintext once the sealed copy is durably written.
	if firstErr == nil {
		if err := os.RemoveAll(r.liveDir); err != nil {
			firstErr = err
		}
	}
	r.keys.Zero()
	r.keys = nil
	r.teardownLock()
	return firstErr
}

func (r *Runtime) teardownLock() {
	if r.lock != nil {
		r.lock.release()
		r.lock = nil
	}
}

// restoreProfile unseals+untars the profile into the live dir (or creates an empty one
// on first launch), then writes the restore-complete marker as its final step.
func (r *Runtime) restoreProfile() error {
	enc := r.layout.ProfileEncPath()
	b, err := os.ReadFile(enc)
	if os.IsNotExist(err) {
		if mkErr := os.MkdirAll(r.liveDir, 0o700); mkErr != nil { // first launch: empty profile
			return mkErr
		}
		return r.markRestored()
	}
	if err != nil {
		return err
	}
	sealed, err := secret.Decode(b)
	if err != nil {
		return err
	}
	var tarBytes []byte
	if err := r.keys.Use(func(dek []byte) error {
		t, oerr := secret.OpenBlobAAD(dek, sealed, []byte(domainProfile))
		tarBytes = t
		return oerr
	}); err != nil {
		return err
	}
	if err := untar(tarBytes, r.liveDir); err != nil {
		return err
	}
	return r.markRestored()
}

func (r *Runtime) markRestored() error {
	return os.WriteFile(filepath.Join(r.liveDir, restoreMarker), []byte("ok"), 0o600)
}

// sealProfile tars the live dir and writes the sealed profile atomically.
func (r *Runtime) sealProfile() error {
	tarBytes, err := tarDir(r.liveDir)
	if err != nil {
		return err
	}
	var sealed secret.Sealed
	if err := r.keys.Use(func(dek []byte) error {
		s, serr := secret.SealBlobAAD(dek, tarBytes, []byte(domainProfile))
		sealed = s
		return serr
	}); err != nil {
		return err
	}
	return atomicWrite(r.layout.ProfileEncPath(), secret.Encode(sealed))
}

// resealStale captures a crash-left (marker-present) session into the seal, keeping the
// prior seal as .bak, then removes the plaintext so Open proceeds via the normal path.
func (r *Runtime) resealStale() error {
	enc := r.layout.ProfileEncPath()
	if fileExists(enc) {
		_ = os.Rename(enc, enc+".bak") // belt-and-braces: retain the previous good seal
	}
	if err := r.sealProfile(); err != nil {
		return err
	}
	return os.RemoveAll(r.liveDir)
}

func (r *Runtime) sealState() error {
	if r.srv == nil {
		return nil
	}
	b, err := json.Marshal(r.srv.ExportState())
	if err != nil {
		return err
	}
	var sealed secret.Sealed
	if err := r.keys.Use(func(dek []byte) error {
		s, serr := secret.SealBlobAAD(dek, b, []byte(domainState))
		sealed = s
		return serr
	}); err != nil {
		return err
	}
	return atomicWrite(statePath(r.layout.DataDir), secret.Encode(sealed))
}

func (r *Runtime) loadState() {
	b, err := os.ReadFile(statePath(r.layout.DataDir))
	if os.IsNotExist(err) {
		return // first launch — no state yet
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "showstone: warning: cannot read session state: %v\n", err)
		return
	}
	sealed, err := secret.Decode(b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "showstone: warning: session state corrupt (ignored): %v\n", err)
		return
	}
	var plain []byte
	if r.keys.Use(func(dek []byte) error {
		p, oerr := secret.OpenBlobAAD(dek, sealed, []byte(domainState))
		plain = p
		return oerr
	}) != nil {
		fmt.Fprintln(os.Stderr, "showstone: warning: session state failed authentication — possible tampering; policy reset to defaults")
		return
	}
	var blob server.StateBlob
	if json.Unmarshal(plain, &blob) == nil {
		r.srv.LoadState(blob)
	}
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

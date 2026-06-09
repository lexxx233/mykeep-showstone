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

func statePath(dataDir string) string {
	return filepath.Join(dataDir, "showstone", "showstone.state.enc")
}

// Open unlocks Showstone with an injected DEK. The dek is adopted and wiped on Lock.
func Open(ctx context.Context, layout paths.Layout, dek []byte, opt Options) (*Runtime, error) {
	r := &Runtime{layout: layout, opt: opt, liveDir: layout.LiveDir(),
		keys: secret.NewKeyStore(dek)}

	encPath := layout.ProfileEncPath()
	if err := os.MkdirAll(filepath.Dir(encPath), 0o700); err != nil {
		r.keys.Zero()
		return nil, err
	}
	lock, err := acquireLock(encPath + ".lock")
	if err != nil {
		r.keys.Zero()
		return nil, err
	}
	r.lock = lock

	// Crash recovery: a leftover live dir means a previous run didn't Lock cleanly.
	// Reseal it (don't lose the session) then restore from the seal — one code path.
	if dirExists(r.liveDir) {
		if err := r.resealStale(); err != nil {
			r.teardownLock()
			return nil, fmt.Errorf("recover stale profile: %w", err)
		}
	}

	chromeBin, err := browser.ResolveChrome(ctx, layout.ChromeDir(), opt.OnDownload)
	if err != nil {
		r.teardownLock()
		return nil, err
	}

	if err := r.restoreProfile(); err != nil {
		r.teardownLock()
		return nil, err
	}

	eng, err := browser.OpenRod(ctx, browser.RodOptions{
		ChromeBin: chromeBin, LiveDir: r.liveDir, Headless: opt.Headless, NoSandbox: opt.NoSandbox})
	if err != nil {
		_ = os.RemoveAll(r.liveDir)
		r.teardownLock()
		return nil, err
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

// restoreProfile unseals+untars the profile into the live dir, or creates an empty one.
func (r *Runtime) restoreProfile() error {
	enc := r.layout.ProfileEncPath()
	b, err := os.ReadFile(enc)
	if os.IsNotExist(err) {
		return os.MkdirAll(r.liveDir, 0o700) // first launch: empty profile
	}
	if err != nil {
		return err
	}
	sealed, err := secret.Decode(b)
	if err != nil {
		return err
	}
	var tar []byte
	if err := r.keys.Use(func(dek []byte) error {
		t, oerr := secret.OpenBlob(dek, sealed)
		tar = t
		return oerr
	}); err != nil {
		return err
	}
	return untar(tar, r.liveDir)
}

// sealProfile tars the live dir and writes the sealed profile atomically.
func (r *Runtime) sealProfile() error {
	tarBytes, err := tarDir(r.liveDir)
	if err != nil {
		return err
	}
	var sealed secret.Sealed
	if err := r.keys.Use(func(dek []byte) error {
		s, serr := secret.SealBlob(dek, tarBytes)
		sealed = s
		return serr
	}); err != nil {
		return err
	}
	return atomicWrite(r.layout.ProfileEncPath(), secret.Encode(sealed))
}

// resealStale captures a crash-left plaintext profile into the seal, then removes it, so
// Open can proceed via the normal restore path.
func (r *Runtime) resealStale() error {
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
		s, serr := secret.SealBlob(dek, b)
		sealed = s
		return serr
	}); err != nil {
		return err
	}
	return atomicWrite(statePath(r.layout.DataDir), secret.Encode(sealed))
}

func (r *Runtime) loadState() {
	b, err := os.ReadFile(statePath(r.layout.DataDir))
	if err != nil {
		return
	}
	sealed, err := secret.Decode(b)
	if err != nil {
		return
	}
	var plain []byte
	if r.keys.Use(func(dek []byte) error {
		p, oerr := secret.OpenBlob(dek, sealed)
		plain = p
		return oerr
	}) != nil {
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

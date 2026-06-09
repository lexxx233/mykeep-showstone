package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mykeep.ai/showstone/internal/paths"
)

// TestLiveRuntimeSealCycle drives the full lifecycle with a REAL Chromium: Open launches
// the browser on a (possibly restored) profile; Lock kills it, seals the profile, and
// removes the plaintext; reopen relaunches on the sealed profile; a wrong key is
// rejected. Gated on SHOWSTONE_LIVE=1.
func TestLiveRuntimeSealCycle(t *testing.T) {
	if os.Getenv("SHOWSTONE_LIVE") == "" {
		t.Skip("set SHOWSTONE_LIVE=1 to run the live runtime test")
	}
	dir := t.TempDir()
	layout := paths.Layout{DataDir: dir, Portable: true}
	dek := bytes.Repeat([]byte{42}, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	opt := Options{Headless: true, NoSandbox: true, UseToken: "u", ApprovalTimeout: time.Second}

	rt, err := Open(ctx, layout, dek, opt)
	if err != nil {
		t.Skipf("open failed (engine unavailable?): %v", err)
	}
	if _, err := rt.eng.Navigate(ctx, "data:text/html,<h1>seed</h1>"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if !dirExists(layout.LiveDir()) {
		t.Fatal("live dir should exist while unlocked")
	}
	if err := rt.Lock(); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if _, err := os.Stat(layout.ProfileEncPath()); err != nil {
		t.Fatalf("sealed profile missing after lock: %v", err)
	}
	if dirExists(layout.LiveDir()) {
		t.Fatal("plaintext live dir should be gone after clean lock")
	}

	// reopen with the same key — must relaunch on the restored profile
	rt2, err := Open(ctx, layout, dek, opt)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := rt2.eng.Navigate(ctx, "data:text/html,<h1>again</h1>"); err != nil {
		t.Fatalf("navigate after reopen: %v", err)
	}
	if err := rt2.Lock(); err != nil {
		t.Fatalf("lock 2: %v", err)
	}

	// wrong key cannot reopen
	if _, err := Open(ctx, layout, bytes.Repeat([]byte{7}, 32), opt); err == nil {
		t.Fatal("reopen with wrong key should fail")
	} else {
		// the failed Open may have left a lock; clean it for hygiene
		_ = os.Remove(filepath.Join(layout.DataDir, "showstone", "showstone.profile.enc.lock"))
	}
}

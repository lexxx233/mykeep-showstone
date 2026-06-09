package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mykeep.ai/showstone/internal/browser"
	"mykeep.ai/showstone/internal/paths"
	"mykeep.ai/showstone/internal/secret"
	"mykeep.ai/showstone/internal/server"
)

func newRT(dir string, dek []byte) *Runtime {
	layout := paths.Layout{DataDir: dir, Portable: true}
	_ = os.MkdirAll(filepath.Dir(layout.ProfileEncPath()), 0o700)
	return &Runtime{layout: layout, liveDir: layout.LiveDir(), keys: secret.NewKeyStore(dek)}
}

// TestProfileSealRoundTrip proves the browser profile seals on lock and restores under
// the same key — and a wrong key cannot open it.
func TestProfileSealRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dek := bytes.Repeat([]byte{7}, 32)

	r := newRT(dir, dek)
	cookie := filepath.Join(r.liveDir, "Default", "Cookies")
	if err := os.MkdirAll(filepath.Dir(cookie), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookie, []byte("session=abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	// a cache file that must be excluded from the seal
	_ = os.MkdirAll(filepath.Join(r.liveDir, "Default", "Cache"), 0o700)
	_ = os.WriteFile(filepath.Join(r.liveDir, "Default", "Cache", "junk"), []byte("x"), 0o600)

	if err := r.sealProfile(); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := os.Stat(r.layout.ProfileEncPath()); err != nil {
		t.Fatalf("sealed profile missing: %v", err)
	}
	_ = os.RemoveAll(r.liveDir)

	// restore with the same key
	r2 := newRT(dir, dek)
	if err := r2.restoreProfile(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(r2.liveDir, "Default", "Cookies"))
	if err != nil || string(b) != "session=abc" {
		t.Fatalf("cookie not restored: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(r2.liveDir, "Default", "Cache", "junk")); !os.IsNotExist(err) {
		t.Fatal("cache should not have been sealed")
	}

	// a wrong key cannot open the sealed profile
	r3 := newRT(dir, bytes.Repeat([]byte{9}, 32))
	if err := r3.restoreProfile(); err == nil {
		t.Fatal("restore with wrong key should fail")
	}
}

// TestStateSealRoundTrip proves audit + policy persist across lock/unlock.
func TestStateSealRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dek := bytes.Repeat([]byte{3}, 32)

	r := newRT(dir, dek)
	r.srv = server.New(browser.NewFake(), server.Options{})
	r.srv.LoadState(server.StateBlob{Trusted: []string{"shop.example.com"}, Strict: true})
	if err := r.sealState(); err != nil {
		t.Fatalf("sealState: %v", err)
	}

	r2 := newRT(dir, dek)
	r2.srv = server.New(browser.NewFake(), server.Options{})
	r2.loadState()
	got := r2.srv.ExportState()
	if !got.Strict || len(got.Trusted) != 1 || got.Trusted[0] != "shop.example.com" {
		t.Fatalf("state not restored: %+v", got)
	}
}

// TestFirstLaunchEmptyProfile proves restore on a fresh dir just creates an empty live dir.
func TestFirstLaunchEmptyProfile(t *testing.T) {
	dir := t.TempDir()
	r := newRT(dir, bytes.Repeat([]byte{1}, 32))
	if err := r.restoreProfile(); err != nil {
		t.Fatal(err)
	}
	if !dirExists(r.liveDir) {
		t.Fatal("first-launch live dir not created")
	}
}

package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveBrowse exercises a REAL Chromium: download/resolve the engine, launch it
// headless, navigate, snapshot (text + indexed elements), screenshot, and act by index.
// Gated on SHOWSTONE_LIVE=1 so ordinary `go test ./...` and CI never download ~150 MB or
// require a browser. Run: SHOWSTONE_LIVE=1 go test ./internal/browser -run Live -v
func TestLiveBrowse(t *testing.T) {
	if os.Getenv("SHOWSTONE_LIVE") == "" {
		t.Skip("set SHOWSTONE_LIVE=1 to run the live Chromium test")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	bin, err := ResolveChrome(ctx, filepath.Join(dir, "chrome"), func(p int) {})
	if err != nil {
		t.Skipf("chrome unavailable: %v", err)
	}
	t.Logf("chrome: %s", bin)

	eng, err := OpenRod(ctx, RodOptions{
		ChromeBin: bin, LiveDir: filepath.Join(dir, "live"), Headless: true, NoSandbox: true,
	})
	if err != nil {
		t.Skipf("chrome launch failed (missing system libs?): %v", err)
	}
	defer eng.Close()

	html := "data:text/html," +
		"<title>T</title><h1>Hello Showstone</h1>" +
		"<a href='https://example.com/x'>A Link</a>" +
		"<button id=b>Press Me</button>" +
		"<input id=i placeholder='your name'>"
	snap, err := eng.Navigate(ctx, html)
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if !strings.Contains(snap.Text, "Hello Showstone") {
		t.Fatalf("text missing heading: %q", snap.Text)
	}
	if snap.ElementCount < 3 {
		t.Fatalf("expected >=3 interactive elements, got %d: %+v", snap.ElementCount, snap.Elements)
	}
	if snap.SnapshotID == "" {
		t.Fatal("no snapshot id")
	}

	shot, err := eng.Screenshot(ctx, false)
	if err != nil || len(shot.PNG) < 8 || string(shot.PNG[1:4]) != "PNG" {
		t.Fatalf("screenshot bad: err=%v len=%d", err, len(shot.PNG))
	}

	// type into the input (find its index)
	inputIdx := -1
	for _, e := range snap.Elements {
		if e.Tag == "input" {
			inputIdx = e.Index
		}
	}
	if inputIdx < 0 {
		t.Fatal("no input element found")
	}
	if _, err := eng.Act(ctx, ActRequest{Action: "type", Index: inputIdx, Text: "Ada"}); err != nil {
		t.Fatalf("type act: %v", err)
	}
	after, err := eng.Snapshot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range after.Elements {
		if e.Tag == "input" && e.Value == "Ada" {
			found = true
		}
	}
	if !found {
		t.Fatalf("typed value not reflected: %+v", after.Elements)
	}
	t.Logf("live browse OK: %d elements, screenshot %dx%d", snap.ElementCount, shot.Width, shot.Height)
}

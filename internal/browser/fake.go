package browser

import (
	"context"
	"sync"
)

// FakeEngine is a programmable in-memory Engine for tests (no Chromium). Set Cur to
// control what snapshots return; Acts records every action; Nav records navigations.
type FakeEngine struct {
	mu      sync.Mutex
	Cur     Snapshot
	Acts    []ActRequest
	Nav     []string
	Filled   map[int]string
	Profiles []HostCookies
	Cleared  bool
	Closed   bool
	// FailAct, if non-nil, is returned by Act once (then cleared) — to script errors.
	FailAct error
}

// NewFake returns a fake engine seeded with one element so index 0 resolves.
func NewFake() *FakeEngine {
	return &FakeEngine{
		Filled: map[int]string{},
		Cur: Snapshot{
			URL: "about:blank", Title: "", Text: "", SnapshotID: "seed",
			Elements: []Element{{Index: 0, Role: "link", Tag: "a", Name: "Home", Href: "/"}},
			ElementCount: 1, Page: 1, PageCount: 1,
		},
	}
}

func (f *FakeEngine) bump() { f.Cur.SnapshotID = randID() }

func (f *FakeEngine) Navigate(_ context.Context, url string) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Nav = append(f.Nav, url)
	f.Cur.URL = url
	f.bump()
	return f.Cur, nil
}

func (f *FakeEngine) Snapshot(_ context.Context, page int) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Cur.Page = page
	if f.Cur.SnapshotID == "" {
		f.bump()
	}
	return f.Cur, nil
}

func (f *FakeEngine) Act(_ context.Context, req ActRequest) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailAct != nil {
		err := f.FailAct
		f.FailAct = nil
		f.bump()
		return f.Cur, err
	}
	if req.SnapshotID != "" && req.SnapshotID != f.Cur.SnapshotID {
		f.bump()
		return f.Cur, ErrStaleSnapshot
	}
	f.Acts = append(f.Acts, req)
	f.bump()
	return f.Cur, nil
}

func (f *FakeEngine) Screenshot(_ context.Context, full bool) (Screenshot, error) {
	return Screenshot{PNG: onePixelPNG, Width: 1, Height: 1, Full: full}, nil
}

func (f *FakeEngine) State(_ context.Context) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return State{URL: f.Cur.URL, Title: f.Cur.Title, SnapshotID: f.Cur.SnapshotID, CanGoBack: f.Cur.CanGoBack}, nil
}

func (f *FakeEngine) Element(index int) (Element, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.Cur.Elements {
		if e.Index == index {
			return e, true
		}
	}
	return Element{}, false
}

func (f *FakeEngine) Fill(_ context.Context, index int, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Filled[index] = value
	return nil
}

func (f *FakeEngine) ClearSession(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Cleared = true
	return nil
}

// Profiles, if set, is returned by Profile.
func (f *FakeEngine) Profile(_ context.Context) ([]HostCookies, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]HostCookies(nil), f.Profiles...), nil
}

func (f *FakeEngine) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Closed = true
	return nil
}

// onePixelPNG is a valid 1×1 transparent PNG.
var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mykeep.ai/showstone/internal/paths"
)

func newApp(t *testing.T) http.Handler {
	t.Helper()
	a, err := New(paths.Layout{DataDir: t.TempDir(), Portable: true}, "test", "127.0.0.1:8771", 0)
	if err != nil {
		t.Fatal(err)
	}
	return loopbackGuard(a.touch(a.handler()))
}

func req(method, path, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.RemoteAddr = "127.0.0.1:5555"
	r.Host = "127.0.0.1:8771"
	return r
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGUILockedSurface(t *testing.T) {
	h := newApp(t)
	// serves the page
	if w := do(h, req("GET", "/", "")); w.Code != 200 || !strings.Contains(w.Body.String(), "Showstone") {
		t.Fatalf("GET / => %d", w.Code)
	}
	// state reports first launch, locked
	w := do(h, req("GET", "/api/state", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"first_launch":true`) || !strings.Contains(w.Body.String(), `"unlocked":false`) {
		t.Fatalf("state => %d %s", w.Code, w.Body.String())
	}
	// the agent planes are 423 while locked
	if w := do(h, req("GET", "/v1/showstone/snapshot", "")); w.Code != http.StatusLocked {
		t.Fatalf("locked snapshot => %d, want 423", w.Code)
	}
	// empty password rejected
	if w := do(h, req("POST", "/api/setup", `{"password":""}`)); w.Code != 400 {
		t.Fatalf("empty setup => %d, want 400", w.Code)
	}
}

func TestGUILoopbackGuard(t *testing.T) {
	h := newApp(t)
	r := req("GET", "/api/state", "")
	r.Host = "evil.example.com"
	if w := do(h, r); w.Code != http.StatusForbidden {
		t.Fatalf("evil Host => %d, want 403", w.Code)
	}
	r = req("GET", "/api/state", "")
	r.RemoteAddr = "203.0.113.5:9999"
	if w := do(h, r); w.Code != http.StatusForbidden {
		t.Fatalf("remote socket => %d, want 403", w.Code)
	}
}

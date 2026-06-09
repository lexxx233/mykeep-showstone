package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mykeep.ai/showstone/internal/browser"
)

const (
	useTok  = "use-token"
	ctrlTok = "control-token"
)

func newServer(t *testing.T, f *browser.FakeEngine) http.Handler {
	t.Helper()
	s := New(f, Options{UseToken: useTok, ControlToken: ctrlTok, ApprovalTimeout: 2 * time.Second})
	return s.Handler()
}

func req(method, path, token, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.RemoteAddr = "127.0.0.1:5555"
	r.Host = "127.0.0.1:8771"
	if token != "" {
		r.Header.Set("X-Showstone-Token", token)
	}
	return r
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestUsePlaneRequiresToken(t *testing.T) {
	h := newServer(t, browser.NewFake())
	// guide is token-free
	if w := do(h, req("GET", "/v1/showstone/guide", "", "")); w.Code != 200 {
		t.Fatalf("guide => %d", w.Code)
	}
	// navigate without token => 401
	if w := do(h, req("POST", "/v1/showstone/navigate", "", `{"url":"https://example.com"}`)); w.Code != 401 {
		t.Fatalf("navigate no token => %d, want 401", w.Code)
	}
	// with token => 200
	if w := do(h, req("POST", "/v1/showstone/navigate", useTok, `{"url":"https://example.com"}`)); w.Code != 200 {
		t.Fatalf("navigate with token => %d", w.Code)
	}
}

func TestDriveLoop(t *testing.T) {
	f := browser.NewFake()
	h := newServer(t, f)
	w := do(h, req("POST", "/v1/showstone/navigate", useTok, `{"url":"https://example.com"}`))
	var env envelope
	mustJSON(t, w, &env)
	if !env.OK || env.URL != "https://example.com" || len(env.Elements) != 1 {
		t.Fatalf("navigate env: %+v", env)
	}
	// benign click on index 0 (the seeded "Home" link)
	w = do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"click","index":0}`))
	mustJSON(t, w, &env)
	if !env.OK || len(f.Acts) != 1 || f.Acts[0].Action != "click" {
		t.Fatalf("click not recorded: code=%d acts=%v", w.Code, f.Acts)
	}
}

func TestScreenshot(t *testing.T) {
	h := newServer(t, browser.NewFake())
	w := do(h, req("GET", "/v1/showstone/screenshot", useTok, ""))
	var m map[string]any
	mustJSON(t, w, &m)
	if m["ok"] != true || m["image_b64"] == "" {
		t.Fatalf("screenshot json: %v", m)
	}
	w = do(h, req("GET", "/v1/showstone/screenshot?format=png", useTok, ""))
	if ct := w.Header().Get("Content-Type"); ct != "image/png" || w.Body.Len() < 8 {
		t.Fatalf("png screenshot ct=%q len=%d", ct, w.Body.Len())
	}
}

func TestStaleSnapshot(t *testing.T) {
	h := newServer(t, browser.NewFake())
	w := do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"click","index":0,"snapshot_id":"OLD"}`))
	var env envelope
	mustJSON(t, w, &env)
	if w.Code != 200 || env.OK || env.Error != "stale_snapshot" || env.SnapshotID == "" {
		t.Fatalf("stale => code=%d env=%+v (want 200, ok=false, fresh snapshot)", w.Code, env)
	}
}

func TestSensitiveClickBlocksThenApproved(t *testing.T) {
	f := browser.NewFake()
	f.Cur.URL = "https://shop.example.com/cart"
	f.Cur.Elements = []browser.Element{{Index: 0, Role: "button", Tag: "button", Name: "Place order"}}
	f.Cur.SnapshotID = "s1"
	h := newServer(t, f)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"click","index":0}`)) }()

	id := waitForApproval(t, h)
	// approve
	if w := do(h, req("POST", "/api/showstone/approvals/"+id+"/decide", ctrlTok, `{"approve":true}`)); w.Code != 200 {
		t.Fatalf("decide => %d", w.Code)
	}
	w := <-done
	var env envelope
	mustJSON(t, w, &env)
	if w.Code != 200 || !env.OK || env.Approval == nil || env.Approval.Decision != "approved" {
		t.Fatalf("approved act => code=%d env=%+v", w.Code, env)
	}
}

func TestSensitiveClickDenied(t *testing.T) {
	f := browser.NewFake()
	f.Cur.URL = "https://shop.example.com/cart"
	f.Cur.Elements = []browser.Element{{Index: 0, Role: "button", Tag: "button", Name: "Delete account"}}
	h := newServer(t, f)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"click","index":0}`)) }()
	id := waitForApproval(t, h)
	do(h, req("POST", "/api/showstone/approvals/"+id+"/decide", ctrlTok, `{"approve":false}`))
	w := <-done
	if w.Code != 403 || !strings.Contains(w.Body.String(), "approval_denied") {
		t.Fatalf("denied act => %d %s", w.Code, w.Body.String())
	}
	if len(f.Acts) != 0 {
		t.Fatal("denied action should not have executed")
	}
}

func TestTrustedHostAutoApproves(t *testing.T) {
	f := browser.NewFake()
	f.Cur.URL = "https://shop.example.com/cart"
	f.Cur.Elements = []browser.Element{{Index: 0, Role: "button", Tag: "button", Name: "Place order"}}
	h := newServer(t, f)
	// trust the host
	do(h, req("POST", "/api/showstone/trust", ctrlTok, `{"host":"shop.example.com","trusted":true}`))
	// now the sensitive click runs without blocking
	w := do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"click","index":0}`))
	var env envelope
	mustJSON(t, w, &env)
	if w.Code != 200 || env.Approval == nil || env.Approval.Decision != "auto" || len(f.Acts) != 1 {
		t.Fatalf("trusted auto => code=%d env=%+v acts=%d", w.Code, env, len(f.Acts))
	}
}

func TestPasswordFieldNeverAutoApprovesEvenTrusted(t *testing.T) {
	f := browser.NewFake()
	f.Cur.URL = "https://bank.example.com/login"
	f.Cur.Elements = []browser.Element{{Index: 0, Role: "textbox", Tag: "input", Type: "password", Name: "Password"}}
	h := newServer(t, f)
	do(h, req("POST", "/api/showstone/trust", ctrlTok, `{"host":"bank.example.com","trusted":true}`))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"type","index":0,"text":"hunter2"}`))
	}()
	// it must BLOCK (hard) despite the trusted host
	id := waitForApproval(t, h)
	do(h, req("POST", "/api/showstone/approvals/"+id+"/decide", ctrlTok, `{"approve":true}`))
	<-done
}

func TestControlPlaneRejectsUseToken(t *testing.T) {
	h := newServer(t, browser.NewFake())
	if w := do(h, req("GET", "/api/showstone/approvals", useTok, "")); w.Code != 401 {
		t.Fatalf("control with use token => %d, want 401", w.Code)
	}
	if w := do(h, req("GET", "/api/showstone/approvals", ctrlTok, "")); w.Code != 200 {
		t.Fatalf("control with control token => %d, want 200", w.Code)
	}
}

func TestAuditChainVerifies(t *testing.T) {
	h := newServer(t, browser.NewFake())
	do(h, req("POST", "/v1/showstone/navigate", useTok, `{"url":"https://a.com"}`))
	do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"click","index":0}`))
	do(h, req("GET", "/v1/showstone/screenshot", useTok, ""))
	w := do(h, req("GET", "/api/showstone/audit/verify", ctrlTok, ""))
	var m map[string]any
	mustJSON(t, w, &m)
	if m["ok"] != true {
		t.Fatalf("audit verify => %v", m)
	}
	w = do(h, req("GET", "/api/showstone/audit", ctrlTok, ""))
	if !strings.Contains(w.Body.String(), "navigate") {
		t.Fatalf("audit list missing navigate: %s", w.Body.String())
	}
}

func TestLoopbackGuardOnControl(t *testing.T) {
	h := newServer(t, browser.NewFake())
	r := req("GET", "/api/showstone/approvals", ctrlTok, "")
	r.RemoteAddr = "203.0.113.9:9999"
	if w := do(h, r); w.Code != 403 {
		t.Fatalf("non-loopback control => %d, want 403", w.Code)
	}
}

// --- helpers ---

func waitForApproval(t *testing.T, h http.Handler) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		w := do(h, req("GET", "/api/showstone/approvals", ctrlTok, ""))
		var resp struct {
			Pending []struct {
				ID string `json:"id"`
			} `json:"pending"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if len(resp.Pending) > 0 {
			return resp.Pending[0].ID
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("no approval appeared")
	return ""
}

func mustJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("bad json (code %d): %v\n%s", w.Code, err, w.Body.String())
	}
}

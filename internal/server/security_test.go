package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mykeep.ai/showstone/internal/browser"
)

func newServerTO(t *testing.T, f *browser.FakeEngine, to time.Duration) http.Handler {
	t.Helper()
	s := New(f, Options{UseToken: useTok, ControlToken: ctrlTok, ApprovalTimeout: to})
	return s.Handler()
}

// blocks runs a USE-plane request in the background and returns its recorder once done.
func blocks(h http.Handler, body string) chan *httptest.ResponseRecorder {
	ch := make(chan *httptest.ResponseRecorder, 1)
	go func() { ch <- do(h, req("POST", "/v1/showstone/act", useTok, body)) }()
	return ch
}

// TestSelectorBypassIsGated: a selector-targeted mutating action must NOT auto-pass —
// the classifier can't see the element, so it fails closed to a hard approval gate.
func TestSelectorBypassIsGated(t *testing.T) {
	f := browser.NewFake()
	f.Cur.URL = "https://shop.example.com/cart"
	h := newServer(t, f)
	// even trusting the host must not let a selector click through (hard gate).
	do(h, req("POST", "/api/showstone/trust", ctrlTok, `{"host":"shop.example.com","trusted":true}`))
	done := blocks(h, `{"action":"click","selector":"button.checkout"}`)
	id := waitForApproval(t, h) // proves it blocked instead of auto-running
	do(h, req("POST", "/api/showstone/approvals/"+id+"/decide", ctrlTok, `{"approve":false}`))
	w := <-done
	if w.Code != 403 {
		t.Fatalf("selector click => %d, want 403 (gated)", w.Code)
	}
	if len(f.Acts) != 0 {
		t.Fatal("denied selector click must not execute")
	}
}

// TestActNavigateStrictBlocked: act{navigate} must obey strict mode (it previously
// bypassed the gate entirely).
func TestActNavigateStrictBlocked(t *testing.T) {
	f := browser.NewFake()
	h := newServer(t, f)
	do(h, req("POST", "/api/showstone/strict", ctrlTok, `{"on":true}`))
	done := blocks(h, `{"action":"navigate","url":"https://evil.com"}`)
	id := waitForApproval(t, h)
	do(h, req("POST", "/api/showstone/approvals/"+id+"/decide", ctrlTok, `{"approve":false}`))
	w := <-done
	if w.Code != 403 {
		t.Fatalf("act-navigate under strict => %d, want 403", w.Code)
	}
	if len(f.Acts) != 0 {
		t.Fatal("denied act-navigate must not execute")
	}
}

func TestDangerousSchemesBlocked(t *testing.T) {
	h := newServer(t, browser.NewFake())
	for _, u := range []string{"file:///etc/passwd", "chrome://settings", "view-source:https://x.com"} {
		if w := do(h, req("POST", "/v1/showstone/navigate", useTok, `{"url":"`+u+`"}`)); w.Code != 400 {
			t.Fatalf("navigate %s => %d, want 400", u, w.Code)
		}
		if w := do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"navigate","url":"`+u+`"}`)); w.Code != 400 {
			t.Fatalf("act-navigate %s => %d, want 400", u, w.Code)
		}
	}
	// https is allowed
	if w := do(h, req("POST", "/v1/showstone/navigate", useTok, `{"url":"https://ok.com"}`)); w.Code != 200 {
		t.Fatalf("https navigate => %d, want 200", w.Code)
	}
}

// TestPressEnterGated: Enter activates/submits, so it must be gated like a click;
// a non-activator key (Tab) stays benign.
func TestPressEnterGated(t *testing.T) {
	f := browser.NewFake()
	f.Cur.URL = "https://untrusted.example.com"
	f.Cur.Elements = []browser.Element{{Index: 0, Role: "textbox", Tag: "input", Name: "search"}}
	h := newServer(t, f)

	done := blocks(h, `{"action":"press","index":0,"key":"Enter"}`)
	id := waitForApproval(t, h) // Enter = soft submit → blocks on an untrusted host
	do(h, req("POST", "/api/showstone/approvals/"+id+"/decide", ctrlTok, `{"approve":true}`))
	if w := <-done; w.Code != 200 {
		t.Fatalf("approved press Enter => %d", w.Code)
	}
	// Tab is benign — runs without blocking
	if w := do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"press","index":0,"key":"Tab"}`)); w.Code != 200 {
		t.Fatalf("press Tab => %d, want 200 auto", w.Code)
	}
}

// TestSecretValueRedacted: a password field's value is never returned to the agent.
func TestSecretValueRedacted(t *testing.T) {
	f := browser.NewFake()
	f.Cur.Elements = []browser.Element{{Index: 0, Role: "textbox", Tag: "input", Type: "password", Name: "Password", Value: "hunter2"}}
	h := newServer(t, f)
	w := do(h, req("GET", "/v1/showstone/snapshot", useTok, ""))
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatalf("password value leaked in snapshot: %s", w.Body.String())
	}
}

// TestTimeoutDecisionReported: a fail-closed timeout is reported as "timeout", not a
// human "denied".
func TestTimeoutDecisionReported(t *testing.T) {
	f := browser.NewFake()
	f.Cur.URL = "https://shop.example.com"
	f.Cur.Elements = []browser.Element{{Index: 0, Role: "button", Tag: "button", Name: "Place order"}}
	h := newServerTO(t, f, 120*time.Millisecond)
	w := do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"click","index":0}`)) // no decide → times out
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"decision":"timeout"`) {
		t.Fatalf("timeout => %d %s", w.Code, w.Body.String())
	}
}

// TestFailedActAudited: a failed act (stale snapshot) still appends an audit entry.
func TestFailedActAudited(t *testing.T) {
	h := newServer(t, browser.NewFake())
	do(h, req("POST", "/v1/showstone/act", useTok, `{"action":"click","index":0,"snapshot_id":"OLD"}`))
	w := do(h, req("GET", "/api/showstone/audit", ctrlTok, ""))
	if !strings.Contains(w.Body.String(), `"decision":"error"`) {
		t.Fatalf("failed act not audited: %s", w.Body.String())
	}
}

func TestUsePlaneLoopbackGuard(t *testing.T) {
	h := newServer(t, browser.NewFake())
	r := req("GET", "/v1/showstone/state", useTok, "")
	r.RemoteAddr = "203.0.113.9:9999"
	if w := do(h, r); w.Code != http.StatusForbidden {
		t.Fatalf("non-loopback use plane => %d, want 403", w.Code)
	}
}

// TestStrictModeBlocksBenign: strict mode gates even an otherwise-benign action.
func TestStrictModeBlocksBenign(t *testing.T) {
	f := browser.NewFake() // seeded benign "Home" link
	h := newServer(t, f)
	do(h, req("POST", "/api/showstone/strict", ctrlTok, `{"on":true}`))
	done := blocks(h, `{"action":"click","index":0}`)
	id := waitForApproval(t, h)
	do(h, req("POST", "/api/showstone/approvals/"+id+"/decide", ctrlTok, `{"approve":true}`))
	if w := <-done; w.Code != 200 || len(f.Acts) != 1 {
		t.Fatalf("strict benign click approved => %d acts=%d", w.Code, len(f.Acts))
	}
}

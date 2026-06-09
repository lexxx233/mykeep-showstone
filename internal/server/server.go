// Package server exposes Showstone over HTTP as two planes (mirrors Vault):
//   - USE plane /v1/showstone/* — the agent drives the browser (navigate/snapshot/act/
//     screenshot); a mandatory X-Showstone-Token, loopback-only unless LAN is enabled.
//   - CONTROL plane /api/showstone/* — the human GUI: approvals, audit, trust/strict,
//     profile, session clear; ALWAYS loopback-only, control token or session cookie.
//
// Mechanism (driving the browser) is the Engine's; policy (auth, sensitivity, approval,
// audit) lives here.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mykeep.ai/showstone/internal/approver"
	"mykeep.ai/showstone/internal/audit"
	"mykeep.ai/showstone/internal/browser"
)

// Options configures a Server. Tokens are generated if empty.
type Options struct {
	EnableLAN       bool
	UseToken        string
	ControlToken    string
	ControlSession  string
	SessionCookie   string // default "sv_session"; the suite uses "mykeep_session"
	StrictDefault   bool
	TrustedHosts    []string
	ApprovalTimeout time.Duration
	Version         string
	Addr            string // for the guide text
}

// Server is the HTTP front for one browser Engine.
type Server struct {
	eng      browser.Engine
	approver *approver.Approver
	audit    *audit.Log
	opt      Options

	mu      sync.Mutex
	strict  bool
	trusted map[string]bool
}

// New builds a Server over a launched engine.
func New(eng browser.Engine, opt Options) *Server {
	if opt.UseToken == "" {
		opt.UseToken = randToken()
	}
	if opt.ControlToken == "" {
		opt.ControlToken = randToken()
	}
	if opt.SessionCookie == "" {
		opt.SessionCookie = "sv_session"
	}
	if opt.Addr == "" {
		opt.Addr = "127.0.0.1:8771"
	}
	trusted := map[string]bool{}
	for _, h := range opt.TrustedHosts {
		trusted[h] = true
	}
	return &Server{
		eng: eng, approver: approver.New(opt.ApprovalTimeout), audit: audit.New(),
		opt: opt, strict: opt.StrictDefault, trusted: trusted,
	}
}

func (s *Server) UseToken() string     { return s.opt.UseToken }
func (s *Server) ControlToken() string { return s.opt.ControlToken }

// Audit exposes the log so the lifecycle can seal/load it with the profile.
func (s *Server) Audit() *audit.Log { return s.audit }

// StateBlob is the durable session state sealed alongside the profile.
type StateBlob struct {
	Audit   []audit.Entry `json:"audit"`
	Trusted []string      `json:"trusted"`
	Strict  bool          `json:"strict"`
}

// ExportState captures the audit log + policy for sealing on Lock.
func (s *Server) ExportState() StateBlob {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := make([]string, 0, len(s.trusted))
	for h := range s.trusted {
		t = append(t, h)
	}
	return StateBlob{Audit: s.audit.Entries(), Trusted: t, Strict: s.strict}
}

// LoadState restores audit + policy on Unlock.
func (s *Server) LoadState(b StateBlob) {
	s.audit.Load(b.Audit)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.strict = b.Strict
	for _, h := range b.Trusted {
		s.trusted[h] = true
	}
}

// Handler returns the full handler for standalone serve (root/health + both planes).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.root)
	mux.HandleFunc("GET /healthz", s.health)
	s.Mount(mux)
	return mux
}

// Mount attaches the two planes onto a shared mux (the suite's).
func (s *Server) Mount(mux *http.ServeMux) {
	use := http.NewServeMux()
	use.HandleFunc("GET /v1/showstone/guide", s.guide)
	use.HandleFunc("GET /v1/showstone/state", s.state)
	use.HandleFunc("GET /v1/showstone/snapshot", s.snapshot)
	use.HandleFunc("GET /v1/showstone/screenshot", s.screenshot)
	use.HandleFunc("POST /v1/showstone/navigate", s.navigate)
	use.HandleFunc("POST /v1/showstone/act", s.act)
	use.HandleFunc("POST /v1/showstone/click", s.click)
	use.HandleFunc("POST /v1/showstone/type", s.typeAct)
	var useH http.Handler = s.requireToken(use)
	if !s.opt.EnableLAN {
		useH = loopbackOnly(useH)
	}
	mux.Handle("/v1/showstone/", useH)

	ctrl := http.NewServeMux()
	ctrl.HandleFunc("GET /api/showstone/state", s.ctrlState)
	ctrl.HandleFunc("GET /api/showstone/approvals", s.approvals)
	ctrl.HandleFunc("POST /api/showstone/approvals/{id}/decide", s.decide)
	ctrl.HandleFunc("GET /api/showstone/audit", s.auditList)
	ctrl.HandleFunc("GET /api/showstone/audit/verify", s.auditVerify)
	ctrl.HandleFunc("GET /api/showstone/profile", s.profile)
	ctrl.HandleFunc("POST /api/showstone/trust", s.trust)
	ctrl.HandleFunc("POST /api/showstone/strict", s.strictSet)
	ctrl.HandleFunc("POST /api/showstone/session/clear", s.sessionClear)
	mux.Handle("/api/showstone/", loopbackOnly(s.controlAuth(ctrl)))
}

// --- root / health (no auth) ---

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "service": "showstone",
		"planes": []string{"/v1/showstone (agent)", "/api/showstone (control, loopback-only)"}, "lan": s.opt.EnableLAN})
}

func (s *Server) root(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset=utf-8><title>Showstone</title>` +
		`<body style="font-family:system-ui;max-width:40rem;margin:4rem auto"><h1>🔮 Showstone</h1>` +
		`<p>A local browser driven over REST. <code>GET /v1/showstone/guide</code> for the protocol. ` +
		`The control plane (<code>/api/showstone</code>) is the human's, loopback-only.</p></body>`))
}

// --- USE plane ---

func (s *Server) guide(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(GuideText(s.opt.Addr)))
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	st, err := s.eng.State(r.Context())
	if err != nil {
		writeErr(w, 409, "no_active_page")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "url": st.URL, "title": st.Title,
		"loading": st.Loading, "can_go_back": st.CanGoBack, "snapshot_id": st.SnapshotID})
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	snap, err := s.eng.Snapshot(r.Context(), page)
	if err != nil {
		writeErr(w, statusFor(err), errCode(err))
		return
	}
	writeJSON(w, 200, envFromSnap(snap))
}

func (s *Server) screenshot(w http.ResponseWriter, r *http.Request) {
	full := r.URL.Query().Get("full") == "true"
	shot, err := s.eng.Screenshot(r.Context(), full)
	if err != nil {
		writeErr(w, 500, "screenshot_failed")
		return
	}
	s.logAction(r, "screenshot", "", "", "", "auto")
	if r.URL.Query().Get("format") == "png" {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(shot.PNG)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "format": "png", "full": shot.Full,
		"width": shot.Width, "height": shot.Height, "image_b64": base64.StdEncoding.EncodeToString(shot.PNG)})
}

func (s *Server) navigate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL        string `json:"url"`
		Screenshot bool   `json:"screenshot"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.URL == "" {
		writeErr(w, 400, "bad_request")
		return
	}
	if !allowedNavURL(body.URL) {
		s.logAction(r, "navigate", "", body.URL, "", "blocked_scheme")
		writeErr(w, 400, "scheme_not_allowed")
		return
	}
	host := hostOf(body.URL)
	// plain navigation is auto unless strict mode is on.
	dec, allowed := s.gate(r.Context(), approver.Request{Action: "navigate", Host: host, URL: body.URL}, false, false)
	if !allowed {
		s.logAction(r, "navigate", host, body.URL, "", dec)
		writeJSON(w, 403, errEnv("approval_denied", dec, host, "", "navigate"))
		return
	}
	snap, err := s.eng.Navigate(r.Context(), body.URL)
	if err != nil {
		s.logAction(r, "navigate", host, body.URL, "", "error")
		writeErr(w, statusFor(err), errCode(err))
		return
	}
	s.logAction(r, "navigate", hostOf(snap.URL), snap.URL, "", dec)
	env := envFromSnap(snap)
	if body.Screenshot {
		s.attachShot(r, &env)
	}
	writeJSON(w, 200, env)
}

func (s *Server) act(w http.ResponseWriter, r *http.Request) {
	var dto actDTO
	if !decode(w, r, &dto) {
		return
	}
	if dto.Action == "" {
		writeErr(w, 400, "bad_request")
		return
	}
	req := dto.toReq()
	if req.Action == "navigate" && !allowedNavURL(req.URL) {
		s.logAction(r, "navigate", "", req.URL, "", "blocked_scheme")
		writeErr(w, 400, "scheme_not_allowed")
		return
	}

	// classify (needs the element for click/type/select targeted by index)
	var el browser.Element
	if req.Selector == "" {
		if e, ok := s.eng.Element(req.Index); ok {
			el = e
		}
	}
	st, _ := s.eng.State(r.Context())
	hard, soft, cr := s.classify(req, el, st.URL)

	// Fail closed on the selector escape hatch: the classifier never resolved the
	// element by selector, so a selector-targeted mutating action is unclassifiable —
	// route it through a hard gate (never auto, even on a trusted host) rather than
	// letting an empty element classify as benign.
	if req.Selector != "" && isMutatingAction(req.Action) {
		hard, soft = true, false
		cr = approver.Request{Action: req.Action, Host: hostOf(st.URL), URL: st.URL, Label: "selector:" + req.Selector}
	}

	// Ensure act-driven navigation and (under strict mode) every action go through the
	// gate even when classify deemed them benign — otherwise /act bypasses the dedicated
	// /navigate gate and the strict-mode kill switch.
	s.mu.Lock()
	strict := s.strict
	s.mu.Unlock()
	if cr.Action == "" {
		if req.Action == "navigate" {
			cr = approver.Request{Action: "navigate", Host: hostOf(req.URL), URL: req.URL}
		} else if strict {
			cr = approver.Request{Action: req.Action, Host: hostOf(st.URL), URL: st.URL, Label: el.Name}
		}
	}

	decision := "auto"
	if cr.Action != "" {
		var allowed bool
		decision, allowed = s.gate(r.Context(), cr, hard, soft)
		if !allowed {
			s.logAction(r, cr.Action, cr.Host, cr.URL, el.Name, decision)
			writeJSON(w, 403, errEnv("approval_denied", decision, cr.Host, el.Name, cr.Action))
			return
		}
	}

	snap, err := s.eng.Act(r.Context(), req)
	if err != nil {
		// stale_snapshot / index_out_of_range carry a fresh snapshot so the agent recovers.
		s.logAction(r, req.Action, hostOf(st.URL), st.URL, el.Name, "error")
		env := envFromSnap(snap)
		env.OK = false
		env.Error = errCode(err)
		writeJSON(w, statusFor(err), env)
		return
	}
	s.logAction(r, req.Action, hostOf(snap.URL), snap.URL, el.Name, decision)
	env := envFromSnap(snap)
	if cr.Action != "" {
		env.Approval = &approvalInfo{Required: true, Decision: decision, Host: cr.Host, Label: el.Name, Action: cr.Action}
	}
	if dto.Screenshot {
		s.attachShot(r, &env)
	}
	writeJSON(w, 200, env)
}

// click / type are thin sugar over act.
func (s *Server) click(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Index      *int   `json:"index"`
		Selector   string `json:"selector"`
		SnapshotID string `json:"snapshot_id"`
		Screenshot bool   `json:"screenshot"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.actInline(w, r, actDTO{Action: "click", Index: b.Index, Selector: b.Selector,
		SnapshotID: b.SnapshotID, Screenshot: b.Screenshot})
}

func (s *Server) typeAct(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Index      *int   `json:"index"`
		Selector   string `json:"selector"`
		Text       string `json:"text"`
		Submit     bool   `json:"submit"`
		SnapshotID string `json:"snapshot_id"`
		Screenshot bool   `json:"screenshot"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.actInline(w, r, actDTO{Action: "type", Index: b.Index, Selector: b.Selector, Text: b.Text,
		Submit: b.Submit, SnapshotID: b.SnapshotID, Screenshot: b.Screenshot})
}

// --- CONTROL plane ---

func (s *Server) ctrlState(w http.ResponseWriter, r *http.Request) {
	st, _ := s.eng.State(r.Context())
	s.mu.Lock()
	strict := s.strict
	trusted := make([]string, 0, len(s.trusted))
	for h := range s.trusted {
		trusted = append(trusted, h)
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "url": st.URL, "host": hostOf(st.URL),
		"strict": strict, "trusted_hosts": trusted, "pending": len(s.approver.List())})
}

func (s *Server) approvals(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"pending": s.approver.List()})
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool `json:"approve"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	if !s.approver.Decide(r.PathValue("id"), b.Approve) {
		writeErr(w, 404, "no_such_pending")
		return
	}
	writeJSON(w, 200, map[string]any{"decided": true})
}

func (s *Server) auditList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"audit": s.audit.Entries()})
}

func (s *Server) auditVerify(w http.ResponseWriter, _ *http.Request) {
	if err := s.audit.Verify(); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.eng.Profile(r.Context())
	if err != nil {
		writeErr(w, 500, "profile_failed")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "hosts": hosts})
}

func (s *Server) trust(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Host    string `json:"host"`
		Trusted bool   `json:"trusted"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Host == "" {
		writeErr(w, 400, "bad_request")
		return
	}
	s.mu.Lock()
	if b.Trusted {
		s.trusted[b.Host] = true
	} else {
		delete(s.trusted, b.Host)
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "host": b.Host, "trusted": b.Trusted})
}

func (s *Server) strictSet(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.mu.Lock()
	s.strict = b.On
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "strict": b.On})
}

func (s *Server) sessionClear(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.ClearSession(r.Context()); err != nil {
		writeErr(w, 500, "clear_failed")
		return
	}
	s.logAction(r, "session_clear", "", "", "", "auto")
	writeJSON(w, 200, map[string]any{"ok": true, "cleared": true})
}

// --- middleware ---

func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/showstone/guide" { // instructions are fetchable pre-token
			next.ServeHTTP(w, r)
			return
		}
		if !tokenOK(r.Header.Get("X-Showstone-Token"), s.opt.UseToken) {
			writeErr(w, 401, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) controlAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenOK(r.Header.Get("X-Showstone-Token"), s.opt.ControlToken) {
			next.ServeHTTP(w, r)
			return
		}
		if s.opt.ControlSession != "" {
			if c, err := r.Cookie(s.opt.SessionCookie); err == nil && tokenOK(c.Value, s.opt.ControlSession) {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeErr(w, 401, "unauthorized")
	})
}

func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			writeErr(w, 403, "loopback_only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- helpers ---

func (s *Server) actInline(w http.ResponseWriter, r *http.Request, dto actDTO) {
	// re-encode into act by calling the same logic; reuse act by stuffing a fresh body.
	b, _ := json.Marshal(dto)
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(strings.NewReader(string(b)))
	s.act(w, r2)
}

func (s *Server) attachShot(r *http.Request, env *envelope) {
	if shot, err := s.eng.Screenshot(r.Context(), false); err == nil {
		env.Screenshot = base64.StdEncoding.EncodeToString(shot.PNG)
	}
}

func (s *Server) logAction(r *http.Request, action, host, url, label, decision string) {
	s.audit.Append(audit.Entry{Action: action, Host: host, URL: url, Label: label,
		Decision: decision, Source: r.RemoteAddr})
}

// gate classifies and (if needed) blocks until a human decides. Returns (decision,
// allowed). hard = always block (never auto, even on a trusted host); soft = block
// unless the host is trusted. strict mode blocks everything.
func (s *Server) gate(ctx context.Context, cr approver.Request, hard, soft bool) (string, bool) {
	s.mu.Lock()
	strict := s.strict
	trusted := s.trusted[cr.Host]
	s.mu.Unlock()
	if !hard && !soft && !strict {
		return "auto", true
	}
	if soft && !hard && !strict && trusted {
		return "auto", true
	}
	ok, err := s.approver.Confirm(ctx, cr)
	switch {
	case errors.Is(err, approver.ErrTimeout):
		return "timeout", false
	case err != nil:
		return "cancelled", false // client context cancelled
	case !ok:
		return "denied", false // explicit human deny
	}
	return "approved", true
}

func tokenOK(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, 400, "bad_request")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "error": msg})
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func isMutatingAction(a string) bool {
	switch a {
	case "click", "type", "select", "press":
		return true
	}
	return false
}

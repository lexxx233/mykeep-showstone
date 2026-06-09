package browser

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

//go:embed snapshot.js
var snapshotJS string

const (
	maxText  = 12000 // cap readable text (bytes)
	pageSize = 150   // elements per snapshot page
	opTimeout = 30 * time.Second
)

// RodOptions configures a launched engine.
type RodOptions struct {
	ChromeBin string
	LiveDir   string
	Headless  bool
	NoSandbox bool // required in containers/CI; weakens Chromium's own sandbox
}

// rodEngine drives one Chromium via go-rod. All Engine methods serialize on mu.
type rodEngine struct {
	mu       sync.Mutex
	launcher *launcher.Launcher
	browser  *rod.Browser
	page     *rod.Page

	snapshotID string
	lastEls    []Element
}

// OpenRod launches Chromium and connects. The caller owns Close().
func OpenRod(ctx context.Context, opt RodOptions) (Engine, error) {
	l := launcher.New().
		Bin(opt.ChromeBin).
		UserDataDir(opt.LiveDir).
		Headless(opt.Headless).
		Set("no-first-run").
		Set("no-default-browser-check").
		Leakless(true)
	if opt.NoSandbox {
		l = l.Set("no-sandbox")
	}
	u, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch chrome: %w", err)
	}
	br := rod.New().ControlURL(u)
	if err := br.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("connect chrome: %w", err)
	}
	// Structurally deny browser-initiated downloads — a heuristic on <a href> extensions
	// can't catch every download vector, so the agent must not be able to write files.
	_ = proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorDeny}.Call(br)
	pg, err := br.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		_ = br.Close()
		l.Kill()
		return nil, fmt.Errorf("open page: %w", err)
	}
	return &rodEngine{launcher: l, browser: br, page: pg}, nil
}

func (e *rodEngine) pg(ctx context.Context) *rod.Page {
	return e.page.Context(ctx).Timeout(opTimeout)
}

// closed reports whether the engine has been Closed (page nil). Callers hold e.mu.
func (e *rodEngine) closed() bool { return e.page == nil }

func (e *rodEngine) Navigate(ctx context.Context, url string) (Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed() {
		return Snapshot{}, ErrNoPage
	}
	p := e.pg(ctx)
	if err := p.Navigate(url); err != nil {
		return Snapshot{}, ErrNavBlocked
	}
	_ = p.WaitLoad()
	return e.snapshot(ctx, 1)
}

func (e *rodEngine) Snapshot(ctx context.Context, page int) (Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed() {
		return Snapshot{}, ErrNoPage
	}
	return e.snapshot(ctx, page)
}

func (e *rodEngine) Act(ctx context.Context, req ActRequest) (Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed() {
		return Snapshot{}, ErrNoPage
	}
	if req.SnapshotID != "" && req.SnapshotID != e.snapshotID {
		snap, _ := e.snapshot(ctx, 1)
		return snap, ErrStaleSnapshot
	}
	if err := e.do(ctx, req); err != nil {
		// index resolution / not found surfaces a fresh snapshot so the agent recovers
		if err == ErrIndexRange {
			snap, _ := e.snapshot(ctx, 1)
			return snap, ErrIndexRange
		}
		return Snapshot{}, err
	}
	_ = e.pg(ctx).WaitLoad()
	time.Sleep(150 * time.Millisecond) // let SPA DOM settle before re-reading
	return e.snapshot(ctx, 1)
}

// do performs the action mechanism (no policy).
func (e *rodEngine) do(ctx context.Context, req ActRequest) error {
	p := e.pg(ctx)
	switch req.Action {
	case "back":
		return p.NavigateBack()
	case "forward":
		return p.NavigateForward()
	case "reload":
		return p.Reload()
	case "navigate":
		if req.URL == "" {
			return ErrBadRequest
		}
		return p.Navigate(req.URL)
	case "wait":
		ms := req.MS
		if ms <= 0 {
			ms = 1500
		}
		if ms > 10000 {
			ms = 10000
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return nil
	case "scroll":
		return e.scroll(p, req)
	case "press":
		k, ok := keyMap[req.Key]
		if !ok {
			return ErrBadRequest
		}
		if el, err := e.resolve(p, req); err == nil && el != nil {
			_ = el.Focus()
		}
		return p.Keyboard.Type(k)
	case "click", "type", "select":
		el, err := e.resolve(p, req)
		if err != nil {
			return err
		}
		switch req.Action {
		case "click":
			return el.Click(proto.InputMouseButtonLeft, 1)
		case "type":
			if err := el.Input(req.Text); err != nil {
				return err
			}
			if req.Submit {
				return el.Type(input.Enter)
			}
			return nil
		case "select":
			return el.Select([]string{req.Value}, true, rod.SelectorTypeText)
		}
	}
	return ErrBadRequest
}

func (e *rodEngine) scroll(p *rod.Page, req ActRequest) error {
	switch req.Direction {
	case "top":
		_, err := p.Eval(`() => window.scrollTo(0,0)`)
		return err
	case "bottom":
		_, err := p.Eval(`() => window.scrollTo(0, document.body.scrollHeight)`)
		return err
	case "up":
		amt := req.Amount
		if amt <= 0 {
			amt = 600
		}
		return p.Mouse.Scroll(0, -float64(amt), 1)
	default: // down
		amt := req.Amount
		if amt <= 0 {
			amt = 600
		}
		return p.Mouse.Scroll(0, float64(amt), 1)
	}
}

// resolve finds the target element by selector (escape hatch) or by snapshot index
// (via the data-showstone-idx marker the snapshot JS set).
func (e *rodEngine) resolve(p *rod.Page, req ActRequest) (*rod.Element, error) {
	sel := req.Selector
	if sel == "" {
		sel = fmt.Sprintf(`[data-showstone-idx="%d"]`, req.Index)
	}
	has, el, err := p.Has(sel)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrIndexRange
	}
	return el, nil
}

func (e *rodEngine) Screenshot(ctx context.Context, full bool) (Screenshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed() {
		return Screenshot{}, ErrNoPage
	}
	png, err := e.pg(ctx).Screenshot(full, nil)
	if err != nil {
		return Screenshot{}, err
	}
	w, h := 0, 0
	if cfg, _, derr := image.DecodeConfig(bytes.NewReader(png)); derr == nil {
		w, h = cfg.Width, cfg.Height
	}
	return Screenshot{PNG: png, Width: w, Height: h, Full: full}, nil
}

func (e *rodEngine) State(ctx context.Context) (State, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed() {
		return State{}, ErrNoPage
	}
	p := e.pg(ctx)
	info, err := p.Info()
	if err != nil {
		return State{}, ErrNoPage
	}
	back := false
	if res, err := p.Eval(`() => window.history.length > 1`); err == nil {
		back = res.Value.Bool()
	}
	return State{URL: info.URL, Title: info.Title, CanGoBack: back, SnapshotID: e.snapshotID}, nil
}

func (e *rodEngine) Element(index int) (Element, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, el := range e.lastEls {
		if el.Index == index {
			return el, true
		}
	}
	return Element{}, false
}

func (e *rodEngine) Fill(ctx context.Context, index int, value string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed() {
		return ErrNoPage
	}
	p := e.pg(ctx)
	el, err := e.resolve(p, ActRequest{Index: index})
	if err != nil {
		return err
	}
	return el.Input(value)
}

func (e *rodEngine) ClearSession(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed() {
		return ErrNoPage
	}
	p := e.pg(ctx)
	_ = proto.NetworkClearBrowserCookies{}.Call(p)
	_, _ = p.Eval(`() => { try{localStorage.clear();sessionStorage.clear();}catch(e){} }`)
	return nil
}

func (e *rodEngine) Profile(ctx context.Context) ([]HostCookies, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed() {
		return nil, ErrNoPage
	}
	res, err := proto.NetworkGetAllCookies{}.Call(e.pg(ctx))
	if err != nil {
		return nil, err
	}
	byHost := map[string]*HostCookies{}
	for _, c := range res.Cookies {
		host := strings.TrimPrefix(c.Domain, ".")
		hc := byHost[host]
		if hc == nil {
			hc = &HostCookies{Host: host}
			byHost[host] = hc
		}
		hc.Cookies++
		switch strings.ToLower(c.Name) {
		case "session", "sessionid", "sess", "sid", "auth", "token", "login", "_session", "connect.sid":
			hc.LoggedIn = true
		}
	}
	out := make([]HostCookies, 0, len(byHost))
	for _, hc := range byHost {
		out = append(out, *hc)
	}
	return out, nil
}

func (e *rodEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser != nil {
		_ = e.browser.Close() // graceful CDP Browser.close → flush leveldb
	}
	if e.launcher != nil {
		e.launcher.Kill()
		e.launcher.Cleanup()
	}
	e.browser, e.launcher, e.page = nil, nil, nil
	return nil
}

// snapshot runs the extraction JS, re-indexes, paginates, and records the result.
func (e *rodEngine) snapshot(ctx context.Context, page int) (Snapshot, error) {
	if page < 1 {
		page = 1
	}
	res, err := e.pg(ctx).Eval(snapshotJS)
	if err != nil {
		return Snapshot{}, ErrNoPage
	}
	var raw struct {
		URL      string    `json:"url"`
		Title    string    `json:"title"`
		Text     string    `json:"text"`
		Elements []Element `json:"elements"`
	}
	if err := json.Unmarshal([]byte(res.Value.Str()), &raw); err != nil {
		return Snapshot{}, err
	}

	text, truncated := raw.Text, false
	if len(text) > maxText {
		text, truncated = text[:maxText], true
	}

	e.snapshotID = randID()
	e.lastEls = raw.Elements

	total := len(raw.Elements)
	pageCount := (total + pageSize - 1) / pageSize
	if pageCount == 0 {
		pageCount = 1
	}
	if page > pageCount {
		page = pageCount
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if end > total {
		end = total
	}
	var slice []Element
	if start < total {
		slice = raw.Elements[start:end]
	} else {
		slice = []Element{}
	}

	back := false
	if r, err := e.pg(ctx).Eval(`() => window.history.length > 1`); err == nil {
		back = r.Value.Bool()
	}

	return Snapshot{
		URL: raw.URL, Title: raw.Title, Text: text, TextTruncated: truncated,
		Elements: slice, ElementCount: total, Page: page, PageCount: pageCount,
		SnapshotID: e.snapshotID, CanGoBack: back,
	}, nil
}

var keyMap = map[string]input.Key{
	"Enter": input.Enter, "Tab": input.Tab, "Escape": input.Escape,
	"Backspace": input.Backspace, "Delete": input.Delete,
	"ArrowDown": input.ArrowDown, "ArrowUp": input.ArrowUp,
	"ArrowLeft": input.ArrowLeft, "ArrowRight": input.ArrowRight,
	"PageDown": input.PageDown, "PageUp": input.PageUp, "Home": input.Home, "End": input.End,
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// trimName keeps element names short for logs/UI.
func trimName(s string) string { return strings.TrimSpace(s) }

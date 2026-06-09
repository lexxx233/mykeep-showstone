// Package browser defines the engine abstraction Showstone drives: the page-snapshot
// model an LLM reads (readable text + indexed interactive elements), the action
// primitive it issues, and the Engine interface. The concrete engine (rod.go) drives a
// real Chromium over CDP via go-rod; tests use a fake. Keeping policy (auth, approval,
// audit) in the server and mechanism here makes the whole surface testable without a
// browser.
package browser

import (
	"context"
	"errors"
)

// Engine error sentinels map 1:1 to the API's fixed error codes.
var (
	ErrStaleSnapshot = errors.New("stale_snapshot")
	ErrIndexRange    = errors.New("index_out_of_range")
	ErrNoPage        = errors.New("no_active_page")
	ErrNavBlocked    = errors.New("nav_blocked")
	ErrBadRequest    = errors.New("bad_request")
)

// Element is one interactive node the agent can target by Index.
type Element struct {
	Index       int    `json:"index"`
	Role        string `json:"role"`
	Tag         string `json:"tag"`
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Href        string `json:"href,omitempty"`
	Type        string `json:"input_type,omitempty"` // input type= (password, email, …) — drives sensitivity
	Checked     *bool  `json:"checked,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	Bbox        []int  `json:"bbox,omitempty"`
}

// Snapshot is what the agent reads: readable text + the indexed element list. Indices
// are per-snapshot and re-numbered every time; SnapshotID is the freshness token.
type Snapshot struct {
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	Loading       bool      `json:"loading"`
	Text          string    `json:"text"`
	TextTruncated bool      `json:"text_truncated,omitempty"`
	Elements      []Element `json:"elements"`
	ElementCount  int       `json:"element_count"`
	Page          int       `json:"page"`
	PageCount     int       `json:"page_count"`
	SnapshotID    string    `json:"snapshot_id"`
	CanGoBack     bool      `json:"can_go_back"`
	CanGoForward  bool      `json:"can_go_forward"`
}

// HostCookies summarizes stored cookies for one host (counts only, never values).
type HostCookies struct {
	Host     string `json:"host"`
	Cookies  int    `json:"cookies"`
	LoggedIn bool   `json:"logged_in_guess"`
}

// State is the cheap status (no DOM walk).
type State struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	Loading      bool   `json:"loading"`
	CanGoBack    bool   `json:"can_go_back"`
	CanGoForward bool   `json:"can_go_forward"`
	SnapshotID   string `json:"snapshot_id"`
}

// ActRequest is a single browser action. Targeting is by Index (preferred) unless
// Selector is set. The engine performs the mechanism only — sensitivity/approval is the
// server's job.
type ActRequest struct {
	Action     string // click|type|select|press|scroll|back|forward|reload|wait|navigate
	Index      int
	Selector   string
	Text       string
	Value      string
	Key        string
	Direction  string // up|down|top|bottom
	Amount     int
	Submit     bool
	URL        string
	MS         int
	SnapshotID string
}

// Screenshot is a PNG plus its pixel dimensions.
type Screenshot struct {
	PNG    []byte
	Width  int
	Height int
	Full   bool
}

// Engine drives one browser. All methods are safe for sequential use from HTTP
// handlers; the concrete engine serializes access internally.
type Engine interface {
	Navigate(ctx context.Context, url string) (Snapshot, error)
	Snapshot(ctx context.Context, page int) (Snapshot, error)
	Act(ctx context.Context, req ActRequest) (Snapshot, error)
	Screenshot(ctx context.Context, full bool) (Screenshot, error)
	State(ctx context.Context) (State, error)
	// Element returns metadata for an index in the CURRENT snapshot (server uses it to
	// classify sensitivity before acting). ok=false if the index is unknown/stale.
	Element(index int) (Element, bool)
	// Fill types a value into an element without it transiting an API body — the seam
	// for Vault-by-reference login (v1.1). The value is never logged.
	Fill(ctx context.Context, index int, value string) error
	// ClearSession wipes cookies/storage (control-plane "session/clear").
	ClearSession(ctx context.Context) error
	// Profile returns a per-host cookie overview for the control plane (counts only).
	Profile(ctx context.Context) ([]HostCookies, error)
	Close() error
}

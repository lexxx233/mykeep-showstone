// Package component is the public integration surface of the mykeep Showstone
// component — a portable browser an agent drives over REST. Standalone, Showstone ships
// as cmd/showstone; to compose into the mykeep suite (one binary, one unlock) the
// aggregator (a separate module) imports this thin bridge, which lives inside the
// mykeep.ai/showstone module so it may reach internal/. Only stdlib + []byte cross the
// boundary; the contract is duck-typed (matches the suite's Component interface).
package component

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"mykeep.ai/showstone/internal/browser"
	"mykeep.ai/showstone/internal/paths"
	"mykeep.ai/showstone/internal/runtime"
)

// ID is Showstone's stable identifier within the suite.
const ID = "showstone"

// ChromeVersion is the pinned Chrome-for-Testing build Showstone downloads/launches.
const ChromeVersion = browser.PinnedChromeVersion

// Options is everything the host (standalone cmd or the suite aggregator) supplies.
type Options struct {
	DataDir         string
	Portable        bool
	Version         string
	Headless        bool
	NoSandbox       bool
	EnableLAN       bool
	StrictDefault   bool
	UseToken        string
	ControlToken    string
	ControlSession  string
	SessionCookie   string
	ApprovalTimeout time.Duration
	Addr            string
	OnDownload      func(pct int) // first-launch Chrome download progress (GUI message)
}

// Component is the Showstone capability. Construct LOCKED with New; Unlock launches the
// browser with an injected key; Mount attaches the two planes; Lock seals + tears down.
type Component struct {
	opts   Options
	layout paths.Layout

	mu sync.Mutex
	rt *runtime.Runtime
}

// New builds a locked component bound to a data dir. Cheap: no launch, no crypto, no I/O.
func New(opts Options) (*Component, error) {
	return &Component{opts: opts, layout: paths.Layout{DataDir: opts.DataDir, Portable: opts.Portable}}, nil
}

// ID returns the stable component identifier.
func (c *Component) ID() string { return ID }

// FirstLaunch reports whether Showstone has never sealed a profile in this data dir.
func (c *Component) FirstLaunch() bool {
	_, err := os.Stat(c.layout.ProfileEncPath())
	return os.IsNotExist(err)
}

// Unlock launches the browser with an externally supplied 32-byte DEK: it resolves the
// engine, unseals + restores the profile onto the stick, and serves the REST planes. It
// does NOT run its own argon2id. The dek is adopted and wiped on Lock.
func (c *Component) Unlock(ctx context.Context, dek []byte) error {
	rt, err := runtime.Open(ctx, c.layout, dek, runtime.Options{
		Version: c.opts.Version, Headless: c.opts.Headless, NoSandbox: c.opts.NoSandbox,
		EnableLAN: c.opts.EnableLAN, StrictDefault: c.opts.StrictDefault,
		UseToken: c.opts.UseToken, ControlToken: c.opts.ControlToken,
		ControlSession: c.opts.ControlSession, SessionCookie: c.opts.SessionCookie,
		ApprovalTimeout: c.opts.ApprovalTimeout, Addr: c.opts.Addr, OnDownload: c.opts.OnDownload,
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.rt = rt
	c.mu.Unlock()
	return nil
}

// Mount attaches Showstone's USE plane (/v1/showstone/) + CONTROL plane
// (/api/showstone/) onto the shared suite mux.
func (c *Component) Mount(mux *http.ServeMux) {
	c.mu.Lock()
	rt := c.rt
	c.mu.Unlock()
	if rt != nil {
		rt.Server().Mount(mux)
	}
}

// UseToken / ControlToken expose the minted tokens (after Unlock) for the snippet/GUI.
func (c *Component) UseToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rt == nil {
		return ""
	}
	return c.rt.Server().UseToken()
}

func (c *Component) ControlToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rt == nil {
		return ""
	}
	return c.rt.Server().ControlToken()
}

// Lock kills the browser, reseals the profile, and zeroizes the key. Idempotent.
func (c *Component) Lock() error {
	c.mu.Lock()
	rt := c.rt
	c.rt = nil
	c.mu.Unlock()
	if rt == nil {
		return nil
	}
	return rt.Lock()
}

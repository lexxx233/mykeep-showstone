// Package approver realises Showstone's "in-RAM pending + GUI decide, synchronous
// block" approval gate (mirrors Vault). A sensitive browser action blocks here until a
// human decides on the control plane, or the timeout fires (fail-closed → deny). The
// agent cannot reach Decide — it lives on the loopback-only control plane.
package approver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Request describes the action awaiting a human decision (shown on the GUI card).
type Request struct {
	Action string `json:"action"` // click|type|navigate|submit|download
	Host   string `json:"host"`
	URL    string `json:"url"`
	Label  string `json:"label"` // element accessible name
	Field  string `json:"field,omitempty"`
}

// Pending is the GUI-visible view of a blocking request.
type Pending struct {
	ID      string    `json:"id"`
	Action  string    `json:"action"`
	Host    string    `json:"host"`
	URL     string    `json:"url"`
	Label   string    `json:"label"`
	Field   string    `json:"field,omitempty"`
	Created time.Time `json:"created"`
}

type pendingReq struct {
	Pending
	ch chan bool
}

// Approver holds the set of currently-blocking requests.
type Approver struct {
	mu      sync.Mutex
	pending map[string]*pendingReq
	timeout time.Duration
}

// New builds an approver; timeout<=0 defaults to 2 minutes.
func New(timeout time.Duration) *Approver {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &Approver{pending: map[string]*pendingReq{}, timeout: timeout}
}

// Confirm blocks until a human decides or the timeout fires (fail-closed → false).
func (a *Approver) Confirm(ctx context.Context, r Request) (bool, error) {
	pr := &pendingReq{
		Pending: Pending{ID: randID(), Action: r.Action, Host: r.Host, URL: r.URL,
			Label: r.Label, Field: r.Field, Created: time.Now().UTC()},
		ch: make(chan bool, 1),
	}
	a.mu.Lock()
	a.pending[pr.ID] = pr
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, pr.ID)
		a.mu.Unlock()
	}()

	timer := time.NewTimer(a.timeout)
	defer timer.Stop()
	select {
	case ok := <-pr.ch:
		return ok, nil
	case <-timer.C:
		return false, nil // fail-closed
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// List returns the pending requests (polled by the GUI).
func (a *Approver) List() []Pending {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Pending, 0, len(a.pending))
	for _, pr := range a.pending {
		out = append(out, pr.Pending)
	}
	return out
}

// Decide resolves a pending request; returns false if the id is unknown.
func (a *Approver) Decide(id string, approve bool) bool {
	a.mu.Lock()
	pr, ok := a.pending[id]
	a.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case pr.ch <- approve:
		return true
	default:
		return false
	}
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

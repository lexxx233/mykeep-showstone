// Package audit is Showstone's append-only, hash-chained action log (mirrors Vault):
// every navigate/click/submit/screenshot/download is recorded with the prior entry's
// hash folded in, so tampering is detectable. Typed text is NEVER logged (only the
// element/field label) — a password could be in it.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"
)

// Entry is one recorded action.
type Entry struct {
	Time     time.Time `json:"t"`
	Action   string    `json:"action"`   // navigate|click|type|submit|screenshot|download|nav
	Host     string    `json:"host"`
	URL      string    `json:"url"`
	Label    string    `json:"label"`    // element/field name — never the typed value
	Decision string    `json:"decision"` // auto|approved|denied|timeout|error
	Source   string    `json:"source"`   // remote addr
	PrevHash string    `json:"prev_hash"`
	Hash     string    `json:"hash"`
}

// Log is the in-RAM hash chain (persisted with the sealed profile by the caller).
type Log struct {
	mu      sync.Mutex
	entries []Entry
}

func New() *Log { return &Log{} }

// Load seeds the log from previously-sealed entries (preserves the chain on reopen).
func (l *Log) Load(es []Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append([]Entry(nil), es...)
}

// Append hash-chains and records an entry.
func (l *Log) Append(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	prev := ""
	if n := len(l.entries); n > 0 {
		prev = l.entries[n-1].Hash
	}
	e.PrevHash = prev
	e.Hash = hashEntry(prev, e)
	l.entries = append(l.entries, e)
}

// Entries returns a copy of the log.
func (l *Log) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Entry(nil), l.entries...)
}

// Verify walks the chain and reports the first break.
func (l *Log) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := ""
	for i, e := range l.entries {
		if e.PrevHash != prev {
			return fmt.Errorf("audit: prev_hash break at row %d", i)
		}
		if e.Hash != hashEntry(prev, e) {
			return fmt.Errorf("audit: row_hash mismatch at row %d", i)
		}
		prev = e.Hash
	}
	return nil
}

func hashEntry(prev string, e Entry) string {
	h := sha256.New()
	io.WriteString(h, prev)
	fmt.Fprintf(h, "\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		e.Time.UTC().Format(time.RFC3339Nano), e.Action, e.Host, e.URL, e.Label, e.Decision)
	return hex.EncodeToString(h.Sum(nil))
}

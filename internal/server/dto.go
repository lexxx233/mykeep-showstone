package server

import (
	"errors"
	"net/http"

	"mykeep.ai/showstone/internal/browser"
)

// envelope is the consistent shape every USE-plane endpoint returns.
type envelope struct {
	OK            bool              `json:"ok"`
	URL           string            `json:"url,omitempty"`
	Title         string            `json:"title,omitempty"`
	Loading       bool              `json:"loading"`
	Text          string            `json:"text,omitempty"`
	TextTruncated bool              `json:"text_truncated,omitempty"`
	Elements      []browser.Element `json:"elements"`
	ElementCount  int               `json:"element_count"`
	Page          int               `json:"page,omitempty"`
	PageCount     int               `json:"page_count,omitempty"`
	Screenshot    string            `json:"screenshot,omitempty"`
	SnapshotID    string            `json:"snapshot_id,omitempty"`
	CanGoBack     bool              `json:"can_go_back"`
	Approval      *approvalInfo     `json:"approval,omitempty"`
	Error         string            `json:"error,omitempty"`
}

type approvalInfo struct {
	Required bool   `json:"required"`
	Decision string `json:"decision,omitempty"` // auto|approved|denied|timeout
	Host     string `json:"host,omitempty"`
	Label    string `json:"label,omitempty"`
	Action   string `json:"action,omitempty"`
}

func envFromSnap(s browser.Snapshot) envelope {
	els := s.Elements
	if els == nil {
		els = []browser.Element{}
	} else {
		// Defense in depth: never return a secret field's VALUE to the agent (the
		// engine's snapshot.js also redacts, but a future engine change must not leak).
		out := make([]browser.Element, len(els))
		copy(out, els)
		for i := range out {
			if isSecretField(out[i]) && out[i].Value != "" {
				out[i].Value = ""
			}
		}
		els = out
	}
	return envelope{
		OK: true, URL: s.URL, Title: s.Title, Loading: s.Loading,
		Text: s.Text, TextTruncated: s.TextTruncated, Elements: els,
		ElementCount: s.ElementCount, Page: s.Page, PageCount: s.PageCount,
		SnapshotID: s.SnapshotID, CanGoBack: s.CanGoBack,
	}
}

func errEnv(code, decision, host, label, action string) envelope {
	return envelope{OK: false, Error: code, Elements: []browser.Element{},
		Approval: &approvalInfo{Required: true, Decision: decision, Host: host, Label: label, Action: action}}
}

// actDTO is the wire form of an /act request; Index is a pointer so 0 is distinguishable
// from "absent".
type actDTO struct {
	Action     string `json:"action"`
	Index      *int   `json:"index"`
	Selector   string `json:"selector"`
	Text       string `json:"text"`
	Value      string `json:"value"`
	Key        string `json:"key"`
	Direction  string `json:"direction"`
	Amount     int    `json:"amount"`
	Submit     bool   `json:"submit"`
	URL        string `json:"url"`
	MS         int    `json:"ms"`
	SnapshotID string `json:"snapshot_id"`
	Screenshot bool   `json:"screenshot"`
}

func (d actDTO) toReq() browser.ActRequest {
	idx := 0
	if d.Index != nil {
		idx = *d.Index
	}
	return browser.ActRequest{
		Action: d.Action, Index: idx, Selector: d.Selector, Text: d.Text, Value: d.Value,
		Key: d.Key, Direction: d.Direction, Amount: d.Amount, Submit: d.Submit,
		URL: d.URL, MS: d.MS, SnapshotID: d.SnapshotID,
	}
}

// errCode maps an engine error to the API's fixed error string.
func errCode(err error) string {
	switch {
	case errors.Is(err, browser.ErrStaleSnapshot):
		return "stale_snapshot"
	case errors.Is(err, browser.ErrIndexRange):
		return "index_out_of_range"
	case errors.Is(err, browser.ErrNoPage):
		return "no_active_page"
	case errors.Is(err, browser.ErrNavBlocked):
		return "nav_blocked"
	case errors.Is(err, browser.ErrBadRequest):
		return "bad_request"
	default:
		return "engine_error"
	}
}

// statusFor maps an engine error to an HTTP status. Recoverable errors that carry a
// fresh snapshot return 200 so the agent reads it and retries.
func statusFor(err error) int {
	switch {
	case errors.Is(err, browser.ErrStaleSnapshot), errors.Is(err, browser.ErrIndexRange):
		return http.StatusOK
	case errors.Is(err, browser.ErrBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, browser.ErrNoPage):
		return http.StatusConflict
	case errors.Is(err, browser.ErrNavBlocked):
		return http.StatusBadGateway
	default:
		return http.StatusBadGateway
	}
}

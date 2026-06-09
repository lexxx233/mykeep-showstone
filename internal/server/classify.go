package server

import (
	"net/url"
	"strings"

	"mykeep.ai/showstone/internal/approver"
	"mykeep.ai/showstone/internal/browser"
)

// allowedNavURL restricts navigation to safe web schemes — blocks file://, chrome://,
// chrome-devtools://, view-source://, data: (as a top-level nav), etc., which a
// prompt-injected agent could otherwise use to read local files or browser internals
// and exfiltrate them via the snapshot/screenshot response.
func allowedNavURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "about":
		return true
	default:
		return false
	}
}

// sensitiveLexicon: a click whose element name/role contains one of these blocks for
// approval (unless the host is trusted). Intentionally broad — false positives just ask.
var sensitiveLexicon = []string{
	"buy", "pay", "purchase", "checkout", "place order", "order now", "subscribe",
	"confirm", "delete", "remove", "send", "post", "publish", "tweet", "transfer",
	"withdraw", "sign", "agree", "accept", "authorize", "donate", "book now", "submit",
	"unsubscribe", "cancel subscription", "make payment",
}

// secretFieldHints: typing into a field matching these (or input type=password) always
// blocks and never auto-approves, even on a trusted host.
var secretFieldHints = []string{"password", "card", "cvv", "cvc", "credit", "iban", "ssn", "social security", "account number"}

// downloadExts: a link click ending in one of these is treated as a download (blocks).
var downloadExts = []string{".pdf", ".zip", ".dmg", ".exe", ".pkg", ".msi", ".tar", ".gz", ".csv", ".xlsx", ".docx", ".apk", ".iso"}

// classify decides whether an action is sensitive. hard = always block (never auto, even
// on a trusted host); soft = block unless the host is trusted. cr is empty (Action "")
// when the action is benign and needs no gate.
func (s *Server) classify(req browser.ActRequest, el browser.Element, curURL string) (hard, soft bool, cr approver.Request) {
	host := hostOf(curURL)
	base := approver.Request{Host: host, URL: curURL, Label: el.Name}
	switch req.Action {
	case "type":
		base.Action = "type"
		base.Field = el.Name
		if isSecretField(el) {
			return true, false, base // hard
		}
		if req.Submit {
			base.Action = "submit"
			return false, true, base // soft
		}
		return false, false, approver.Request{} // benign typing into a normal field
	case "click":
		if isDownloadLink(el) {
			base.Action = "download"
			return true, false, base
		}
		if matchesLexicon(el.Name) || matchesLexicon(el.Role) {
			base.Action = "click"
			return false, true, base // soft
		}
		return false, false, approver.Request{}
	case "press":
		// Enter/Space activate the focused control (submit a form, click a button) —
		// the same effect as a click, so gate it the same way.
		if isActivatorKey(req.Key) {
			base.Action = "submit"
			if matchesLexicon(el.Name) || matchesLexicon(el.Role) {
				base.Action = "click"
			}
			if isSecretField(el) {
				return true, false, base // hard: Enter in a password/payment field
			}
			return false, true, base // soft
		}
		return false, false, approver.Request{} // Tab/arrows/Esc/etc. are benign
	case "select":
		// Choosing an option can arm a sensitive flow (plan tier, saved address…).
		if matchesLexicon(el.Name) || matchesLexicon(el.Role) || matchesLexicon(req.Value) {
			base.Action = "select"
			return false, true, base // soft
		}
		return false, false, approver.Request{}
	default:
		// scroll/back/forward/reload/wait are benign; navigate's gate (and strict mode)
		// is applied in the act/navigate handler, not here.
		return false, false, approver.Request{}
	}
}

func isActivatorKey(k string) bool {
	switch k {
	case "Enter", "Return", "Space", " ":
		return true
	}
	return false
}

func isSecretField(el browser.Element) bool {
	if strings.EqualFold(el.Type, "password") {
		return true
	}
	hay := strings.ToLower(el.Name + " " + el.Placeholder)
	for _, h := range secretFieldHints {
		if strings.Contains(hay, h) {
			return true
		}
	}
	return false
}

func isDownloadLink(el browser.Element) bool {
	if el.Tag != "a" || el.Href == "" {
		return false
	}
	href := strings.ToLower(el.Href)
	// strip query/fragment
	if i := strings.IndexAny(href, "?#"); i >= 0 {
		href = href[:i]
	}
	for _, ext := range downloadExts {
		if strings.HasSuffix(href, ext) {
			return true
		}
	}
	return false
}

func matchesLexicon(s string) bool {
	low := strings.ToLower(s)
	for _, w := range sensitiveLexicon {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

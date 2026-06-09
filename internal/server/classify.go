package server

import (
	"strings"

	"mykeep.ai/showstone/internal/approver"
	"mykeep.ai/showstone/internal/browser"
)

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
	default:
		// select/press/scroll/back/forward/reload/wait/navigate are benign here;
		// navigate's cross-host gate (and strict mode) is handled at the call site.
		return false, false, approver.Request{}
	}
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

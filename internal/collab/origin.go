package collab

import (
	"net/http"
	"net/url"
	"strings"
)

// newOriginPolicy returns the cross-site check for WebSocket handshakes. A
// handshake is a GET, which CSRF protection deliberately passes, yet it opens
// a cookie-authenticated channel that can write; without this check any site
// could open one on a signed-in user's behalf (cross-site WebSocket
// hijacking). It mirrors the server's CSRF policy: Fetch Metadata first, then
// the configured origins, then the request's own host. Browsers always send
// Origin on a handshake; requests without one are not from a browser.
func newOriginPolicy(allowedOrigins []string, disabled bool) func(*http.Request) bool {
	origins := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origins[strings.ToLower(origin)] = true
	}
	return func(r *http.Request) bool {
		if disabled {
			return true
		}
		switch r.Header.Get("Sec-Fetch-Site") {
		case "same-origin":
			return true
		case "", "none":
		default:
			return false
		}
		origin := r.Header.Get("Origin")
		if origin == "" || origins[strings.ToLower(origin)] {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host)
	}
}

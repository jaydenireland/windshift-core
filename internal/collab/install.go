package collab

import (
	"log/slog"
	"net/http"
	"os"
	"strings"

	"windshift/internal/database"
	"windshift/internal/services"
)

// DisableEnv turns co-editing off; editors then fall back to autosave.
const DisableEnv = "WINDSHIFT_DISABLE_COLLAB"

// Deps is everything co-editing needs from the server. Install takes them in
// one call so the server's only co-editing change is that call.
type Deps struct {
	DB database.Database
	// Route registers a handler on the /api route group (its HandleH), so
	// requests get the group's CORS, session and request-limit middleware.
	Route func(pattern string, handler http.Handler)
	// RequireAuth rejects unauthenticated requests.
	RequireAuth func(http.Handler) http.Handler
	// StreamLimiter caps long-lived connections per user; the SSE hub's cap
	// is shared so live updates and co-editing count together. May be nil.
	StreamLimiter StreamLimiter
	// AllowedOrigins and DisableCSRF are the server's CSRF settings, applied
	// to WebSocket handshakes.
	AllowedOrigins []string
	DisableCSRF    bool

	Pages           *services.PageService
	PagePermissions *services.PagePermissionService
	// Items co-edits item descriptions; nil leaves them on explicit saves.
	Items *services.ItemUpdateApplicationService
}

// Install wires co-editing into a server: it creates the tables, starts the
// hub and registers its routes. It returns nil when co-editing is disabled or
// cannot start; the server keeps working and editors use autosave.
//
// Routes (under /api):
//
//	GET /collab/status                 document kinds that support co-editing
//	GET /collab/{kind}/{id}/events     the co-editing WebSocket
//
// The socket path ends in /events so the per-user request limiter treats it
// as a long-lived stream, like the SSE endpoints.
func Install(d Deps) *Hub {
	if envEnabled(DisableEnv) {
		slog.Info("live co-editing disabled", "env", DisableEnv)
		return nil
	}
	if err := ensureSchema(d.DB); err != nil {
		slog.Error("live co-editing unavailable", "error", err)
		return nil
	}
	hub := NewHub(d.DB, d.StreamLimiter)
	hub.Register(NewPageBackend(d.DB, d.Pages, d.PagePermissions))
	if d.Items != nil {
		hub.Register(NewItemBackend(d.DB, d.Items))
	}
	hub.StartSettling()
	hub.SetOriginPolicy(newOriginPolicy(d.AllowedOrigins, d.DisableCSRF))
	d.Route("GET /collab/{kind}/{id}/events", d.RequireAuth(http.HandlerFunc(hub.ServeDocument)))
	d.Route("GET /collab/status", d.RequireAuth(http.HandlerFunc(hub.ServeStatus)))
	return hub
}

func envEnabled(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

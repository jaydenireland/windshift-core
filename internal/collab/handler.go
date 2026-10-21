package collab

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"windshift/internal/models"
	"windshift/internal/utils"
)

const (
	// readLimit bounds one client frame. A seed or compacted state for the
	// largest page (1 MiB of markdown) fits comfortably once base64-encoded.
	readLimit = 16 << 20
	// writeTimeout bounds one frame write to a client.
	writeTimeout = 15 * time.Second
	// pingInterval keeps idle connections alive through proxies.
	pingInterval = 25 * time.Second
	// reauthorizeInterval re-checks access for long-lived connections, so a
	// revoked permission ends the session.
	reauthorizeInterval = 30 * time.Second
)

// Close codes sent to clients. 4xxx codes are application-defined; clients
// stop reconnecting on codes that will not change by retrying.
const (
	closeGone          websocket.StatusCode = 4404
	closeAccessChanged websocket.StatusCode = 4403
	closeSlowConsumer  websocket.StatusCode = 4008
	closeUnavailable   websocket.StatusCode = 4500
)

// ServeDocument handles GET /collab/{kind}/{id}/events: it authorizes the
// caller, upgrades to a WebSocket, and joins the document's room.
func (h *Hub) ServeDocument(w http.ResponseWriter, r *http.Request) {
	user := utils.GetCurrentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	backend, ok := h.backends[r.PathValue("kind")]
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !h.originOK(r) {
		slog.Warn("collab: cross-site WebSocket handshake blocked", //nolint:gosec // logging the rejected origin is intentional
			"origin", r.Header.Get("Origin"), "remote_addr", r.RemoteAddr)
		writeError(w, http.StatusForbidden, "cross-site request blocked")
		return
	}
	access, err := backend.Authorize(r.Context(), user.ID, id)
	if err != nil {
		slog.Error("collab: authorize", "kind", backend.Kind(), "id", id, "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if access == AccessNone {
		// Existence-masking: indistinguishable from a missing document.
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	h.registerShutdown(r)
	release := func() {}
	if h.limiter != nil {
		var ok bool
		release, ok = h.limiter.AcquireUserStream(user.ID)
		if !ok {
			writeError(w, http.StatusTooManyRequests, "too many live connections are open for this account")
			return
		}
	}
	defer release()

	// The server's ReadTimeout/WriteTimeout would sever the hijacked socket;
	// clear them before the upgrade.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The origin was verified above against the configured policy.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(readLimit)

	c := newConn(newEpoch(), user.ID, displayName(user), user.AvatarURL, access == AccessEdit)
	room, err := h.enter(backend, id, c)
	if err != nil {
		_ = ws.Close(websocket.StatusGoingAway, "unavailable")
		return
	}
	defer h.exit(room, c)
	// However the read loop ends, the writer closes the socket and exits.
	defer c.kick("closed")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.writeLoop(ctx, ws, c)
	go h.reauthorize(ctx, backend, id, c, access)

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m Message
		if err := json.Unmarshal(data, &m); err != nil {
			c.kick("bad_frame")
			return
		}
		if !room.enqueue(event{kind: evMessage, conn: c, msg: &m}) {
			return
		}
	}
}

// ServeStatus handles GET /collab/status: the document kinds that support
// co-editing on this server.
func (h *Hub) ServeStatus(w http.ResponseWriter, _ *http.Request) {
	kinds := make([]string, 0, len(h.backends))
	for kind := range h.backends {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	body, _ := json.Marshal(map[string]any{"enabled": true, "kinds": kinds})
	_, _ = w.Write(body)
}

// writeLoop sends queued frames and pings until the connection is kicked.
func (h *Hub) writeLoop(ctx context.Context, ws *websocket.Conn, c *conn) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case frame := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := ws.Write(wctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				c.kick("write_failed")
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := ws.Ping(pctx)
			cancel()
			if err != nil {
				c.kick("ping_failed")
			}
		case <-c.kicked:
			// Drain what the room queued before the kick (for example the error
			// that explains it), then close with a code the client can act on.
		drain:
			for {
				select {
				case frame := <-c.send:
					wctx, cancel := context.WithTimeout(ctx, time.Second)
					_ = ws.Write(wctx, websocket.MessageText, frame)
					cancel()
				default:
					break drain
				}
			}
			_ = ws.Close(closeCode(c.kickCause), c.kickCause)
			return
		}
	}
}

// reauthorize ends the session when the user's access changes, so a revoked
// permission takes effect without waiting for the client to leave.
func (h *Hub) reauthorize(ctx context.Context, b Backend, id int, c *conn, granted Access) {
	ticker := time.NewTicker(reauthorizeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.kicked:
			return
		case <-ticker.C:
			access, err := b.Authorize(ctx, c.userID, id)
			if err != nil {
				continue
			}
			if access != granted {
				c.kick("access_changed")
				return
			}
		}
	}
}

func (h *Hub) originOK(r *http.Request) bool {
	if h.originAllowed != nil {
		return h.originAllowed(r)
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func closeCode(cause string) websocket.StatusCode {
	switch cause {
	case "gone":
		return closeGone
	case "access_changed":
		return closeAccessChanged
	case "slow_consumer":
		return closeSlowConsumer
	case "server_shutdown":
		return websocket.StatusGoingAway
	case "unavailable":
		return closeUnavailable
	default:
		return websocket.StatusNormalClosure
	}
}

func displayName(u *models.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	return name
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]string{"error": message})
	_, _ = w.Write(body)
}

package collab

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"windshift/internal/database"
)

// StreamLimiter reserves one of a user's long-lived stream slots. The SSE hub
// implements it, so live updates and co-editing share one per-user cap.
type StreamLimiter interface {
	AcquireUserStream(userID int) (release func(), ok bool)
}

// ErrUnknownKind reports a document kind with no registered backend.
var ErrUnknownKind = errors.New("collab: unknown document kind")

// errShuttingDown reports that the hub no longer accepts connections.
var errShuttingDown = errors.New("collab: shutting down")

// Hub owns every live room in the process.
type Hub struct {
	store         *Store
	limiter       StreamLimiter
	originAllowed func(*http.Request) bool
	backends      map[string]Backend

	// probeInterval is how often an open room polls its document for writes
	// made outside the session.
	probeInterval time.Duration
	// settleInterval is how often due bursts of edits are settled.
	settleInterval time.Duration
	settleStop     chan struct{}
	settleDone     chan struct{}

	mu           sync.Mutex
	rooms        map[string]*room
	conns        map[*conn]struct{}
	shutdown     bool
	shutdownHook sync.Once
}

// defaultProbeInterval bounds how long an outside write (an agent's edit, a
// rename, an archive) takes to reach an open session. Each open document
// costs one indexed single-row query per interval.
const defaultProbeInterval = 2 * time.Second

// NewHub creates a hub persisting to db. limiter may be nil.
func NewHub(db database.Database, limiter StreamLimiter) *Hub {
	return &Hub{
		store:          NewStore(db),
		limiter:        limiter,
		probeInterval:  defaultProbeInterval,
		settleInterval: settleScanInterval,
		backends:       make(map[string]Backend),
		rooms:          make(map[string]*room),
		conns:          make(map[*conn]struct{}),
	}
}

// StartSettling settles bursts of edits for backends implementing Settler,
// until Shutdown. Call after registering backends.
func (h *Hub) StartSettling() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.settleStop != nil || h.shutdown {
		return
	}
	h.settleStop = make(chan struct{})
	h.settleDone = make(chan struct{})
	go h.runSettler(h.settleStop, h.settleDone)
}

// SetOriginPolicy installs the cross-site check for WebSocket handshakes.
// Upgrades are GET requests, which the CSRF middleware deliberately skips, so
// the handler must verify the origin itself. Without a policy only same-host
// origins are accepted.
func (h *Hub) SetOriginPolicy(allowed func(*http.Request) bool) {
	h.originAllowed = allowed
}

// Register adds a document backend. Call before serving requests.
func (h *Hub) Register(b Backend) {
	h.backends[b.Kind()] = b
}

// Notify wakes the room for a document, if one is open, to merge a write made
// outside the session right away instead of at its next probe. It never
// blocks. Optional: rooms find outside writes by polling regardless.
func (h *Hub) Notify(kind string, id int) {
	h.mu.Lock()
	r := h.rooms[docKey(kind, id)]
	h.mu.Unlock()
	if r != nil {
		r.notifyExternal()
	}
}

// registerShutdown closes sessions when the HTTP server shuts down. Hijacked
// WebSocket connections are invisible to http.Server.Shutdown, and hooking it
// from the first request keeps the server's shutdown sequence unchanged.
func (h *Hub) registerShutdown(r *http.Request) {
	srv, ok := r.Context().Value(http.ServerContextKey).(*http.Server)
	if !ok {
		return
	}
	h.shutdownHook.Do(func() {
		srv.RegisterOnShutdown(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = h.Shutdown(ctx)
		})
	})
}

// Shutdown disconnects every client and waits for rooms to flush.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.shutdown = true
	for c := range h.conns {
		c.kick("server_shutdown")
	}
	rooms := make([]*room, 0, len(h.rooms))
	for _, r := range h.rooms {
		rooms = append(rooms, r)
	}
	settleDone := h.settleDone
	if h.settleStop != nil {
		close(h.settleStop)
		h.settleStop = nil
	}
	h.mu.Unlock()
	for _, r := range rooms {
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if settleDone != nil {
		select {
		case <-settleDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func docKey(kind string, id int) string {
	return kind + ":" + strconv.Itoa(id)
}

// enter admits c to the room for (kind, id), opening the room if needed. A
// room that is closing is awaited so its final writes land before the next
// room loads the document.
func (h *Hub) enter(b Backend, id int, c *conn) (*room, error) {
	key := docKey(b.Kind(), id)
	for {
		h.mu.Lock()
		if h.shutdown {
			h.mu.Unlock()
			return nil, errShuttingDown
		}
		r := h.rooms[key]
		if r == nil {
			r = newRoom(h, b, key, id)
			h.rooms[key] = r
			go r.run()
		}
		if r.admit() {
			h.conns[c] = struct{}{}
			h.mu.Unlock()
			if !r.enqueue(event{kind: evJoin, conn: c}) {
				h.exit(r, c)
				return nil, errShuttingDown
			}
			return r, nil
		}
		done := r.done
		h.mu.Unlock()
		<-done
	}
}

// exit removes c from the room. Every successful enter must be paired with
// exactly one exit.
func (h *Hub) exit(r *room, c *conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
	r.enqueue(event{kind: evLeave, conn: c})
}

// forget drops a closing room from the index.
func (h *Hub) forget(r *room) {
	h.mu.Lock()
	if h.rooms[r.key] == r {
		delete(h.rooms, r.key)
	}
	h.mu.Unlock()
}

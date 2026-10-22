package collab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"windshift/internal/contextkeys"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/services"
)

// fakeBackend is an in-memory document store with per-user access.
type fakeBackend struct {
	kind        string
	mu          sync.Mutex
	content     map[int]Content
	meta        map[int]string
	access      map[int]Access // by user id
	checkpoints int
	finalized   int
	// db, when set, runs checkpoint callbacks in a transaction like the
	// real backends do.
	db database.Database
}

func newFakeBackend(kind string) *fakeBackend {
	return &fakeBackend{kind: kind, content: map[int]Content{}, meta: map[int]string{}, access: map[int]Access{}}
}

func hashOf(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (b *fakeBackend) set(id int, md string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.content[id] = Content{Markdown: md, Hash: hashOf(md)}
}

func (b *fakeBackend) get(id int) Content {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.content[id]
}

func (b *fakeBackend) Kind() string              { return b.kind }
func (b *fakeBackend) OwnerPageID(int) *int      { return nil }
func (b *fakeBackend) setAccess(u int, a Access) { b.mu.Lock(); b.access[u] = a; b.mu.Unlock() }

func (b *fakeBackend) Authorize(_ context.Context, userID, id int) (Access, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.content[id]; !ok {
		return AccessNone, nil
	}
	return b.access[userID], nil
}

func (b *fakeBackend) Load(_ context.Context, id int) (Content, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.content[id]
	if !ok {
		return Content{}, ErrGone
	}
	return c, nil
}

func (b *fakeBackend) setMeta(id int, meta string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.meta[id] = meta
}

func (b *fakeBackend) Probe(_ context.Context, id int) (Probe, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.content[id]
	if !ok {
		return Probe{}, ErrGone
	}
	return Probe{ContentHash: c.Hash, Meta: b.meta[id]}, nil
}

func (b *fakeBackend) Checkpoint(_ context.Context, _ int, id int, md, expected string, after func(database.Tx, Content) error) (Content, error) {
	b.mu.Lock()
	cur, ok := b.content[id]
	if !ok {
		b.mu.Unlock()
		return Content{}, ErrGone
	}
	if cur.Hash != expected {
		b.mu.Unlock()
		return Content{}, ErrConflict
	}
	next := Content{Markdown: md, Hash: hashOf(md)}
	if b.db != nil {
		if err := database.WithTx(b.db, func(tx database.Tx) error { return after(tx, next) }); err != nil {
			b.mu.Unlock()
			return Content{}, err
		}
	}
	b.content[id] = next
	b.checkpoints++
	b.mu.Unlock()
	return next, nil
}

func (b *fakeBackend) Finalize(context.Context, int, int) error {
	b.mu.Lock()
	b.finalized++
	b.mu.Unlock()
	return nil
}

type harness struct {
	t       *testing.T
	kind    string
	hub     *Hub
	backend *fakeBackend
	server  *httptest.Server
}

var (
	sharedDBOnce sync.Once
	sharedDB     database.Database
	sharedDBErr  error
	harnessSeq   atomic.Int64
)

// testDB initializes one schema per test binary; harnesses keep their rows
// apart with distinct document kinds.
func testDB(t *testing.T) database.Database {
	t.Helper()
	sharedDBOnce.Do(func() {
		dir, err := os.MkdirTemp("", "collab-test-*")
		if err != nil {
			sharedDBErr = err
			return
		}
		sharedDB, sharedDBErr = database.NewSQLiteDB(filepath.Join(dir, "collab.db"))
		if sharedDBErr == nil {
			sharedDBErr = sharedDB.Initialize()
		}
		if sharedDBErr == nil {
			sharedDBErr = ensureSchema(sharedDB)
		}
	})
	if sharedDBErr != nil {
		t.Fatal(sharedDBErr)
	}
	return sharedDB
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := testDB(t)
	hub := NewHub(db, nil)
	// Probe far apart by default so tests drive outside writes explicitly;
	// probing tests shorten it.
	hub.probeInterval = time.Hour
	kind := fmt.Sprintf("doc%d", harnessSeq.Add(1))
	backend := newFakeBackend(kind)
	hub.Register(backend)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /collab/{kind}/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := strconv.Atoi(r.URL.Query().Get("user"))
		user := &models.User{ID: uid, Username: "user" + strconv.Itoa(uid)}
		hub.ServeDocument(w, r.WithContext(context.WithValue(r.Context(), contextkeys.User, user)))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Shutdown(ctx)
		server.Close()
	})
	return &harness{t: t, kind: kind, hub: hub, backend: backend, server: server}
}

type client struct {
	t     *testing.T
	ws    *websocket.Conn
	inbox chan Message
	done  chan struct{}
	// closeCode is set when the server closes the socket.
	closeCode websocket.StatusCode
}

func (h *harness) dial(user, doc int) *client {
	h.t.Helper()
	c, err := h.tryDial(user, doc, "")
	if err != nil {
		h.t.Fatalf("dial user %d: %v", user, err)
	}
	return c
}

func (h *harness) tryDial(user, doc int, origin string) (*client, error) {
	url := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/collab/" + h.kind + "/" + strconv.Itoa(doc) + "/events?user=" + strconv.Itoa(user)
	opts := &websocket.DialOptions{}
	if origin != "" {
		opts.HTTPHeader = http.Header{"Origin": []string{origin}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(readLimit)
	c := &client{t: h.t, ws: ws, inbox: make(chan Message, 256), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for {
			_, data, err := ws.Read(context.Background())
			if err != nil {
				c.closeCode = websocket.CloseStatus(err)
				return
			}
			var m Message
			if err := json.Unmarshal(data, &m); err == nil {
				c.inbox <- m
			}
		}
	}()
	h.t.Cleanup(func() { _ = ws.CloseNow() })
	return c, nil
}

func (c *client) send(m Message) {
	c.t.Helper()
	data, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// expect returns the next message of type typ, skipping presence chatter.
func (c *client) expect(typ string) Message {
	c.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-c.inbox:
			if m.Type == typ {
				return m
			}
			switch m.Type {
			case msgPeers, msgAwareness, msgLeave, msgStored, msgCheckpointed, msgUpdate:
				continue
			}
			c.t.Fatalf("expected %q, got %q (%+v)", typ, m.Type, m)
		case <-deadline:
			c.t.Fatalf("timed out waiting for %q", typ)
		}
	}
}

// quiet asserts no message of type typ arrives within d.
func (c *client) quiet(typ string, d time.Duration) {
	c.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m := <-c.inbox:
			if m.Type == typ {
				c.t.Fatalf("unexpected %q: %+v", typ, m)
			}
		case <-deadline:
			return
		}
	}
}

func (c *client) close() {
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
	<-c.done
}

// seed answers a seed request as a browser would and returns the epoch.
func (c *client) seed(payload string) string {
	c.t.Helper()
	req := c.expect(msgSeedRequest)
	c.send(Message{Type: msgSeed, Data: []byte(payload), Epoch: req.Epoch})
	c.expect(msgAck)
	return req.Epoch
}

func TestSeedingIsCoordinatedByOneLeader(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "# Hello")
	h.backend.setAccess(10, AccessEdit)
	h.backend.setAccess(11, AccessEdit)

	a := h.dial(10, 1)
	initA := a.expect(msgInit)
	if *initA.Seeded || initA.Role != "leader" || !*initA.Writable {
		t.Fatalf("first editor init = %+v", initA)
	}
	req := a.expect(msgSeedRequest)
	if req.Markdown != "# Hello" || req.Hash != hashOf("# Hello") || req.Epoch == "" {
		t.Fatalf("seed request = %+v", req)
	}

	b := h.dial(11, 1)
	initB := b.expect(msgInit)
	if *initB.Seeded || initB.Role != "follower" {
		t.Fatalf("second editor init = %+v", initB)
	}
	b.quiet(msgSeedRequest, 200*time.Millisecond)

	// A follower cannot seed or write before the document exists.
	b.send(Message{Type: msgSeed, Data: []byte("dup"), Epoch: req.Epoch})
	if e := b.expect(msgError); e.Error != "unexpected_seed" {
		t.Fatalf("follower seed error = %q", e.Error)
	}

	a.send(Message{Type: msgSeed, Data: []byte("SEED"), Epoch: req.Epoch})
	if ack := a.expect(msgAck); ack.Seq != 1 {
		t.Fatalf("seed ack = %+v", ack)
	}
	resync := b.expect(msgInit)
	if !*resync.Seeded || resync.Epoch != req.Epoch || len(resync.Updates) != 1 || string(resync.Updates[0]) != "SEED" {
		t.Fatalf("follower resync = %+v", resync)
	}
}

func TestUpdatesRelayAndPersistAcrossSessions(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "text")
	h.backend.setAccess(10, AccessEdit)
	h.backend.setAccess(20, AccessView)

	a := h.dial(10, 1)
	a.expect(msgInit)
	epoch := a.seed("S")
	v := h.dial(20, 1)
	vInit := v.expect(msgInit)
	if *vInit.Writable || len(vInit.Updates) != 1 {
		t.Fatalf("viewer init = %+v", vInit)
	}

	a.send(Message{Type: msgUpdate, Data: []byte("U2"), Epoch: epoch})
	if ack := a.expect(msgAck); ack.Seq != 2 {
		t.Fatalf("ack = %+v", ack)
	}
	if u := v.expect(msgUpdate); string(u.Data) != "U2" || u.Seq != 2 {
		t.Fatalf("relayed = %+v", u)
	}

	// Viewers cannot write; stale epochs are refused.
	v.send(Message{Type: msgUpdate, Data: []byte("X"), Epoch: epoch})
	if e := v.expect(msgError); e.Error != "read_only" {
		t.Fatalf("viewer write error = %q", e.Error)
	}
	a.send(Message{Type: msgUpdate, Data: []byte("X"), Epoch: "old"})
	if e := a.expect(msgError); e.Error != "stale_epoch" {
		t.Fatalf("stale epoch error = %q", e.Error)
	}

	v.close()
	a.close()
	// The room flushes and closes; a new session replays the persisted log.
	waitForRoomClose(t, h.hub, h.kind+":1")
	a2 := h.dial(10, 1)
	init := a2.expect(msgInit)
	if !*init.Seeded || init.Epoch != epoch || len(init.Updates) != 2 || string(init.Updates[1]) != "U2" || init.Seq != 2 {
		t.Fatalf("reopened init = %+v", init)
	}
}

func waitForRoomClose(t *testing.T, h *Hub, key string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		r := h.rooms[key]
		h.mu.Unlock()
		if r == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("room %s did not close", key)
}

func TestCheckpointAndExternalWriteReconcile(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)
	h.backend.setAccess(11, AccessEdit)

	a := h.dial(10, 1)
	a.expect(msgInit)
	epoch := a.seed("S")
	b := h.dial(11, 1)
	b.expect(msgInit)

	a.send(Message{Type: msgUpdate, Data: []byte("U"), Epoch: epoch})
	a.expect(msgAck)
	a.send(Message{Type: msgCheckpoint, Markdown: "v2", Hash: hashOf("v1"), Seq: 2})
	cp := b.expect(msgCheckpointed)
	if cp.Hash != hashOf("v2") || cp.Seq != 2 {
		t.Fatalf("checkpointed = %+v", cp)
	}
	if got := h.backend.get(1); got.Markdown != "v2" {
		t.Fatalf("stored = %q", got.Markdown)
	}

	// An agent writes the page outside the session.
	h.backend.set(1, "v2 plus agent")
	h.hub.Notify(h.kind, 1)
	rec := a.expect(msgReconcile)
	if rec.Base != "v2" || rec.Markdown != "v2 plus agent" || rec.Hash != hashOf("v2 plus agent") {
		t.Fatalf("reconcile = %+v", rec)
	}
	b.quiet(msgReconcile, 200*time.Millisecond)

	// A checkpoint on the old base is refused and re-requests the merge only
	// once; the merged checkpoint on the new base is accepted.
	a.send(Message{Type: msgCheckpoint, Markdown: "stale", Hash: hashOf("v2"), Seq: 2})
	a.quiet(msgCheckpointed, 200*time.Millisecond)
	a.send(Message{Type: msgCheckpoint, Markdown: "v2 plus agent and typing", Hash: rec.Hash, Seq: 2})
	a.expect(msgCheckpointed)
	if got := h.backend.get(1); got.Markdown != "v2 plus agent and typing" {
		t.Fatalf("stored after merge = %q", got.Markdown)
	}
}

func TestLeaderHandoffAndSeedReassignment(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)
	h.backend.setAccess(11, AccessEdit)

	a := h.dial(10, 1)
	a.expect(msgInit)
	a.expect(msgSeedRequest)
	b := h.dial(11, 1)
	b.expect(msgInit)

	// The seeder leaves before seeding: the next editor is asked instead.
	a.close()
	role := b.expect(msgRole)
	if role.Role != "leader" {
		t.Fatalf("role = %+v", role)
	}
	epoch := b.seed("S")

	c := h.dial(10, 1)
	init := c.expect(msgInit)
	if init.Role != "follower" || !*init.Seeded || init.Epoch != epoch {
		t.Fatalf("rejoin init = %+v", init)
	}
	b.close()
	if r := c.expect(msgRole); r.Role != "leader" {
		t.Fatalf("handoff role = %+v", r)
	}
}

func TestExternalWriteWithoutEditorsResetsCleanDocument(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)
	h.backend.setAccess(20, AccessView)

	a := h.dial(10, 1)
	a.expect(msgInit)
	a.seed("S")
	v := h.dial(20, 1)
	v.expect(msgInit)
	a.close()

	h.backend.set(1, "agent rewrite")
	h.hub.Notify(h.kind, 1)
	reset := v.expect(msgReset)
	if reset.Hash != hashOf("agent rewrite") {
		t.Fatalf("reset = %+v", reset)
	}

	// The next editor reseeds from the new markdown.
	e := h.dial(10, 1)
	init := e.expect(msgInit)
	if *init.Seeded {
		t.Fatalf("expected unseeded after reset: %+v", init)
	}
	if req := e.expect(msgSeedRequest); req.Markdown != "agent rewrite" {
		t.Fatalf("reseed markdown = %q", req.Markdown)
	}
}

func TestExternalWriteWithUnsavedEditsMarksStale(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)
	h.backend.setAccess(20, AccessView)

	a := h.dial(10, 1)
	a.expect(msgInit)
	epoch := a.seed("S")
	v := h.dial(20, 1)
	v.expect(msgInit)
	// An edit that never reaches a checkpoint, then the editor drops.
	a.send(Message{Type: msgUpdate, Data: []byte("U"), Epoch: epoch})
	a.expect(msgAck)
	a.close()

	h.backend.set(1, "agent rewrite")
	h.hub.Notify(h.kind, 1)
	st := v.expect(msgState)
	if st.Stale == nil || !*st.Stale {
		t.Fatalf("state = %+v", st)
	}

	// The next editor merges the outside write into the unsaved session.
	e := h.dial(10, 1)
	init := e.expect(msgInit)
	if !*init.Seeded || !*init.Dirty || init.Role != "leader" {
		t.Fatalf("leader init = %+v", init)
	}
	rec := e.expect(msgReconcile)
	if rec.Base != "v1" || rec.Markdown != "agent rewrite" {
		t.Fatalf("reconcile = %+v", rec)
	}
}

func TestAccessAndOriginChecks(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)

	if _, err := h.tryDial(99, 1, ""); err == nil {
		t.Fatal("user without access connected")
	}
	if _, err := h.tryDial(10, 2, ""); err == nil {
		t.Fatal("missing document connected")
	}
	if _, err := h.tryDial(10, 1, "https://evil.example"); err == nil {
		t.Fatal("cross-site origin connected")
	}
	same := strings.TrimPrefix(h.server.URL, "http://")
	c, err := h.tryDial(10, 1, "http://"+same)
	if err != nil {
		t.Fatalf("same-origin dial: %v", err)
	}
	c.expect(msgInit)
}

func TestDocumentGoneClosesSession(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)
	a := h.dial(10, 1)
	a.expect(msgInit)
	a.seed("S")

	h.backend.mu.Lock()
	delete(h.backend.content, 1)
	h.backend.mu.Unlock()
	h.hub.Notify(h.kind, 1)
	if e := a.expect(msgError); e.Error != "gone" {
		t.Fatalf("error = %+v", e)
	}
	<-a.done
	if a.closeCode != closeGone {
		t.Fatalf("close code = %d", a.closeCode)
	}
}

func TestHelloPublishesClientIDAndAwarenessRelays(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)
	h.backend.setAccess(11, AccessEdit)
	a := h.dial(10, 1)
	a.expect(msgInit)
	a.seed("S")
	a.send(Message{Type: msgHello, ClientID: 77})
	a.send(Message{Type: msgAwareness, Data: []byte("cursor")})

	b := h.dial(11, 1)
	init := b.expect(msgInit)
	if len(init.Aware) != 1 || string(init.Aware[0]) != "cursor" {
		t.Fatalf("awareness snapshot = %+v", init.Aware)
	}
	found := false
	for _, p := range init.Peers {
		if p.UserID == 10 && p.ClientID == 77 {
			found = true
		}
	}
	if !found {
		t.Fatalf("peers = %+v", init.Peers)
	}
	a.close()
	if l := b.expect(msgLeave); len(l.Clients) != 1 || l.Clients[0] != 77 {
		t.Fatalf("leave = %+v", l)
	}
}

func TestCompactionReplacesLogPrefix(t *testing.T) {
	h := newHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)
	a := h.dial(10, 1)
	a.expect(msgInit)
	epoch := a.seed("S")
	for i := 0; i < compactAfterEntries; i++ {
		a.send(Message{Type: msgUpdate, Data: []byte("u" + strconv.Itoa(i)), Epoch: epoch})
		a.expect(msgAck)
	}
	req := a.expect(msgCompactRequest)
	a.send(Message{Type: msgCompact, Data: []byte("STATE"), Seq: req.Seq})
	a.send(Message{Type: msgUpdate, Data: []byte("after"), Epoch: epoch})
	a.expect(msgAck)
	a.close()
	waitForRoomClose(t, h.hub, h.kind+":1")

	b := h.dial(10, 1)
	init := b.expect(msgInit)
	if len(init.Updates) != 2 || string(init.Updates[0]) != "STATE" || string(init.Updates[1]) != "after" {
		t.Fatalf("compacted log = %d entries", len(init.Updates))
	}
}

func TestProbeAnnouncesRenamesAndMergesOutsideWrites(t *testing.T) {
	h := newHarness(t)
	h.hub.probeInterval = 50 * time.Millisecond
	h.backend.set(1, "v1")
	h.backend.setMeta(1, "Title")
	h.backend.setAccess(10, AccessEdit)
	h.backend.setAccess(20, AccessView)
	a := h.dial(10, 1)
	a.expect(msgInit)
	a.seed("S")
	v := h.dial(20, 1)
	v.expect(msgInit)

	// A rename elsewhere reaches every client without any hook.
	h.backend.setMeta(1, "Renamed")
	a.expect(msgMeta)
	v.expect(msgMeta)

	// So does an outside content write: the leader is asked to merge it.
	h.backend.set(1, "agent edit")
	rec := a.expect(msgReconcile)
	if rec.Base != "v1" || rec.Markdown != "agent edit" {
		t.Fatalf("reconcile = %+v", rec)
	}

	// Archiving (the probe reports gone) ends the session.
	h.backend.mu.Lock()
	delete(h.backend.content, 1)
	h.backend.mu.Unlock()
	if e := v.expect(msgError); e.Error != "gone" {
		t.Fatalf("error = %+v", e)
	}
}

func TestOriginPolicy(t *testing.T) {
	policy := newOriginPolicy([]string{"https://windshift.example"}, false)
	cases := []struct {
		name, site, origin, host string
		want                     bool
	}{
		{"fetch metadata same-origin", "same-origin", "https://x.example", "x.example", true},
		{"fetch metadata cross-site", "cross-site", "https://evil.example", "windshift.example", false},
		{"fetch metadata same-site subdomain", "same-site", "https://sub.windshift.example", "windshift.example", false},
		{"configured origin", "", "https://windshift.example", "internal:7777", true},
		{"same host", "", "http://localhost:7777", "localhost:7777", true},
		{"other origin", "", "https://evil.example", "windshift.example", false},
		{"no origin (not a browser)", "", "", "windshift.example", true},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/api/collab/page/1/events", nil)
		r.Host = tc.host
		if tc.site != "" {
			r.Header.Set("Sec-Fetch-Site", tc.site)
		}
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if got := policy(r); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if !newOriginPolicy(nil, true)(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Error("disabled policy must allow")
	}
}

func TestEnsureSchemaIsIdempotent(t *testing.T) {
	db := testDB(t)
	if err := ensureSchema(db); err != nil {
		t.Fatalf("second ensureSchema: %v", err)
	}
}

func TestInstallRegistersRoutesUnlessDisabled(t *testing.T) {
	db := testDB(t)
	routes := map[string]bool{}
	deps := Deps{
		DB:          db,
		Route:       func(pattern string, _ http.Handler) { routes[pattern] = true },
		RequireAuth: func(h http.Handler) http.Handler { return h },
		Pages:       services.NewPageService(db),
	}

	t.Setenv(DisableEnv, "true")
	if Install(deps) != nil || len(routes) != 0 {
		t.Fatalf("disabled install registered %v", routes)
	}

	t.Setenv(DisableEnv, "")
	if Install(deps) == nil {
		t.Fatal("install returned nil")
	}
	for _, pattern := range []string{"GET /collab/{kind}/{id}/events", "GET /collab/status"} {
		if !routes[pattern] {
			t.Errorf("route %q not registered (got %v)", pattern, routes)
		}
	}
}

// settlingBackend records history once edits go quiet.
type settlingBackend struct {
	*fakeBackend
	mu      sync.Mutex
	settled []Settlement
	gone    bool
}

func (b *settlingBackend) SettleAfter() time.Duration { return 2 * time.Minute }

func (b *settlingBackend) Settle(_ context.Context, _ int, s Settlement) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gone {
		return ErrGone
	}
	b.settled = append(b.settled, s)
	return nil
}

func (b *settlingBackend) takeSettled() []Settlement {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.settled
	b.settled = nil
	return out
}

func newSettlingHarness(t *testing.T) (*harness, *settlingBackend) {
	t.Helper()
	h := newHarness(t)
	h.backend.db = testDB(t)
	sb := &settlingBackend{fakeBackend: h.backend}
	h.hub.Register(sb)
	return h, sb
}

func checkpointed(t *testing.T, c *client, md, base string, seq int64) {
	t.Helper()
	c.send(Message{Type: msgCheckpoint, Markdown: md, Hash: hashOf(base), Seq: seq})
	if got := c.expect(msgCheckpointed); got.Hash != hashOf(md) {
		t.Fatalf("checkpointed = %+v", got)
	}
}

func TestBurstOfEditsSettlesOnceAfterQuietPeriod(t *testing.T) {
	h, sb := newSettlingHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)
	h.backend.setAccess(11, AccessEdit)

	a := h.dial(10, 1)
	a.expect(msgInit)
	epoch := a.seed("S")
	b := h.dial(11, 1)
	b.expect(msgInit)

	// Both type; the leader (10) stores two checkpoints.
	b.send(Message{Type: msgUpdate, Data: []byte("B1"), Epoch: epoch})
	b.expect(msgAck)
	checkpointed(t, a, "v2", "v1", 2)
	a.send(Message{Type: msgUpdate, Data: []byte("A1"), Epoch: epoch})
	a.expect(msgAck)
	b.send(Message{Type: msgUpdate, Data: []byte("B2"), Epoch: epoch})
	b.expect(msgAck)
	checkpointed(t, a, "v3", "v2", 4)

	// Not quiet yet: nothing is recorded.
	h.hub.settleDue(context.Background(), time.Now().Add(time.Minute))
	if got := sb.takeSettled(); len(got) != 0 {
		t.Fatalf("settled before quiet period: %+v", got)
	}
	h.hub.settleDue(context.Background(), time.Now().Add(2*time.Minute+time.Second))
	got := sb.takeSettled()
	if len(got) != 1 {
		t.Fatalf("settled %d bursts, want 1", len(got))
	}
	s := got[0]
	if s.Before.Markdown != "v1" || s.After.Markdown != "v3" || s.After.Hash != hashOf("v3") {
		t.Fatalf("settlement = %+v", s)
	}
	if fmt.Sprint(s.Editors) != "[10 11]" {
		t.Fatalf("editors = %v, want typists with the most recent last", s.Editors)
	}

	// Settled bursts are gone; a later edit starts a new one from v3.
	h.hub.settleDue(context.Background(), time.Now().Add(time.Hour))
	if got := sb.takeSettled(); len(got) != 0 {
		t.Fatalf("settled twice: %+v", got)
	}
	a.send(Message{Type: msgUpdate, Data: []byte("A2"), Epoch: epoch})
	a.expect(msgAck)
	checkpointed(t, a, "v4", "v3", 5)
	h.hub.settleDue(context.Background(), time.Now().Add(time.Hour))
	if got := sb.takeSettled(); len(got) != 1 || got[0].Before.Markdown != "v3" || fmt.Sprint(got[0].Editors) != "[10]" {
		t.Fatalf("second burst = %+v", got)
	}
}

func TestOutsideWriteClosesTheBurstBeforeIt(t *testing.T) {
	h, sb := newSettlingHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)

	a := h.dial(10, 1)
	a.expect(msgInit)
	epoch := a.seed("S")
	a.send(Message{Type: msgUpdate, Data: []byte("U"), Epoch: epoch})
	a.expect(msgAck)
	checkpointed(t, a, "v2", "v1", 2)

	// An outside write records its own history; the session's burst must not
	// include it, so it closes and settles without waiting.
	h.backend.set(1, "v2 plus agent")
	h.hub.Notify(h.kind, 1)
	rec := a.expect(msgReconcile)
	a.send(Message{Type: msgUpdate, Data: []byte("U2"), Epoch: epoch})
	a.expect(msgAck)
	a.send(Message{Type: msgCheckpoint, Markdown: "v2 plus agent and typing", Hash: rec.Hash, Seq: 3})
	a.expect(msgCheckpointed)

	h.hub.settleDue(context.Background(), time.Now())
	got := sb.takeSettled()
	if len(got) != 1 || got[0].Before.Markdown != "v1" || got[0].After.Markdown != "v2" {
		t.Fatalf("closed burst = %+v", got)
	}
	h.hub.settleDue(context.Background(), time.Now().Add(3*time.Minute))
	got = sb.takeSettled()
	if len(got) != 1 || got[0].Before.Markdown != "v2 plus agent" || got[0].After.Markdown != "v2 plus agent and typing" {
		t.Fatalf("burst after the outside write = %+v", got)
	}
}

func TestBurstSurvivesRestartAndClaimsOnce(t *testing.T) {
	h, sb := newSettlingHarness(t)
	h.backend.set(1, "v1")
	h.backend.setAccess(10, AccessEdit)

	a := h.dial(10, 1)
	a.expect(msgInit)
	epoch := a.seed("S")
	a.send(Message{Type: msgUpdate, Data: []byte("U"), Epoch: epoch})
	a.expect(msgAck)
	checkpointed(t, a, "v2", "v1", 2)
	a.close()

	// A fresh hub (a restarted server) settles what the old one left.
	hub := NewHub(testDB(t), nil)
	hub.Register(sb)
	ctx := context.Background()
	due, err := hub.store.dueBursts(ctx, h.kind, time.Now().Add(time.Hour))
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %+v, %v", due, err)
	}
	// A burst extended after it was read is not claimed.
	if ok, err := hub.store.claimBurst(ctx, due[0].id, due[0].version+1); ok || err != nil {
		t.Fatalf("claimed a newer version: %v %v", ok, err)
	}
	hub.settleDue(ctx, time.Now().Add(3*time.Minute))
	if got := sb.takeSettled(); len(got) != 1 || got[0].After.Markdown != "v2" {
		t.Fatalf("settled after restart = %+v", got)
	}

	// A burst of a deleted document is dropped.
	a = h.dial(10, 1)
	a.expect(msgInit)
	a.send(Message{Type: msgUpdate, Data: []byte("U3"), Epoch: epoch})
	a.expect(msgAck)
	checkpointed(t, a, "v3", "v2", 3)
	sb.mu.Lock()
	sb.gone = true
	sb.mu.Unlock()
	hub.settleDue(ctx, time.Now().Add(3*time.Minute))
	if due, _ := hub.store.dueBursts(ctx, h.kind, time.Now().Add(time.Hour)); len(due) != 0 {
		t.Fatalf("gone burst kept: %+v", due)
	}
}

func TestMergeEditorsKeepsMostRecentLast(t *testing.T) {
	if got := fmt.Sprint(mergeEditors([]int{1, 2, 3}, []int{2, 4, 1})); got != "[3 2 4 1]" {
		t.Fatalf("mergeEditors = %s", got)
	}
	if got := fmt.Sprint(parseEditors(formatEditors([]int{5, 7}))); got != "[5 7]" {
		t.Fatalf("round trip = %s", got)
	}
}

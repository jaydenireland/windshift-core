package collab

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"windshift/internal/database"
)

const (
	// appendFlushInterval batches update-log inserts so a burst of keystrokes
	// costs one transaction. A crash loses at most this window from the server
	// log; reconnecting clients send back anything the server is missing.
	appendFlushInterval = 200 * time.Millisecond
	// compactAfterEntries asks the leader to compact once the log grows this
	// long, keeping joins and memory bounded.
	compactAfterEntries = 200
	// compactAfterBytes is the size equivalent of compactAfterEntries.
	compactAfterBytes = 4 << 20
	// sendBuffer bounds frames queued for one connection. A client that falls
	// this far behind is disconnected and resynchronizes on reconnect.
	sendBuffer = 512
	// maxLogBytes caps a document's uncompacted log. The leader is asked to
	// compact long before this; the cap only stops a client that never does
	// from growing server memory and storage without bound.
	maxLogBytes = 64 << 20
	// maxAwarenessBytes bounds one client's presence payload (cursor, name),
	// which is relayed to every participant.
	maxAwarenessBytes = 64 << 10
)

// conn is one client connection as the room sees it. The transport owns the
// socket; the room only queues encoded frames and may kick the connection.
type conn struct {
	id       string
	userID   int
	name     string
	avatar   string
	writable bool

	clientID  uint64
	awareness []byte

	send      chan []byte
	kickOnce  sync.Once
	kicked    chan struct{}
	kickCause string
}

func newConn(id string, userID int, name, avatar string, writable bool) *conn {
	return &conn{
		id:       id,
		userID:   userID,
		name:     name,
		avatar:   avatar,
		writable: writable,
		send:     make(chan []byte, sendBuffer),
		kicked:   make(chan struct{}),
	}
}

// kick asks the transport to close the connection.
func (c *conn) kick(cause string) {
	c.kickOnce.Do(func() {
		c.kickCause = cause
		close(c.kicked)
	})
}

func (c *conn) peer() Peer {
	return Peer{ConnID: c.id, UserID: c.userID, Name: c.name, Avatar: c.avatar, ClientID: c.clientID, Writable: c.writable}
}

type eventKind int

const (
	evJoin eventKind = iota
	evLeave
	evMessage
	evPersisted
)

type event struct {
	kind eventKind
	conn *conn
	msg  *Message
	done func()
}

// room is one live document. Its state is owned by the run goroutine; other
// goroutines talk to it through events and the external-change wake channel.
type room struct {
	hub     *Hub
	key     string
	id      int
	backend Backend

	events   chan event
	external chan struct{}
	stopping chan struct{}
	done     chan struct{}

	mu       sync.Mutex
	members  int
	closing  bool
	persistQ *persistQueue

	// Loop-owned state.
	conns  []*conn // join order; the first writable connection leads
	leader *conn

	seeded        bool
	epoch         string
	log           []update
	logBytes      int
	nextSeq       int64
	checkpointSeq int64

	materializedHash    string
	materializedContent string
	stored              Content
	storedMeta          string
	staleAnnounced      bool
	reconcileTarget     string

	seeder      *conn
	seedEpoch   string
	seedContent Content

	pendingAppends   []update
	checkpointBusy   bool
	queuedCheckpoint *checkpointRequest
	compactPending   bool
	wroteCheckpoint  bool
	lastActor        int
}

type checkpointRequest struct {
	conn     *conn
	markdown string
	hash     string
	seq      int64
}

func newRoom(h *Hub, backend Backend, key string, id int) *room {
	r := &room{
		hub:      h,
		key:      key,
		id:       id,
		backend:  backend,
		events:   make(chan event, 1024),
		external: make(chan struct{}, 1),
		stopping: make(chan struct{}),
		done:     make(chan struct{}),
	}
	r.persistQ = newPersistQueue(r)
	return r
}

// admit reserves a membership slot unless the room is shutting down. The slot
// keeps the room alive until the matching leave is processed.
func (r *room) admit() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return false
	}
	r.members++
	return true
}

// release gives back a slot and reports whether the room should close.
func (r *room) release() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members--
	if r.members <= 0 {
		r.closing = true
		return true
	}
	return false
}

// notifyExternal wakes the room to re-read stored content. It never blocks.
func (r *room) notifyExternal() {
	select {
	case r.external <- struct{}{}:
	default:
	}
}

func (r *room) run() {
	defer close(r.done)
	go r.persistQ.run()
	if err := r.load(); err != nil {
		slog.Error("collab: load document", "doc", r.key, "error", err)
		r.failAll("unavailable")
		return
	}
	ticker := time.NewTicker(appendFlushInterval)
	defer ticker.Stop()
	probe := time.NewTicker(r.hub.probeInterval)
	defer probe.Stop()
	for {
		select {
		case ev := <-r.events:
			if r.handle(ev) {
				r.shutdown()
				return
			}
		case <-r.external:
			r.refreshStored()
		case <-ticker.C:
			r.flushAppends()
		case <-probe.C:
			r.probe()
		}
	}
}

// failAll drains joins after a load failure so every connection is told why
// and every membership slot is released.
func (r *room) failAll(reason string) {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	close(r.stopping)
	r.hub.forget(r)
	r.persistQ.stop()
	for {
		select {
		case ev := <-r.events:
			if ev.conn != nil {
				ev.conn.kick(reason)
			}
			if ev.done != nil {
				ev.done()
			}
		default:
			return
		}
	}
}

func (r *room) load() error {
	ctx := context.Background()
	st, updates, err := r.hub.store.load(ctx, r.key)
	if err != nil {
		return err
	}
	stored, err := r.backend.Load(ctx, r.id)
	if err != nil {
		return err
	}
	r.stored = stored
	if p, err := r.backend.Probe(ctx, r.id); err == nil {
		r.storedMeta = p.Meta
	}
	r.nextSeq = st.nextSeq
	if r.nextSeq < 1 {
		r.nextSeq = 1
	}
	if st.exists && st.seeded && len(updates) > 0 {
		r.seeded = true
		r.epoch = st.epoch
		r.log = updates
		for _, u := range updates {
			r.logBytes += len(u.data)
			if u.seq >= r.nextSeq {
				r.nextSeq = u.seq + 1
			}
		}
		r.checkpointSeq = st.checkpointSeq
		r.materializedHash = st.materializedHash
		r.materializedContent = st.materializedContent
	}
	return nil
}

// handle processes one event and reports whether the room is now empty.
func (r *room) handle(ev event) bool {
	switch ev.kind {
	case evJoin:
		r.join(ev.conn)
	case evLeave:
		r.leave(ev.conn)
		if r.release() {
			return true
		}
	case evMessage:
		r.message(ev.conn, ev.msg)
	case evPersisted:
		if ev.done != nil {
			ev.done()
		}
	}
	return false
}

func (r *room) lastSeq() int64 { return r.nextSeq - 1 }

func (r *room) dirty() bool { return r.seeded && r.lastSeq() > r.checkpointSeq }

func (r *room) stale() bool { return r.seeded && r.stored.Hash != r.materializedHash }

func (r *room) indexOf(c *conn) int {
	for i, existing := range r.conns {
		if existing == c {
			return i
		}
	}
	return -1
}

func (r *room) join(c *conn) {
	r.conns = append(r.conns, c)
	if r.leader == nil && c.writable {
		r.leader = c
	}
	r.sendInit(c)
	r.broadcastPeers(c)
	if r.leader == c {
		r.leaderDuties()
	}
}

func (r *room) sendInit(c *conn) {
	seeded := r.seeded
	writable := c.writable
	stale := r.stale() && r.leader == nil
	dirty := r.leader == c && r.dirty()
	updates := make([][]byte, 0, len(r.log))
	if r.seeded {
		for _, u := range r.log {
			updates = append(updates, u.data)
		}
	}
	var aware [][]byte
	for _, other := range r.conns {
		if other != c && len(other.awareness) > 0 {
			aware = append(aware, other.awareness)
		}
	}
	self := c.peer()
	r.sendTo(c, &Message{
		Type:     msgInit,
		Seeded:   &seeded,
		Epoch:    r.epoch,
		Seq:      r.lastSeq(),
		Updates:  updates,
		Aware:    aware,
		Peers:    r.peers(),
		Self:     &self,
		Role:     r.roleOf(c),
		Writable: &writable,
		Hash:     r.stored.Hash,
		Stale:    &stale,
		Dirty:    &dirty,
	})
}

func (r *room) roleOf(c *conn) string {
	if r.leader == c {
		return "leader"
	}
	return "follower"
}

func (r *room) leave(c *conn) {
	i := r.indexOf(c)
	if i < 0 {
		return
	}
	r.conns = append(r.conns[:i], r.conns[i+1:]...)
	if c.clientID != 0 {
		r.broadcast(&Message{Type: msgLeave, Clients: []uint64{c.clientID}}, nil)
	}
	if r.seeder == c {
		// The seed never arrived; nothing from it was accepted or relayed, so
		// the next leader can seed from scratch.
		r.seeder = nil
	}
	if r.queuedCheckpoint != nil && r.queuedCheckpoint.conn == c {
		r.queuedCheckpoint = nil
	}
	if r.leader == c {
		r.leader = nil
		r.reconcileTarget = ""
		for _, candidate := range r.conns {
			if candidate.writable {
				r.leader = candidate
				break
			}
		}
		if r.leader != nil {
			dirty := r.dirty()
			r.sendTo(r.leader, &Message{Type: msgRole, Role: "leader", Hash: r.stored.Hash, Dirty: &dirty})
			r.leaderDuties()
		} else {
			r.checkStale()
		}
	}
	r.broadcastPeers(nil)
}

// leaderDuties gives a new leader whatever is outstanding: the seed for an
// unseeded document, or the merge of an external write.
func (r *room) leaderDuties() {
	if r.leader == nil {
		return
	}
	if !r.seeded {
		r.requestSeed()
		return
	}
	r.checkStale()
}

func (r *room) requestSeed() {
	if r.seeded || r.seeder != nil || r.leader == nil {
		return
	}
	r.seeder = r.leader
	r.seedEpoch = newEpoch()
	r.seedContent = r.stored
	r.sendTo(r.seeder, &Message{Type: msgSeedRequest, Markdown: r.seedContent.Markdown, Hash: r.seedContent.Hash, Epoch: r.seedEpoch})
}

func (r *room) message(c *conn, m *Message) {
	if r.indexOf(c) < 0 {
		return
	}
	switch m.Type {
	case msgHello:
		r.hello(c, m)
	case msgSeed:
		r.seed(c, m)
	case msgUpdate:
		r.update(c, m)
	case msgAwareness:
		if len(m.Data) == 0 || len(m.Data) > maxAwarenessBytes {
			return
		}
		c.awareness = m.Data
		r.broadcast(&Message{Type: msgAwareness, Data: m.Data}, c)
	case msgCheckpoint:
		r.checkpoint(c, m)
	case msgCompact:
		r.compact(c, m)
	case msgFlush:
		switch {
		case !r.seeded || (!r.dirty() && !r.stale()):
			// Nothing unsaved: the stored markdown is already current.
			r.sendTo(c, &Message{Type: msgCheckpointed, Hash: r.stored.Hash, Seq: r.checkpointSeq})
		case r.leader != nil:
			r.sendTo(r.leader, &Message{Type: msgCheckpointNow})
		}
	default:
		r.sendTo(c, &Message{Type: msgError, Error: "unknown_message"})
	}
}

func (r *room) hello(c *conn, m *Message) {
	if c.clientID != 0 || m.ClientID == 0 {
		return
	}
	c.clientID = m.ClientID
	r.broadcastPeers(nil)
}

func (r *room) seed(c *conn, m *Message) {
	if c != r.seeder || r.seeded || len(m.Data) == 0 || m.Epoch != r.seedEpoch {
		r.sendTo(c, &Message{Type: msgError, Error: "unexpected_seed"})
		return
	}
	r.seeder = nil
	r.seeded = true
	r.epoch = r.seedEpoch
	r.log = []update{{seq: 1, data: m.Data}}
	r.logBytes = len(m.Data)
	r.nextSeq = 2
	r.checkpointSeq = 1
	r.materializedHash = r.seedContent.Hash
	r.materializedContent = r.seedContent.Markdown
	r.pendingAppends = nil
	seedCopy := m.Data
	content := r.seedContent
	epoch := r.epoch
	pageID := r.backend.OwnerPageID(r.id)
	r.persistQ.push(func(ctx context.Context) error {
		return r.hub.store.seed(ctx, r.key, pageID, epoch, seedCopy, content.Hash, content.Markdown)
	})
	r.sendTo(c, &Message{Type: msgAck, Seq: 1})
	for _, other := range r.conns {
		if other != c {
			r.sendInit(other)
		}
	}
	// The stored page may have changed while the seed was being built.
	r.checkStale()
}

func (r *room) update(c *conn, m *Message) {
	if !c.writable {
		r.sendTo(c, &Message{Type: msgError, Error: "read_only"})
		return
	}
	if !r.seeded || m.Epoch != r.epoch || len(m.Data) == 0 {
		// An update from a document this room no longer holds (a stale epoch
		// or a pre-seed race) would never integrate; the client resyncs.
		r.sendTo(c, &Message{Type: msgError, Error: "stale_epoch"})
		return
	}
	if r.logBytes+len(m.Data) > maxLogBytes {
		r.sendTo(c, &Message{Type: msgError, Error: "document_too_large"})
		if r.leader != nil {
			r.sendTo(r.leader, &Message{Type: msgCompactRequest, Seq: r.lastSeq()})
		}
		return
	}
	u := update{seq: r.nextSeq, data: m.Data}
	r.nextSeq++
	r.log = append(r.log, u)
	r.logBytes += len(u.data)
	r.pendingAppends = append(r.pendingAppends, u)
	r.sendTo(c, &Message{Type: msgAck, Seq: u.seq})
	r.broadcast(&Message{Type: msgUpdate, Data: u.data, Seq: u.seq}, c)
	if !r.compactPending && r.leader != nil && (len(r.log) > compactAfterEntries || r.logBytes > compactAfterBytes) {
		r.compactPending = true
		r.sendTo(r.leader, &Message{Type: msgCompactRequest, Seq: r.lastSeq()})
	}
}

func (r *room) flushAppends() {
	if len(r.pendingAppends) == 0 {
		return
	}
	batch := r.pendingAppends
	r.pendingAppends = nil
	r.persistQ.push(func(ctx context.Context) error {
		return r.hub.store.appendUpdates(ctx, r.key, batch)
	})
}

func (r *room) checkpoint(c *conn, m *Message) {
	if c != r.leader || !r.seeded {
		r.sendTo(c, &Message{Type: msgError, Error: "not_leader"})
		return
	}
	req := &checkpointRequest{conn: c, markdown: m.Markdown, hash: m.Hash, seq: m.Seq}
	if r.checkpointBusy {
		r.queuedCheckpoint = req
		return
	}
	r.startCheckpoint(req)
}

func (r *room) startCheckpoint(req *checkpointRequest) {
	r.checkpointBusy = true
	// Updates the checkpoint covers must be durable before the markdown that
	// includes them; the queue is FIFO.
	r.flushAppends()
	key := r.key
	r.persistQ.push(func(ctx context.Context) error {
		stored, err := r.backend.Checkpoint(ctx, req.conn.userID, r.id, req.markdown, req.hash, func(tx database.Tx, stored Content) error {
			return setMaterializedTx(ctx, tx, key, stored.Hash, stored.Markdown, req.seq)
		})
		r.post(func() { r.checkpointDone(req, stored, err) })
		return nil
	})
}

func (r *room) checkpointDone(req *checkpointRequest, stored Content, err error) {
	r.checkpointBusy = false
	switch {
	case err == nil:
		r.materializedHash = stored.Hash
		r.materializedContent = stored.Markdown
		if req.seq > r.checkpointSeq {
			r.checkpointSeq = req.seq
		}
		if stored.Hash != r.stored.Hash {
			r.wroteCheckpoint = true
			r.lastActor = req.conn.userID
		}
		r.stored = stored
		r.reconcileTarget = ""
		r.broadcast(&Message{Type: msgCheckpointed, Hash: stored.Hash, Seq: req.seq}, nil)
		r.checkStale()
	case errors.Is(err, ErrConflict):
		// Another writer changed the page: merge it before checkpointing again.
		// The leader also learns the current hash in case its base was simply
		// out of date.
		r.refreshStored()
		if r.leader != nil {
			r.sendTo(r.leader, &Message{Type: msgStored, Hash: r.stored.Hash})
		}
	case errors.Is(err, ErrGone):
		r.closeAll("gone")
		return
	default:
		slog.Error("collab: checkpoint", "doc", r.key, "error", err)
		if r.indexOf(req.conn) >= 0 {
			r.sendTo(req.conn, &Message{Type: msgError, Error: "checkpoint_failed"})
		}
	}
	if next := r.queuedCheckpoint; next != nil {
		r.queuedCheckpoint = nil
		if next.conn == r.leader {
			r.startCheckpoint(next)
		}
	}
}

func (r *room) compact(c *conn, m *Message) {
	if c != r.leader || !r.seeded || len(m.Data) == 0 || m.Seq < 1 || m.Seq > r.lastSeq() {
		return
	}
	r.compactPending = false
	upto := m.Seq
	state := m.Data
	kept := []update{{seq: upto, data: state}}
	bytes := len(state)
	for _, u := range r.log {
		if u.seq > upto {
			kept = append(kept, u)
			bytes += len(u.data)
		}
	}
	r.log = kept
	r.logBytes = bytes
	r.flushAppends()
	r.persistQ.push(func(ctx context.Context) error {
		return r.hub.store.compact(ctx, r.key, upto, state)
	})
}

// probe polls the stored document for writes made outside the session: new
// content is merged, a changed title is announced, a gone document closes.
func (r *room) probe() {
	p, err := r.backend.Probe(context.Background(), r.id)
	if errors.Is(err, ErrGone) {
		r.closeAll("gone")
		return
	}
	if err != nil {
		slog.Warn("collab: probe", "doc", r.key, "error", err)
		return
	}
	if p.Meta != r.storedMeta {
		r.storedMeta = p.Meta
		r.broadcast(&Message{Type: msgMeta}, nil)
	}
	// A checkpoint in flight is about to change the hash itself.
	if p.ContentHash != r.stored.Hash && !r.checkpointBusy {
		r.refreshStored()
	}
}

// refreshStored re-reads the stored markdown after an outside write (or a
// refused checkpoint) and decides how to merge it.
func (r *room) refreshStored() {
	stored, err := r.backend.Load(context.Background(), r.id)
	if errors.Is(err, ErrGone) {
		r.closeAll("gone")
		return
	}
	if err != nil {
		slog.Error("collab: reload stored content", "doc", r.key, "error", err)
		return
	}
	changed := stored.Hash != r.stored.Hash
	r.stored = stored
	if changed {
		r.broadcast(&Message{Type: msgStored, Hash: stored.Hash}, nil)
	}
	r.checkStale()
}

// checkStale routes an unmerged outside write: to the leader as a reconcile
// request, or, with no editor present, by resetting a document that has no
// unsaved session edits so it is rebuilt from the new markdown.
func (r *room) checkStale() {
	if !r.stale() {
		if r.staleAnnounced {
			r.staleAnnounced = false
			stale := false
			r.broadcast(&Message{Type: msgState, Stale: &stale}, nil)
		}
		return
	}
	if r.leader != nil {
		if r.reconcileTarget == r.stored.Hash {
			return
		}
		r.reconcileTarget = r.stored.Hash
		r.sendTo(r.leader, &Message{Type: msgReconcile, Base: r.materializedContent, Markdown: r.stored.Markdown, Hash: r.stored.Hash})
		return
	}
	if !r.dirty() {
		r.reset()
		return
	}
	if !r.staleAnnounced {
		r.staleAnnounced = true
		stale := true
		r.broadcast(&Message{Type: msgState, Stale: &stale}, nil)
	}
}

// reset discards the live document so the next editor reseeds it from the
// stored markdown. Only safe when the log holds nothing unmaterialized.
func (r *room) reset() {
	r.seeded = false
	r.epoch = ""
	r.log = nil
	r.logBytes = 0
	r.nextSeq = 1
	r.checkpointSeq = 0
	r.materializedHash = ""
	r.materializedContent = ""
	r.pendingAppends = nil
	r.staleAnnounced = false
	r.reconcileTarget = ""
	r.persistQ.push(func(ctx context.Context) error {
		return r.hub.store.remove(ctx, r.key)
	})
	r.broadcast(&Message{Type: msgReset, Hash: r.stored.Hash}, nil)
}

func (r *room) closeAll(reason string) {
	for _, c := range r.conns {
		r.sendTo(c, &Message{Type: msgError, Error: reason})
		c.kick(reason)
	}
}

// post runs fn on the room goroutine, unless the room is already stopping.
func (r *room) post(fn func()) {
	r.enqueue(event{kind: evPersisted, done: fn})
}

// enqueue delivers an event to the room loop, giving up once the room stops so
// callers never block on a room that is no longer reading.
func (r *room) enqueue(ev event) bool {
	select {
	case r.events <- ev:
		return true
	case <-r.stopping:
		return false
	}
}

// shutdown flushes pending writes, records the session's final revision and
// releases the room.
func (r *room) shutdown() {
	close(r.stopping)
	r.flushAppends()
	if r.wroteCheckpoint {
		actor := r.lastActor
		r.persistQ.push(func(ctx context.Context) error {
			return r.backend.Finalize(ctx, actor, r.id)
		})
	}
	// Deregister only after the writes land: a join arriving meanwhile is
	// refused by admit and waits on done, so the next room for this document
	// always loads the final state.
	r.persistQ.stop()
	r.persistQ.wait()
	r.hub.forget(r)
}

func (r *room) peers() []Peer {
	peers := make([]Peer, 0, len(r.conns))
	for _, c := range r.conns {
		peers = append(peers, c.peer())
	}
	return peers
}

func (r *room) broadcastPeers(except *conn) {
	r.broadcast(&Message{Type: msgPeers, Peers: r.peers()}, except)
}

func (r *room) broadcast(m *Message, except *conn) {
	frame, err := json.Marshal(m)
	if err != nil {
		slog.Error("collab: encode frame", "error", err)
		return
	}
	for _, c := range r.conns {
		if c != except {
			r.deliver(c, frame)
		}
	}
}

func (r *room) sendTo(c *conn, m *Message) {
	frame, err := json.Marshal(m)
	if err != nil {
		slog.Error("collab: encode frame", "error", err)
		return
	}
	r.deliver(c, frame)
}

func (r *room) deliver(c *conn, frame []byte) {
	select {
	case c.send <- frame:
	default:
		c.kick("slow_consumer")
	}
}

func newEpoch() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// persistQueue runs a room's database writes one at a time, in order, off the
// room goroutine.
type persistQueue struct {
	r       *room
	mu      sync.Mutex
	jobs    []func(context.Context) error
	wake    chan struct{}
	stopped bool
	idle    chan struct{}
}

func newPersistQueue(r *room) *persistQueue {
	return &persistQueue{r: r, wake: make(chan struct{}, 1), idle: make(chan struct{})}
}

func (q *persistQueue) push(job func(context.Context) error) {
	q.mu.Lock()
	q.jobs = append(q.jobs, job)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *persistQueue) stop() {
	q.mu.Lock()
	q.stopped = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *persistQueue) wait() { <-q.idle }

func (q *persistQueue) run() {
	defer close(q.idle)
	for {
		q.mu.Lock()
		jobs := q.jobs
		q.jobs = nil
		stopped := q.stopped
		q.mu.Unlock()
		for _, job := range jobs {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := job(ctx); err != nil {
				slog.Error("collab: persist", "doc", q.r.key, "error", err)
			}
			cancel()
		}
		if len(jobs) > 0 {
			continue
		}
		if stopped {
			return
		}
		<-q.wake
	}
}

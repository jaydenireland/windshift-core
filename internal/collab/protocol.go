// Package collab hosts live co-editing sessions for markdown documents.
//
// Browsers own the CRDT: each runs Yjs and binds it to the editor. The server
// is a relay with a durable, opaque update log. It never decodes Yjs bytes; it
// orders updates, persists them, and coordinates the three jobs that need one
// client to act for everyone:
//
//   - Seeding. A document starts empty. The server asks exactly one editor
//     (the leader) to build it from the stored markdown, so concurrent joiners
//     cannot each insert a copy.
//   - Checkpointing. The leader periodically serializes the document to
//     markdown and the server stores it through the owning service, guarded by
//     the stored content hash. Markdown stays the durable record that search,
//     the API, agents and the portal read.
//   - Reconciling. A write made outside the session (API, CLI, agent tools,
//     diagrams, revision restore) changes the stored hash. The leader receives
//     the previous and new markdown, three-way merges the change into the live
//     document, and checkpoints the result, so the edit appears live for
//     everyone without discarding what people are typing.
//
// Rooms are in-process, matching the SSE hub: one server process owns every
// session.
package collab

import (
	"context"
	"errors"

	"windshift/internal/database"
)

// Access is a user's access to a document.
type Access int

const (
	AccessNone Access = iota
	AccessView
	AccessEdit
)

// Content is a document's stored markdown and its content hash.
type Content struct {
	Markdown string
	Hash     string
}

var (
	// ErrConflict reports that the stored hash no longer matches the hash a
	// checkpoint was based on.
	ErrConflict = errors.New("collab: stored content changed")
	// ErrGone reports that the document no longer exists or can no longer be
	// edited (deleted or archived).
	ErrGone = errors.New("collab: document gone")
)

// Probe is a cheap fingerprint of a document's stored state. Open rooms poll
// it to notice writes made outside the session (API, agents, diagram saves,
// restores, renames, archiving) without hooks in the owning service.
type Probe struct {
	// ContentHash is the stored markdown's hash.
	ContentHash string
	// Meta fingerprints what surrounds the body (a page title), so clients
	// can refresh it when someone else changes it.
	Meta string
}

// Backend adapts one kind of markdown document (for example a knowledge page)
// to the hub.
type Backend interface {
	// Kind is the document kind in URLs and keys, such as "page".
	Kind() string
	// Authorize resolves the user's access. It must return AccessNone rather
	// than an error for documents the user cannot see.
	Authorize(ctx context.Context, userID, id int) (Access, error)
	// Load returns the stored markdown, or ErrGone.
	Load(ctx context.Context, id int) (Content, error)
	// Probe returns the stored state's fingerprint, or ErrGone.
	Probe(ctx context.Context, id int) (Probe, error)
	// Checkpoint stores markdown if the stored hash still equals expectedHash
	// (ErrConflict otherwise) and calls afterWrite inside the same
	// transaction with what was stored.
	Checkpoint(ctx context.Context, actorID, id int, markdown, expectedHash string, afterWrite func(tx database.Tx, stored Content) error) (Content, error)
	// Finalize runs when a session ends after storing checkpoints.
	Finalize(ctx context.Context, actorID, id int) error
	// OwnerPageID returns the page whose deletion cascades to the document's
	// collab state, or nil for documents not owned by a page.
	OwnerPageID(id int) *int
}

// Message is the JSON frame exchanged with clients. Binary payloads (Yjs
// updates, awareness updates, compacted state) are base64 in Data.
type Message struct {
	Type string `json:"t"`

	// Yjs payload for update, seed, awareness and compact.
	Data []byte `json:"u,omitempty"`
	// Log position: an update's sequence, an ack, or the highest sequence a
	// checkpoint or compaction covers.
	Seq int64 `json:"seq,omitempty"`

	// Markdown for seed requests, checkpoints and reconcile requests.
	Markdown string `json:"md,omitempty"`
	// Base markdown for a reconcile request.
	Base string `json:"base,omitempty"`
	// Stored content hash (seed, checkpoint base, reconcile target).
	Hash string `json:"hash,omitempty"`

	// Hello: the client's Yjs client id (its awareness id).
	ClientID uint64 `json:"client_id,omitempty"`
	// Hello: the epoch of the document the client already holds.
	Epoch string `json:"epoch,omitempty"`

	// Init / state fields.
	Seeded   *bool    `json:"seeded,omitempty"`
	Role     string   `json:"role,omitempty"`
	Writable *bool    `json:"writable,omitempty"`
	Updates  [][]byte `json:"updates,omitempty"`
	Aware    [][]byte `json:"awareness,omitempty"`
	Peers    []Peer   `json:"peers,omitempty"`
	Self     *Peer    `json:"self,omitempty"`
	Clients  []uint64 `json:"clients,omitempty"`
	Stale    *bool    `json:"stale,omitempty"`
	Dirty    *bool    `json:"dirty,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Peer is one connected participant, as the server knows them. Clients use it
// for presence and to label remote cursors by awareness client id.
type Peer struct {
	ConnID   string `json:"conn_id"`
	UserID   int    `json:"user_id"`
	Name     string `json:"name"`
	Avatar   string `json:"avatar_url,omitempty"`
	ClientID uint64 `json:"client_id,omitempty"`
	Writable bool   `json:"writable"`
}

// Client → server message types.
const (
	msgHello      = "hello"
	msgUpdate     = "update"
	msgSeed       = "seed"
	msgAwareness  = "awareness"
	msgCheckpoint = "checkpoint"
	msgCompact    = "compact"
	msgFlush      = "flush"
)

// Server → client message types.
const (
	msgInit           = "init"
	msgAck            = "ack"
	msgSeedRequest    = "seed_request"
	msgRole           = "role"
	msgReconcile      = "reconcile"
	msgCheckpointed   = "checkpointed"
	msgStored         = "stored"
	msgPeers          = "peers"
	msgLeave          = "leave"
	msgState          = "state"
	msgReset          = "reset"
	msgCompactRequest = "compact_request"
	msgCheckpointNow  = "checkpoint_request"
	msgMeta           = "meta"
	msgError          = "error"
)

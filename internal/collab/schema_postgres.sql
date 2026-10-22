-- Live co-editing (Yjs) document state, owned by internal/collab and created
-- by collab.Install (CREATE ... IF NOT EXISTS), not by the main migration
-- catalog, so a fork carrying co-editing never edits upstream schema files.
--
-- The update log is opaque Yjs bytes; browsers own the CRDT. The owning row's
-- markdown (pages.content) stays the durable record: materialized_* mirror the
-- markdown the session last stored there, so a stored hash that differs from
-- materialized_hash marks a write made outside the session.
CREATE TABLE IF NOT EXISTS collab_documents (
    doc_key TEXT PRIMARY KEY,
    page_id BIGINT,
    seeded BOOLEAN NOT NULL DEFAULT FALSE,
    epoch TEXT NOT NULL DEFAULT '',
    materialized_hash TEXT NOT NULL DEFAULT '',
    materialized_content TEXT NOT NULL DEFAULT '',
    next_seq BIGINT NOT NULL DEFAULT 1,
    checkpoint_seq BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_collab_documents_page ON collab_documents(page_id);

-- Append-only Yjs update log. Compaction replaces a prefix of the log with one
-- merged state row carrying the highest sequence it covers.
CREATE TABLE IF NOT EXISTS collab_updates (
    doc_key TEXT NOT NULL,
    seq BIGINT NOT NULL,
    payload BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (doc_key, seq),
    FOREIGN KEY (doc_key) REFERENCES collab_documents(doc_key) ON DELETE CASCADE
);

-- Bursts of live edits waiting to be recorded, for document kinds whose
-- history and notifications fire once edits go quiet (collab.Settler). A
-- checkpoint extends the document's open burst; the hub settles a burst once
-- it is closed (an outside write interrupted it) or quiet long enough.
-- Timestamps are Unix milliseconds.
CREATE TABLE IF NOT EXISTS collab_pending_settlements (
    id BIGSERIAL PRIMARY KEY,
    doc_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    doc_id BIGINT NOT NULL,
    before_content TEXT NOT NULL,
    before_hash TEXT NOT NULL,
    after_content TEXT NOT NULL,
    after_hash TEXT NOT NULL,
    editors TEXT NOT NULL DEFAULT '',
    closed BOOLEAN NOT NULL DEFAULT FALSE,
    version BIGINT NOT NULL DEFAULT 1,
    last_write_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_collab_pending_settlements_doc ON collab_pending_settlements(doc_key, closed);
CREATE INDEX IF NOT EXISTS idx_collab_pending_settlements_due ON collab_pending_settlements(kind, last_write_at);

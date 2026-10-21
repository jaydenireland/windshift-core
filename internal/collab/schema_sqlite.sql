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
    page_id INTEGER,
    seeded BOOLEAN NOT NULL DEFAULT FALSE,
    epoch TEXT NOT NULL DEFAULT '',
    materialized_hash TEXT NOT NULL DEFAULT '',
    materialized_content TEXT NOT NULL DEFAULT '',
    next_seq INTEGER NOT NULL DEFAULT 1,
    checkpoint_seq INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (page_id) REFERENCES pages(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_collab_documents_page ON collab_documents(page_id);

-- Append-only Yjs update log. Compaction replaces a prefix of the log with one
-- merged state row carrying the highest sequence it covers.
CREATE TABLE IF NOT EXISTS collab_updates (
    doc_key TEXT NOT NULL,
    seq INTEGER NOT NULL,
    payload BLOB NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (doc_key, seq),
    FOREIGN KEY (doc_key) REFERENCES collab_documents(doc_key) ON DELETE CASCADE
);

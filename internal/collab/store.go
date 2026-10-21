package collab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"windshift/internal/database"
)

// docState is the persisted bookkeeping for one co-edited document.
type docState struct {
	exists              bool
	seeded              bool
	epoch               string
	materializedHash    string
	materializedContent string
	nextSeq             int64
	checkpointSeq       int64
}

// update is one opaque Yjs update (or a compacted state) at a log position.
type update struct {
	seq  int64
	data []byte
}

// Store persists co-editing documents. Payloads are opaque Yjs bytes; the
// store never decodes them.
type Store struct {
	db database.Database
}

// NewStore returns a store over db.
func NewStore(db database.Database) *Store {
	return &Store{db: db}
}

func (s *Store) load(ctx context.Context, key string) (docState, []update, error) {
	var st docState
	err := s.db.QueryRowContext(ctx, `
		SELECT seeded, epoch, materialized_hash, materialized_content, next_seq, checkpoint_seq
		FROM collab_documents WHERE doc_key = ?`, key,
	).Scan(&st.seeded, &st.epoch, &st.materializedHash, &st.materializedContent, &st.nextSeq, &st.checkpointSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return docState{nextSeq: 1}, nil, nil
	}
	if err != nil {
		return docState{}, nil, fmt.Errorf("load collab document %s: %w", key, err)
	}
	st.exists = true
	rows, err := s.db.QueryContext(ctx, `SELECT seq, payload FROM collab_updates WHERE doc_key = ? ORDER BY seq`, key)
	if err != nil {
		return docState{}, nil, fmt.Errorf("load collab updates %s: %w", key, err)
	}
	defer func() { _ = rows.Close() }()
	var updates []update
	for rows.Next() {
		var u update
		if err := rows.Scan(&u.seq, &u.data); err != nil {
			return docState{}, nil, fmt.Errorf("scan collab update %s: %w", key, err)
		}
		updates = append(updates, u)
	}
	if err := rows.Err(); err != nil {
		return docState{}, nil, fmt.Errorf("iterate collab updates %s: %w", key, err)
	}
	return st, updates, nil
}

// seed replaces any previous state of the document with a freshly seeded one
// whose first log entry is the seed update.
func (s *Store) seed(ctx context.Context, key string, pageID *int, epoch string, data []byte, hash, content string) error {
	return database.WithTx(s.db, func(tx database.Tx) error {
		if err := deleteTx(ctx, tx, key); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO collab_documents
				(doc_key, page_id, seeded, epoch, materialized_hash, materialized_content, next_seq, checkpoint_seq, updated_at)
			VALUES (?, ?, TRUE, ?, ?, ?, 2, 1, CURRENT_TIMESTAMP)`,
			key, pageID, epoch, hash, content,
		); err != nil {
			return fmt.Errorf("insert collab document %s: %w", key, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO collab_updates (doc_key, seq, payload) VALUES (?, 1, ?)`, key, data); err != nil {
			return fmt.Errorf("insert collab seed %s: %w", key, err)
		}
		return nil
	})
}

// appendUpdates persists a batch of log entries in order.
func (s *Store) appendUpdates(ctx context.Context, key string, updates []update) error {
	if len(updates) == 0 {
		return nil
	}
	return database.WithTx(s.db, func(tx database.Tx) error {
		for _, u := range updates {
			if _, err := tx.ExecContext(ctx, `INSERT INTO collab_updates (doc_key, seq, payload) VALUES (?, ?, ?)`, key, u.seq, u.data); err != nil {
				return fmt.Errorf("append collab update %s/%d: %w", key, u.seq, err)
			}
		}
		last := updates[len(updates)-1].seq
		if _, err := tx.ExecContext(ctx, `UPDATE collab_documents SET next_seq = ?, updated_at = CURRENT_TIMESTAMP WHERE doc_key = ?`, last+1, key); err != nil {
			return fmt.Errorf("advance collab sequence %s: %w", key, err)
		}
		return nil
	})
}

// setMaterializedTx records the markdown a checkpoint stored, inside the page
// write transaction.
func setMaterializedTx(ctx context.Context, tx database.Tx, key, hash, content string, checkpointSeq int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE collab_documents
		SET materialized_hash = ?, materialized_content = ?,
		    checkpoint_seq = CASE WHEN checkpoint_seq > ? THEN checkpoint_seq ELSE ? END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE doc_key = ?`, hash, content, checkpointSeq, checkpointSeq, key,
	); err != nil {
		return fmt.Errorf("record collab checkpoint %s: %w", key, err)
	}
	return nil
}

// compact replaces every log entry up to and including upto with state, which
// the client built from a document that had applied all of them.
func (s *Store) compact(ctx context.Context, key string, upto int64, state []byte) error {
	return database.WithTx(s.db, func(tx database.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM collab_updates WHERE doc_key = ? AND seq <= ?`, key, upto); err != nil {
			return fmt.Errorf("compact collab updates %s: %w", key, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO collab_updates (doc_key, seq, payload) VALUES (?, ?, ?)`, key, upto, state); err != nil {
			return fmt.Errorf("insert compacted collab state %s: %w", key, err)
		}
		return nil
	})
}

// remove deletes the document and its log.
func (s *Store) remove(ctx context.Context, key string) error {
	return database.WithTx(s.db, func(tx database.Tx) error {
		return deleteTx(ctx, tx, key)
	})
}

func deleteTx(ctx context.Context, tx database.Tx, key string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM collab_updates WHERE doc_key = ?`, key); err != nil {
		return fmt.Errorf("delete collab updates %s: %w", key, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM collab_documents WHERE doc_key = ?`, key); err != nil {
		return fmt.Errorf("delete collab document %s: %w", key, err)
	}
	return nil
}

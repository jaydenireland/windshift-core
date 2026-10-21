package collab

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"windshift/internal/database"
)

// Settler is an optional Backend extension for documents whose history and
// notifications are recorded once live edits go quiet, instead of on every
// checkpoint. Checkpoints then store content silently, and the hub records
// each burst of edits as one change.
type Settler interface {
	// SettleAfter is how long a document must go without session writes
	// before its burst of edits is settled.
	SettleAfter() time.Duration
	// Settle records the net change of one burst. It returns ErrGone when
	// the document no longer exists; the burst is then dropped.
	Settle(ctx context.Context, id int, s Settlement) error
}

// Settlement is the net change a burst of live edits made.
type Settlement struct {
	// Before is the stored content when the burst began; After is what the
	// session last stored. Writes made outside the session never fall inside
	// a burst: one closes the burst before it.
	Before Content
	After  Content
	// Editors are the users who typed during the burst, most recent last.
	Editors []int
}

// Pruner is an optional Backend extension for kinds whose co-editing state no
// foreign key removes when the document is deleted.
type Pruner interface {
	// Missing returns the ids among ids whose documents no longer exist.
	Missing(ctx context.Context, ids []int) ([]int, error)
}

const (
	// settleScanInterval bounds how late a due burst is settled.
	settleScanInterval = 10 * time.Second
	// pruneInterval is how often state of deleted documents is removed.
	pruneInterval = time.Hour
)

// pendingBurst is the open burst of a document, as stored.
type pendingBurst struct {
	id        int64
	afterHash string
	editors   []int
	version   int64
}

// recordBurstTx extends the document's open burst with a checkpoint that
// stored after over base, inside the checkpoint's transaction. A burst whose
// last write is not base was interrupted by an outside write; it is closed so
// it settles on its own, and a new burst starts from base.
func recordBurstTx(ctx context.Context, tx database.Tx, kind string, docID int, key string, base, after Content, editors []int, now time.Time) error {
	open, err := openBurstTx(ctx, tx, key)
	if err != nil {
		return err
	}
	ms := now.UnixMilli()
	if open != nil && open.afterHash == base.Hash {
		res, err := tx.ExecContext(ctx, `
			UPDATE collab_pending_settlements
			SET after_content = ?, after_hash = ?, editors = ?, version = version + 1, last_write_at = ?
			WHERE id = ? AND version = ?`,
			after.Markdown, after.Hash, formatEditors(mergeEditors(open.editors, editors)), ms, open.id, open.version)
		if err != nil {
			return fmt.Errorf("extend collab burst %s: %w", key, err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 1 {
			return nil
		}
		// The scanner claimed the burst meanwhile; start the next one.
	} else if open != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE collab_pending_settlements SET closed = TRUE WHERE id = ?`, open.id); err != nil {
			return fmt.Errorf("close collab burst %s: %w", key, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO collab_pending_settlements
			(doc_key, kind, doc_id, before_content, before_hash, after_content, after_hash, editors, closed, version, last_write_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, FALSE, 1, ?)`,
		key, kind, docID, base.Markdown, base.Hash, after.Markdown, after.Hash, formatEditors(editors), ms,
	); err != nil {
		return fmt.Errorf("open collab burst %s: %w", key, err)
	}
	return nil
}

func openBurstTx(ctx context.Context, tx database.Tx, key string) (*pendingBurst, error) {
	var b pendingBurst
	var editors string
	err := tx.QueryRowContext(ctx, `
		SELECT id, after_hash, editors, version FROM collab_pending_settlements
		WHERE doc_key = ? AND closed = FALSE ORDER BY id DESC LIMIT 1`, key,
	).Scan(&b.id, &b.afterHash, &editors, &b.version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read collab burst %s: %w", key, err)
	}
	b.editors = parseEditors(editors)
	return &b, nil
}

// mergeEditors appends recent to editors, keeping each user once at their
// most recent position.
func mergeEditors(editors, recent []int) []int {
	out := make([]int, 0, len(editors)+len(recent))
	seen := make(map[int]bool, len(editors)+len(recent))
	all := append(append([]int{}, editors...), recent...)
	for i := len(all) - 1; i >= 0; i-- {
		if !seen[all[i]] {
			seen[all[i]] = true
			out = append(out, all[i])
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func formatEditors(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

func parseEditors(s string) []int {
	var ids []int
	for _, part := range strings.Split(s, ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// dueBurst is a burst ready to settle.
type dueBurst struct {
	id      int64
	kind    string
	docID   int
	version int64
	s       Settlement
}

// runSettler settles due bursts until stop closes. Bursts are durable, so
// ones pending at shutdown settle after the next start.
func (h *Hub) runSettler(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(h.settleInterval)
	defer ticker.Stop()
	var pruned time.Time
	for {
		now := time.Now()
		h.settleDue(context.Background(), now)
		if now.Sub(pruned) >= pruneInterval {
			pruned = now
			h.prune(context.Background())
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

// settleDue settles every closed burst and every open burst quiet for its
// backend's SettleAfter.
func (h *Hub) settleDue(ctx context.Context, now time.Time) {
	for kind, b := range h.backends {
		settler, ok := b.(Settler)
		if !ok {
			continue
		}
		due, err := h.store.dueBursts(ctx, kind, now.Add(-settler.SettleAfter()))
		if err != nil {
			slog.Error("collab: list due edits", "kind", kind, "error", err)
			continue
		}
		for _, d := range due {
			claimed, err := h.store.claimBurst(ctx, d.id, d.version)
			if err != nil {
				slog.Error("collab: claim due edits", "kind", kind, "id", d.docID, "error", err)
				continue
			}
			if !claimed || d.s.Before.Hash == d.s.After.Hash {
				continue
			}
			if err := settler.Settle(ctx, d.docID, d.s); err != nil && !errors.Is(err, ErrGone) {
				slog.Error("collab: record settled edits", "kind", kind, "id", d.docID, "error", err)
			}
		}
	}
}

func (s *Store) dueBursts(ctx context.Context, kind string, quietSince time.Time) ([]dueBurst, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, doc_id, version, before_content, before_hash, after_content, after_hash, editors
		FROM collab_pending_settlements
		WHERE kind = ? AND (closed = TRUE OR last_write_at <= ?)
		ORDER BY id`, kind, quietSince.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query due collab bursts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var due []dueBurst
	for rows.Next() {
		d := dueBurst{kind: kind}
		var editors string
		if err := rows.Scan(&d.id, &d.docID, &d.version, &d.s.Before.Markdown, &d.s.Before.Hash, &d.s.After.Markdown, &d.s.After.Hash, &editors); err != nil {
			return nil, fmt.Errorf("scan due collab burst: %w", err)
		}
		d.s.Editors = parseEditors(editors)
		due = append(due, d)
	}
	return due, rows.Err()
}

// claimBurst deletes a burst unless a checkpoint extended it since it was
// read, so each burst settles exactly once.
func (s *Store) claimBurst(ctx context.Context, id, version int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM collab_pending_settlements WHERE id = ? AND version = ?`, id, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// prune removes co-editing state of deleted documents for backends
// implementing Pruner. Open rooms are skipped; they notice deletion
// themselves.
func (h *Hub) prune(ctx context.Context) {
	for kind, b := range h.backends {
		pruner, ok := b.(Pruner)
		if !ok {
			continue
		}
		ids, err := h.store.documentIDs(ctx, kind)
		if err != nil {
			slog.Error("collab: list documents", "kind", kind, "error", err)
			continue
		}
		if len(ids) == 0 {
			continue
		}
		missing, err := pruner.Missing(ctx, ids)
		if err != nil {
			slog.Error("collab: find deleted documents", "kind", kind, "error", err)
			continue
		}
		for _, id := range missing {
			key := docKey(kind, id)
			h.mu.Lock()
			open := h.rooms[key] != nil
			h.mu.Unlock()
			if open {
				continue
			}
			if err := h.store.remove(ctx, key); err != nil {
				slog.Error("collab: remove deleted document", "doc", key, "error", err)
			}
		}
	}
}

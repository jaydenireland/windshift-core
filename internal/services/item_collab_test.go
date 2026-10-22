package services

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"windshift/internal/database"
	"windshift/internal/models"
)

type recordingItemEmitter struct {
	calls []struct {
		original, updated *models.Item
		actor             int
		history           []HistoryEntry
	}
}

func (e *recordingItemEmitter) EmitItemUpdated(original, updated *models.Item, _, _ bool, actor int, history []HistoryEntry, _ ...string) {
	e.calls = append(e.calls, struct {
		original, updated *models.Item
		actor             int
		history           []HistoryEntry
	}{original, updated, actor, history})
}

func newItemCollabTestService(t *testing.T) (*ItemUpdateApplicationService, *recordingItemEmitter, database.Database) {
	t.Helper()
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "items.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users (id, email, username, first_name, last_name) VALUES (7, 'ada@example.test', 'ada', 'Ada', 'L'), (8, 'grace@example.test', 'grace', 'Grace', 'H')`,
		`INSERT INTO workspaces (id, name, key) VALUES (42, 'Work', 'WRK')`,
		`INSERT INTO items (id, workspace_id, workspace_item_number, title, description, frac_index, status_id, priority_id) VALUES (5, 42, 1, 'Ship it', 'v1', 'a0', (SELECT id FROM statuses LIMIT 1), (SELECT id FROM priorities LIMIT 1))`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	svc := NewItemUpdateApplicationService(db, nil)
	emitter := &recordingItemEmitter{}
	svc.SetEmitter(emitter)
	return svc, emitter, db
}

func countRows(t *testing.T, db database.Database, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCollabDescriptionCheckpointsAreSilentUntilSettled(t *testing.T) {
	svc, emitter, db := newItemCollabTestService(t)
	ctx := context.Background()
	history := `SELECT COUNT(*) FROM item_history WHERE item_id = 5`
	events := `SELECT COUNT(*) FROM domain_events WHERE aggregate_id = '5' AND event_type = 'item.updated'`
	baseEvents := countRows(t, db, events)

	afterWrites := 0
	stored, err := svc.CollabCheckpointDescription(ctx, ItemCollabCheckpointInput{
		ItemID: 5, Description: "v2", ExpectedHash: ItemDescriptionHash("v1"),
		AfterWrite: func(database.Tx, string) error { afterWrites++; return nil },
	})
	if err != nil || stored != "v2" || afterWrites != 1 {
		t.Fatalf("checkpoint = %q, %v (after writes %d)", stored, err, afterWrites)
	}
	if _, err := svc.CollabCheckpointDescription(ctx, ItemCollabCheckpointInput{ItemID: 5, Description: "v3", ExpectedHash: ItemDescriptionHash("v2")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.CollabDescription(ctx, 5); got != "v3" {
		t.Fatalf("stored description = %q", got)
	}
	if n := countRows(t, db, history); n != 0 {
		t.Fatalf("checkpoints recorded %d history rows", n)
	}
	if n := countRows(t, db, events); n != baseEvents {
		t.Fatalf("checkpoints appended %d item.updated events", n-baseEvents)
	}
	if len(emitter.calls) != 0 {
		t.Fatalf("checkpoints emitted %d updates", len(emitter.calls))
	}

	// A checkpoint on an out-of-date base is refused and changes nothing.
	_, err = svc.CollabCheckpointDescription(ctx, ItemCollabCheckpointInput{
		ItemID: 5, Description: "stale", ExpectedHash: ItemDescriptionHash("v1"),
		AfterWrite: func(database.Tx, string) error { afterWrites++; return nil },
	})
	if !errors.Is(err, ErrItemDescriptionConflict) || afterWrites != 1 {
		t.Fatalf("stale checkpoint err = %v (after writes %d)", err, afterWrites)
	}

	// Settling records the burst once, attributed to the most recent editor.
	if err := svc.SettleCollabDescription(ctx, ItemDescriptionSettlement{ItemID: 5, Before: "v1", After: "v3", Editors: []int{7, 8}}); err != nil {
		t.Fatal(err)
	}
	var user int
	var oldValue, newValue string
	if err := db.QueryRow(`SELECT user_id, old_value, new_value FROM item_history WHERE item_id = 5 AND field_name = 'description'`).Scan(&user, &oldValue, &newValue); err != nil {
		t.Fatal(err)
	}
	if user != 8 || oldValue != "v1" || newValue != "v3" {
		t.Fatalf("history = %d %q→%q", user, oldValue, newValue)
	}
	if n := countRows(t, db, events); n != baseEvents+1 {
		t.Fatalf("settle appended %d item.updated events, want 1", n-baseEvents)
	}
	if len(emitter.calls) != 1 || emitter.calls[0].actor != 8 ||
		emitter.calls[0].original.Description != "v1" || emitter.calls[0].updated.Description != "v3" {
		t.Fatalf("emitted = %+v", emitter.calls)
	}

	// An unchanged burst records nothing.
	if err := svc.SettleCollabDescription(ctx, ItemDescriptionSettlement{ItemID: 5, Before: "v3", After: "v3", Editors: []int{7}}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, history); n != 1 {
		t.Fatalf("history rows = %d", n)
	}
}

func TestCollabDescriptionRefusesMergedAndDeletedItems(t *testing.T) {
	svc, _, db := newItemCollabTestService(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO items (id, workspace_id, workspace_item_number, title, description, frac_index, status_id, priority_id) VALUES (6, 42, 2, 'Survivor', '', 'a1', (SELECT id FROM statuses LIMIT 1), (SELECT id FROM priorities LIMIT 1))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE items SET merged_into_item_id = 6 WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CollabDescription(ctx, 5); !errors.Is(err, ErrItemCollabGone) {
		t.Fatalf("merged load err = %v", err)
	}
	if _, err := svc.CollabCheckpointDescription(ctx, ItemCollabCheckpointInput{ItemID: 5, Description: "x", ExpectedHash: ItemDescriptionHash("v1")}); !errors.Is(err, ErrItemCollabGone) {
		t.Fatalf("merged checkpoint err = %v", err)
	}
	if _, err := svc.CollabDescription(ctx, 99); !errors.Is(err, ErrItemCollabGone) {
		t.Fatalf("missing load err = %v", err)
	}
	if err := svc.SettleCollabDescription(ctx, ItemDescriptionSettlement{ItemID: 99, Before: "a", After: "b", Editors: []int{7}}); !errors.Is(err, ErrItemCollabGone) {
		t.Fatalf("missing settle err = %v", err)
	}
}

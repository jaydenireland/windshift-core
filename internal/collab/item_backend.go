package collab

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"windshift/internal/database"
	"windshift/internal/services"
)

// ItemSettleAfter is how long an item description must go without live edits
// before the burst is recorded: one history entry, notifications, automations,
// webhooks and @mentions, as if the description had been saved once.
const ItemSettleAfter = 2 * time.Minute

// ItemBackend co-edits work-item descriptions. Checkpoints store the text
// silently; history and notifications wait until the edits settle.
type ItemBackend struct {
	db    database.Database
	items *services.ItemUpdateApplicationService
}

// NewItemBackend returns the item-description backend.
func NewItemBackend(db database.Database, items *services.ItemUpdateApplicationService) *ItemBackend {
	return &ItemBackend{db: db, items: items}
}

// Kind implements Backend.
func (b *ItemBackend) Kind() string { return "item" }

// OwnerPageID implements Backend. Items are hard-deleted without a cascade to
// co-editing state; Missing lets the hub prune it.
func (b *ItemBackend) OwnerPageID(int) *int { return nil }

// Authorize implements Backend with the item update pipeline's checks.
func (b *ItemBackend) Authorize(ctx context.Context, userID, id int) (Access, error) {
	canView, canEdit, err := b.items.CollabItemAccess(ctx, userID, id)
	switch {
	case err != nil:
		return AccessNone, err
	case canEdit:
		return AccessEdit, nil
	case canView:
		return AccessView, nil
	}
	return AccessNone, nil
}

// Load implements Backend.
func (b *ItemBackend) Load(ctx context.Context, id int) (Content, error) {
	description, err := b.items.CollabDescription(ctx, id)
	if errors.Is(err, services.ErrItemCollabGone) {
		return Content{}, ErrGone
	}
	if err != nil {
		return Content{}, err
	}
	return Content{Markdown: description, Hash: services.ItemDescriptionHash(description)}, nil
}

// Probe implements Backend with one primary-key read. Items store no content
// hash, so the description is read and hashed.
func (b *ItemBackend) Probe(ctx context.Context, id int) (Probe, error) {
	var description sql.NullString
	var merged sql.NullInt64
	err := b.db.QueryRowContext(ctx, `SELECT description, merged_into_item_id FROM items WHERE id = ?`, id).Scan(&description, &merged)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && merged.Valid) {
		return Probe{}, ErrGone
	}
	if err != nil {
		return Probe{}, err
	}
	return Probe{ContentHash: services.ItemDescriptionHash(description.String)}, nil
}

// Checkpoint implements Backend.
func (b *ItemBackend) Checkpoint(ctx context.Context, _, id int, markdown, expectedHash string, afterWrite func(tx database.Tx, stored Content) error) (Content, error) {
	stored, err := b.items.CollabCheckpointDescription(ctx, services.ItemCollabCheckpointInput{
		ItemID:       id,
		Description:  markdown,
		ExpectedHash: expectedHash,
		AfterWrite: func(tx database.Tx, description string) error {
			return afterWrite(tx, Content{Markdown: description, Hash: services.ItemDescriptionHash(description)})
		},
	})
	switch {
	case errors.Is(err, services.ErrItemDescriptionConflict):
		return Content{}, ErrConflict
	case errors.Is(err, services.ErrItemCollabGone):
		return Content{}, ErrGone
	case err != nil:
		return Content{}, err
	}
	return Content{Markdown: stored, Hash: services.ItemDescriptionHash(stored)}, nil
}

// Finalize implements Backend. Settling records the history instead.
func (b *ItemBackend) Finalize(context.Context, int, int) error { return nil }

// SettleAfter implements Settler.
func (b *ItemBackend) SettleAfter() time.Duration { return ItemSettleAfter }

// Settle implements Settler.
func (b *ItemBackend) Settle(ctx context.Context, id int, s Settlement) error {
	err := b.items.SettleCollabDescription(ctx, services.ItemDescriptionSettlement{
		ItemID:  id,
		Before:  s.Before.Markdown,
		After:   s.After.Markdown,
		Editors: s.Editors,
	})
	if errors.Is(err, services.ErrItemCollabGone) {
		return ErrGone
	}
	return err
}

// Missing implements Pruner.
func (b *ItemBackend) Missing(ctx context.Context, ids []int) ([]int, error) {
	var missing []int
	for _, id := range ids {
		var one int
		err := b.db.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ?`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			missing = append(missing, id)
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return missing, nil
}

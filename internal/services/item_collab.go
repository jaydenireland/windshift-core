package services

// Live co-editing support for item descriptions (internal/collab). Like
// page_collab.go, this file changes none of the existing item code so it can
// be carried on a fork without touching upstream files.
//
// A live session stores the description every few seconds without history,
// domain events, notifications or webhooks (CollabCheckpointDescription). Once
// the edits go quiet, the session's net change is recorded as one ordinary
// description update (SettleCollabDescription).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"windshift/internal/database"
	"windshift/internal/itemevents"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/validation"
)

var (
	// ErrItemCollabGone reports an item that no longer exists or was merged
	// into another item, so its description can no longer be edited.
	ErrItemCollabGone = errors.New("item deleted or merged")
	// ErrItemDescriptionConflict reports that the stored description changed
	// since the hash a checkpoint was based on.
	ErrItemDescriptionConflict = errors.New("item description changed")
)

// ItemDescriptionHash fingerprints a description for co-editing's conflict
// checks. Items store no content hash, so it is computed from the text.
func ItemDescriptionHash(description string) string {
	return contentHash(description)
}

// CollabItemAccess reports whether the user may view and edit the item's
// description. Merged items are read-only.
func (s *ItemUpdateApplicationService) CollabItemAccess(ctx context.Context, userID, itemID int) (canView, canEdit bool, err error) {
	item, err := s.update.FindItem(itemID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && item == nil) {
		return false, false, nil
	}
	if err != nil || s.permission == nil {
		return false, false, err
	}
	canView, err = s.permission.HasWorkspacePermission(userID, item.WorkspaceID, models.PermissionItemView)
	if err != nil {
		return false, false, err
	}
	canEdit, err = s.permission.HasWorkspacePermission(userID, item.WorkspaceID, models.PermissionItemEdit)
	if err != nil || !canEdit {
		return canView || canEdit, false, err
	}
	merged, err := repository.NewItemRepository(s.update.db).MergedIntoItemID(ctx, itemID)
	if err != nil {
		return false, false, err
	}
	return true, merged == nil, nil
}

// CollabDescription returns the stored description of an editable item.
func (s *ItemUpdateApplicationService) CollabDescription(ctx context.Context, itemID int) (string, error) {
	items := repository.NewItemRepository(s.update.db)
	merged, err := items.MergedIntoItemID(ctx, itemID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && merged != nil) {
		return "", ErrItemCollabGone
	}
	if err != nil {
		return "", err
	}
	item, err := items.FindByID(itemID)
	if errors.Is(err, repository.ErrNotFound) {
		return "", ErrItemCollabGone
	}
	if err != nil {
		return "", err
	}
	return item.Description, nil
}

// ItemCollabCheckpointInput is one markdown checkpoint from a live session.
type ItemCollabCheckpointInput struct {
	ItemID      int
	Description string
	// ExpectedHash is the ItemDescriptionHash the session last stored or
	// merged; a different stored description is refused so it can be merged
	// first.
	ExpectedHash string
	// AfterWrite runs inside the item transaction with the stored
	// description, so the session's bookkeeping commits with it.
	AfterWrite func(tx database.Tx, description string) error
}

// CollabCheckpointDescription stores a live session's description silently:
// open item views refresh, but no history, domain event, notification,
// automation or webhook is produced until SettleCollabDescription.
func (s *ItemUpdateApplicationService) CollabCheckpointDescription(ctx context.Context, in ItemCollabCheckpointInput) (string, error) {
	if err := validation.ValidateMarkdownSource("description", in.Description, validation.MarkdownMaxBytes, false); err != nil {
		return "", err
	}
	items := repository.NewItemRepository(s.update.db)
	var item *models.Item
	err := database.WithTx(s.update.db, func(tx database.Tx) error {
		var err error
		item, err = items.FindByIDForUpdate(tx, in.ItemID)
		if errors.Is(err, repository.ErrNotFound) {
			return ErrItemCollabGone
		}
		if err != nil {
			return err
		}
		var merged *int
		if err := tx.QueryRowContext(ctx, `SELECT merged_into_item_id FROM items WHERE id = ?`, in.ItemID).Scan(&merged); err != nil {
			return fmt.Errorf("load merge state for item %d: %w", in.ItemID, err)
		}
		if merged != nil {
			return ErrItemCollabGone
		}
		if ItemDescriptionHash(item.Description) != in.ExpectedHash {
			return ErrItemDescriptionConflict
		}
		if item.Description != in.Description {
			if err := items.UpdateFields(tx, in.ItemID, map[string]any{"description": in.Description}); err != nil {
				return err
			}
		}
		if in.AfterWrite != nil {
			return in.AfterWrite(tx, in.Description)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if item.Description != in.Description {
		PublishItemChange(item.ID)
		PublishWorkspaceChange(item.WorkspaceID)
	}
	return in.Description, nil
}

// ItemDescriptionSettlement is the net change of one burst of live edits.
type ItemDescriptionSettlement struct {
	ItemID int
	// Before is the description when the burst began; After is what the
	// session last stored.
	Before, After string
	// Editors typed during the burst, most recent last. The most recent is
	// recorded as the change's actor.
	Editors []int
}

// SettleCollabDescription records a burst of live edits as one description
// update: a history entry and an item.updated domain event (which drive
// notifications and automations), then webhooks and @mentions, exactly as a
// saved description would. It returns ErrItemCollabGone for deleted items.
func (s *ItemUpdateApplicationService) SettleCollabDescription(ctx context.Context, in ItemDescriptionSettlement) error {
	if in.Before == in.After || len(in.Editors) == 0 {
		return nil
	}
	actor := in.Editors[len(in.Editors)-1]
	now := time.Now()
	history := []HistoryEntry{{
		ItemID:    in.ItemID,
		UserID:    actor,
		FieldName: "description",
		OldValue:  in.Before,
		NewValue:  in.After,
		ChangedAt: now,
	}}
	err := database.WithTx(s.update.db, func(tx database.Tx) error {
		item, err := repository.NewItemRepository(s.update.db).FindByIDForUpdate(tx, in.ItemID)
		if errors.Is(err, repository.ErrNotFound) {
			return ErrItemCollabGone
		}
		if err != nil {
			return err
		}
		metadata := itemEventMetadata(actor, "application", nil)
		metadata.OccurredAt = now
		stampHistorySource(history, metadata)
		if err := s.update.recordItemHistory(tx, history); err != nil {
			return fmt.Errorf("record description history: %w", err)
		}
		_, err = itemevents.NewRecorder(s.update.db).Updated(ctx, tx, item, itemHistoryEventChanges(history), metadata)
		return err
	})
	if err != nil {
		return err
	}

	if s.activityTracker != nil {
		for _, editor := range in.Editors {
			if err := s.activityTracker.TrackItemActivity(editor, in.ItemID, ActivityEdit); err != nil {
				slog.Warn("failed to track item edit activity", slog.Int("user_id", editor), slog.Int("item_id", in.ItemID), slog.Any("error", err))
			}
		}
	}
	updated, err := s.update.loadItemWithJoins(in.ItemID)
	if err != nil {
		return fmt.Errorf("load settled item: %w", err)
	}
	original := *updated
	original.Description = in.Before
	after := *updated
	after.Description = in.After
	switch {
	case s.emitter != nil:
		s.emitter.EmitItemUpdated(&original, &after, false, false, actor, history)
	case s.fallback != nil:
		s.fallback.EmitItemUpdated(&original, &after, false, false, actor, history)
	}
	if s.mentionService != nil {
		// Mentions are reconciled against the stored description, so newly
		// mentioned people are notified once per burst.
		if err := s.mentionService.ProcessMentions(ProcessMentionsParams{
			SourceType:  "item_description",
			SourceID:    updated.ID,
			Content:     updated.Description,
			ItemID:      updated.ID,
			WorkspaceID: updated.WorkspaceID,
			ActorUserID: actor,
		}); err != nil {
			slog.Warn("failed to process description mentions", slog.Int("item_id", updated.ID), slog.Any("error", err))
		}
	}
	return nil
}

package services

// Live co-editing support for pages (internal/collab). This file is the only
// part of co-editing inside the services package: it needs the page write
// pipeline's unexported pieces (sanitizing, excerpts, revisions, search
// chunks) but deliberately changes none of the existing page code, so it can
// be carried on a fork without touching upstream files.

import (
	"database/sql"
	"errors"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
)

// CollabRevisionInterval bounds how often a live co-editing session adds a
// page revision. Checkpoints arrive every few seconds while people type; each
// one updates the page row and its search chunks, but only the first
// checkpoint after this interval (or by a different editor) records history.
// The session end always records a final revision (FinalizeCollabRevision).
const CollabRevisionInterval = 5 * time.Minute

// CollabCheckpointInput is one markdown checkpoint from a live session.
type CollabCheckpointInput struct {
	PageID  int
	ActorID int
	Content string
	// ExpectedContentHash is the stored hash the session last materialized or
	// merged. A different stored hash means another writer changed the page
	// since; the checkpoint is refused so that write can be merged first.
	ExpectedContentHash string
	// AfterWrite runs inside the page transaction with the stored row, so the
	// session's own bookkeeping commits atomically with the page content.
	AfterWrite func(tx database.Tx, page *models.Page) error
}

// CollabCheckpoint stores a live session's markdown. It returns the stored
// page, which may differ from the input after sanitization.
func (s *PageService) CollabCheckpoint(in CollabCheckpointInput) (*models.Page, error) {
	content := sanitize.LongDocument.Sanitize(in.Content)
	hash := contentHash(content)
	return database.WithTxResult(s.db, func(tx database.Tx) (*models.Page, error) {
		if err := s.lockPageRowTx(tx, in.PageID); err != nil {
			return nil, err
		}
		existing, err := s.pages.GetByIDTx(tx, in.PageID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrPageNotFound
			}
			return nil, err
		}
		if existing.ArchivedAt != nil {
			return nil, ErrPageNotFound
		}
		if existing.ContentHash != in.ExpectedContentHash {
			return nil, ErrPageContentConflict
		}
		page := existing
		if hash != existing.ContentHash {
			err = s.pages.UpdateTx(tx, repository.UpdateInput{
				ID:                 existing.ID,
				Title:              existing.Title,
				Slug:               existing.Slug,
				Content:            content,
				ContentHash:        hash,
				Excerpt:            deriveExcerpt(content),
				InheritPermissions: existing.InheritPermissions,
				UpdatedBy:          in.ActorID,
			})
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return nil, ErrPageNotFound
				}
				return nil, err
			}
			page, err = s.pages.GetByIDTx(tx, existing.ID)
			if err != nil {
				return nil, err
			}
			if err := s.collabSnapshotTx(tx, page, in.ActorID, false); err != nil {
				return nil, err
			}
		}
		if in.AfterWrite != nil {
			if err := in.AfterWrite(tx, page); err != nil {
				return nil, err
			}
		}
		return page, nil
	})
}

// FinalizeCollabRevision records a revision when the page's stored content is
// newer than its latest revision. Sessions call it when the last editor leaves
// so coalesced checkpoints always end in a history entry.
func (s *PageService) FinalizeCollabRevision(pageID, actorID int) error {
	return database.WithTx(s.db, func(tx database.Tx) error {
		page, err := s.pages.GetByIDTx(tx, pageID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			return err
		}
		return s.collabSnapshotTx(tx, page, actorID, true)
	})
}

// lockPageRowTx serializes checkpoints with other writers of the page row, so
// the hash compared in CollabCheckpoint is still current when it writes.
// SQLite transactions already hold the database write lock.
func (s *PageService) lockPageRowTx(tx database.Tx, pageID int) error {
	if !database.IsPostgresDriver(s.db.GetDriverName()) {
		return nil
	}
	var id int
	err := tx.QueryRow("SELECT id FROM pages WHERE id = ? FOR UPDATE", pageID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPageNotFound
	}
	return err
}

type collabLatestRevision struct {
	number      int
	contentHash string
	title       string
	changeType  string
	createdBy   int
	createdAt   time.Time
}

// collabSnapshotTx rebuilds search chunks for a checkpointed page and records a
// revision when one is due. Without a new revision the chunks keep pointing at
// the latest revision number, matching how they are labeled in search hits.
func (s *PageService) collabSnapshotTx(tx database.Tx, page *models.Page, actorID int, final bool) error {
	var latest *collabLatestRevision
	var r collabLatestRevision
	err := tx.QueryRow(`
		SELECT revision_number, content_hash, title, change_type, created_by, created_at
		FROM page_revisions WHERE page_id = ? ORDER BY revision_number DESC LIMIT 1`, page.ID,
	).Scan(&r.number, &r.contentHash, &r.title, &r.changeType, &r.createdBy, &r.createdAt)
	switch {
	case err == nil:
		latest = &r
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if latest != nil && latest.contentHash == page.ContentHash && latest.title == page.Title {
		return nil
	}
	due := latest == nil || final || latest.createdBy != actorID ||
		latest.changeType != models.PageRevisionChangeTypeEdit
	if !due {
		// A negative age means the stored timestamp is skewed; record rather
		// than risk suppressing history until the skew elapses.
		age := time.Since(latest.createdAt)
		due = age < 0 || age >= CollabRevisionInterval
	}
	if due {
		return s.snapshotAndRebuildChunks(tx, page, actorID, models.PageRevisionChangeTypeEdit, "")
	}
	if err := s.pages.DeleteChunksForPageTx(tx, page.ID); err != nil {
		return err
	}
	if page.Content == "" {
		return nil
	}
	for _, chunk := range buildPageChunks(page, latest.number, chunkPageMarkdown(page.Content)) {
		if err := s.pages.InsertChunkTx(tx, chunk); err != nil {
			return err
		}
	}
	return nil
}

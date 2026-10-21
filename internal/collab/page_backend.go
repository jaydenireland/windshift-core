package collab

import (
	"context"
	"database/sql"
	"errors"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/services"
)

// PageBackend co-edits knowledge-page content. Titles and metadata keep their
// own save path; the live document is the page body only.
type PageBackend struct {
	db    database.Database
	pages *services.PageService
	perms *services.PagePermissionService
}

// NewPageBackend returns the knowledge-page backend.
func NewPageBackend(db database.Database, pages *services.PageService, perms *services.PagePermissionService) *PageBackend {
	return &PageBackend{db: db, pages: pages, perms: perms}
}

// Kind implements Backend.
func (b *PageBackend) Kind() string { return "page" }

// OwnerPageID implements Backend.
func (b *PageBackend) OwnerPageID(id int) *int { return &id }

// Authorize implements Backend using the page ACL evaluation shared by every
// page route. Archived pages are view-only.
func (b *PageBackend) Authorize(_ context.Context, userID, id int) (Access, error) {
	page, err := b.pages.GetByID(id)
	if err != nil {
		if errors.Is(err, services.ErrPageNotFound) {
			return AccessNone, nil
		}
		return AccessNone, err
	}
	if page.ArchivedAt == nil {
		canEdit, err := b.perms.Can(userID, page.WorkspaceID, id, services.PageOpEdit)
		if err != nil {
			return AccessNone, err
		}
		if canEdit {
			return AccessEdit, nil
		}
	}
	canView, err := b.perms.Can(userID, page.WorkspaceID, id, services.PageOpView)
	if err != nil {
		return AccessNone, err
	}
	if canView {
		return AccessView, nil
	}
	return AccessNone, nil
}

// Load implements Backend.
func (b *PageBackend) Load(_ context.Context, id int) (Content, error) {
	page, err := b.pages.GetByID(id)
	if err != nil {
		if errors.Is(err, services.ErrPageNotFound) {
			return Content{}, ErrGone
		}
		return Content{}, err
	}
	if page.ArchivedAt != nil {
		return Content{}, ErrGone
	}
	return Content{Markdown: page.Content, Hash: page.ContentHash}, nil
}

// Probe implements Backend with one indexed read of the page row.
func (b *PageBackend) Probe(ctx context.Context, id int) (Probe, error) {
	var hash, title, metadata string
	var archived bool
	err := b.db.QueryRowContext(ctx,
		`SELECT content_hash, title, metadata, archived_at IS NOT NULL FROM pages WHERE id = ?`, id,
	).Scan(&hash, &title, &metadata, &archived)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && archived) {
		return Probe{}, ErrGone
	}
	if err != nil {
		return Probe{}, err
	}
	return Probe{ContentHash: hash, Meta: title + "\x00" + metadata}, nil
}

// Checkpoint implements Backend.
func (b *PageBackend) Checkpoint(_ context.Context, actorID, id int, markdown, expectedHash string, afterWrite func(tx database.Tx, stored Content) error) (Content, error) {
	page, err := b.pages.CollabCheckpoint(services.CollabCheckpointInput{
		PageID:              id,
		ActorID:             actorID,
		Content:             markdown,
		ExpectedContentHash: expectedHash,
		AfterWrite: func(tx database.Tx, page *models.Page) error {
			return afterWrite(tx, Content{Markdown: page.Content, Hash: page.ContentHash})
		},
	})
	switch {
	case errors.Is(err, services.ErrPageContentConflict):
		return Content{}, ErrConflict
	case errors.Is(err, services.ErrPageNotFound):
		return Content{}, ErrGone
	case err != nil:
		return Content{}, err
	}
	return Content{Markdown: page.Content, Hash: page.ContentHash}, nil
}

// Finalize implements Backend.
func (b *PageBackend) Finalize(_ context.Context, actorID, id int) error {
	return b.pages.FinalizeCollabRevision(id, actorID)
}

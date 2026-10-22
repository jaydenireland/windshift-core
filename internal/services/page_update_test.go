package services

import (
	"errors"
	"path/filepath"
	"testing"

	"windshift/internal/database"
	"windshift/internal/repository"
)

func newPageTestService(t *testing.T) (*PageService, database.Database) {
	t.Helper()
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "pages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users (id, email, username, first_name, last_name) VALUES (7, 'ada@example.test', 'ada', 'Ada', 'L'), (8, 'grace@example.test', 'grace', 'Grace', 'H')`,
		`INSERT INTO workspaces (id, name, key) VALUES (42, 'Docs', 'DOC')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return NewPageService(db), db
}

func TestPageUpdatePreserveContentLeavesBodyAlone(t *testing.T) {
	svc, db := newPageTestService(t)
	page, err := svc.Create(7, CreatePageInput{WorkspaceID: 42, Title: "Guide", Content: "original"})
	if err != nil {
		t.Fatal(err)
	}
	// Another writer changes the body after a title edit read the page.
	if _, err := svc.Update(8, UpdatePageInput{ID: page.ID, Title: "Guide", Content: "newer body"}); err != nil {
		t.Fatal(err)
	}
	// The title edit still carries the old body; it must not write it back.
	updated, err := svc.Update(7, UpdatePageInput{ID: page.ID, Title: "Renamed", Content: "original", PreserveContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Renamed" || updated.Content != "newer body" {
		t.Fatalf("got title=%q content=%q, want Renamed/newer body", updated.Title, updated.Content)
	}
	var revisionContent string
	if err := db.QueryRow(`SELECT content FROM page_revisions WHERE page_id = ? ORDER BY revision_number DESC LIMIT 1`, page.ID).Scan(&revisionContent); err != nil {
		t.Fatal(err)
	}
	if revisionContent != "newer body" {
		t.Fatalf("revision content = %q, want the stored body", revisionContent)
	}
}

func TestPageUpdateExpectedHashConflict(t *testing.T) {
	svc, _ := newPageTestService(t)
	page, err := svc.Create(7, CreatePageInput{WorkspaceID: 42, Title: "Guide", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	stale := page.ContentHash
	if _, err := svc.Update(7, UpdatePageInput{ID: page.ID, Title: "Guide", Content: "v2", ExpectedContentHash: &stale}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(8, UpdatePageInput{ID: page.ID, Title: "Guide", Content: "v3", ExpectedContentHash: &stale}); !errors.Is(err, ErrPageContentConflict) {
		t.Fatalf("second writer with stale hash: err = %v, want ErrPageContentConflict", err)
	}
}

func TestPageRepositoryConditionalUpdate(t *testing.T) {
	svc, db := newPageTestService(t)
	page, err := svc.Create(7, CreatePageInput{WorkspaceID: 42, Title: "Guide", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewPageRepository(db)
	wrong := "not-the-hash"
	err = database.WithTx(db, func(tx database.Tx) error {
		return repo.UpdateTx(tx, repository.UpdateInput{
			ID: page.ID, Title: page.Title, Slug: page.Slug, Content: "v2", ContentHash: "h2",
			ExpectedContentHash: &wrong, UpdatedBy: 7,
		})
	})
	if !errors.Is(err, repository.ErrContentHashMismatch) {
		t.Fatalf("err = %v, want ErrContentHashMismatch", err)
	}
	hash := page.ContentHash
	err = database.WithTx(db, func(tx database.Tx) error {
		return repo.UpdateTx(tx, repository.UpdateInput{ID: page.ID + 999, Title: "x", Slug: "x", ExpectedContentHash: &hash, UpdatedBy: 7})
	})
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing page: err = %v, want ErrNotFound", err)
	}
}

package repository

import (
	"errors"
	"path/filepath"
	"testing"

	"windshift/internal/database"
)

func TestPageUpdateTxExpectedContentHashGuardsTheWrite(t *testing.T) {
	db, err := database.NewSQLiteDBWithPoolSizes(filepath.Join(t.TempDir(), "windshift.db"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	var userID, workspaceID int
	if err := db.QueryRow(`INSERT INTO users (email, username, first_name, last_name) VALUES (?, ?, ?, ?) RETURNING id`,
		"page-guard@example.com", "page-guard", "Page", "Guard").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES (?, ?) RETURNING id`, "Page guard", "PGUARD").Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}

	repo := NewPageRepository(db)
	pageID, err := database.WithTxResult(db, func(tx database.Tx) (int, error) {
		return repo.CreateTx(tx, CreateInput{
			WorkspaceID: workspaceID, Title: "Runbook", Slug: "runbook", Metadata: "{}",
			Content: "v1", ContentHash: "hash-v1", CreatedBy: userID, InheritPermissions: true, Path: "/",
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	save := func(id int, expectedHash *string, content string) error {
		return database.WithTx(db, func(tx database.Tx) error {
			return repo.UpdateTx(tx, UpdateInput{
				ID: id, Title: "Runbook", Slug: "runbook", Content: content, ContentHash: "hash-" + content,
				InheritPermissions: true, UpdatedBy: userID, ExpectedContentHash: expectedHash,
			})
		})
	}

	v1Hash, v2Hash := "hash-v1", "hash-v2"
	if err := save(pageID, &v1Hash, "v2"); err != nil {
		t.Fatalf("save against the stored hash: %v", err)
	}
	if err := save(pageID, &v1Hash, "v3"); !errors.Is(err, ErrConcurrentUpdate) {
		t.Fatalf("save against a stale hash: got %v, want ErrConcurrentUpdate", err)
	}
	if err := save(pageID+1000, &v2Hash, "v3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("guarded save of a missing page: got %v, want ErrNotFound", err)
	}
	page, err := repo.GetByID(pageID)
	if err != nil {
		t.Fatal(err)
	}
	if page.Content != "v2" || page.ContentHash != v2Hash {
		t.Fatalf("rejected saves changed the page: content=%q hash=%q", page.Content, page.ContentHash)
	}

	if err := save(pageID, nil, "v4"); err != nil {
		t.Fatalf("save without an expected hash: %v", err)
	}
	if err := save(pageID+1000, nil, "v4"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unguarded save of a missing page: got %v, want ErrNotFound", err)
	}
}

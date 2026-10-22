package services

import (
	"errors"
	"testing"

	"windshift/internal/database"
	"windshift/internal/models"
)

func countRevisions(t *testing.T, db database.Database, pageID int) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM page_revisions WHERE page_id = ?`, pageID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCollabCheckpointCoalescesRevisions(t *testing.T) {
	svc, db := newPageTestService(t)
	page, err := svc.Create(7, CreatePageInput{WorkspaceID: 42, Title: "Guide", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	base := countRevisions(t, db, page.ID)

	// First checkpoint after a create revision records an edit revision.
	p1, err := svc.CollabCheckpoint(CollabCheckpointInput{PageID: page.ID, ActorID: 7, Content: "v2", ExpectedContentHash: page.ContentHash})
	if err != nil {
		t.Fatal(err)
	}
	// Rapid follow-ups by the same editor only update the row.
	p2, err := svc.CollabCheckpoint(CollabCheckpointInput{PageID: page.ID, ActorID: 7, Content: "v3", ExpectedContentHash: p1.ContentHash})
	if err != nil {
		t.Fatal(err)
	}
	if got := countRevisions(t, db, page.ID); got != base+1 {
		t.Fatalf("revisions = %d, want %d (coalesced)", got, base+1)
	}
	if p2.Content != "v3" {
		t.Fatalf("content = %q", p2.Content)
	}
	// Ending the session records the final state, once.
	if err := svc.FinalizeCollabRevision(page.ID, 7); err != nil {
		t.Fatal(err)
	}
	if got := countRevisions(t, db, page.ID); got != base+2 {
		t.Fatalf("revisions after finalize = %d, want %d", got, base+2)
	}
	if err := svc.FinalizeCollabRevision(page.ID, 7); err != nil {
		t.Fatal(err)
	}
	if got := countRevisions(t, db, page.ID); got != base+2 {
		t.Fatalf("revisions after idle finalize = %d, want %d", got, base+2)
	}
	// Search chunks follow the stored content even without a new revision.
	var chunkText string
	if err := db.QueryRow(`SELECT content FROM page_chunks WHERE page_id = ? LIMIT 1`, page.ID).Scan(&chunkText); err != nil {
		t.Fatal(err)
	}
	if chunkText != "v3" {
		t.Fatalf("chunk content = %q, want v3", chunkText)
	}
}

func TestCollabCheckpointConflictAndAfterWrite(t *testing.T) {
	svc, _ := newPageTestService(t)
	page, err := svc.Create(7, CreatePageInput{WorkspaceID: 42, Title: "Guide", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CollabCheckpoint(CollabCheckpointInput{PageID: page.ID, ActorID: 7, Content: "v2", ExpectedContentHash: "stale"}); !errors.Is(err, ErrPageContentConflict) {
		t.Fatalf("err = %v, want ErrPageContentConflict", err)
	}
	boom := errors.New("bookkeeping failed")
	_, err = svc.CollabCheckpoint(CollabCheckpointInput{
		PageID: page.ID, ActorID: 7, Content: "v2", ExpectedContentHash: page.ContentHash,
		AfterWrite: func(database.Tx, *models.Page) error { return boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want bookkeeping failure", err)
	}
	reloaded, err := svc.GetByID(page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Content != "v1" {
		t.Fatalf("content = %q: an AfterWrite failure must roll back the page write", reloaded.Content)
	}
	// An unchanged body still runs the bookkeeping, without a write.
	ran := false
	if _, err := svc.CollabCheckpoint(CollabCheckpointInput{
		PageID: page.ID, ActorID: 7, Content: "v1", ExpectedContentHash: page.ContentHash,
		AfterWrite: func(database.Tx, *models.Page) error { ran = true; return nil },
	}); err != nil || !ran {
		t.Fatalf("unchanged checkpoint: err=%v ran=%v", err, ran)
	}
}

func TestCollabCheckpointRefusesArchivedPage(t *testing.T) {
	svc, _ := newPageTestService(t)
	page, err := svc.Create(7, CreatePageInput{WorkspaceID: 42, Title: "Guide", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Archive(7, page.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CollabCheckpoint(CollabCheckpointInput{PageID: page.ID, ActorID: 7, Content: "v2", ExpectedContentHash: page.ContentHash}); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("err = %v, want ErrPageNotFound", err)
	}
}

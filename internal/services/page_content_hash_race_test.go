package services

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"windshift/internal/database"
)

// pageRacePostgresURLEnv names a postgres:// URL whose role may CREATE
// DATABASE (e.g. the dev-postgres.sh container). PostgreSQL-backed tests skip
// when it is unset.
const pageRacePostgresURLEnv = "WINDSHIFT_TEST_POSTGRES_URL"

func openPageRaceSQLite(t *testing.T) database.Database {
	t.Helper()
	db, err := database.NewSQLiteDBWithPoolSizes(filepath.Join(t.TempDir(), "windshift.db"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	return db
}

// openPageRacePostgres initializes a throwaway database on the server named by
// WINDSHIFT_TEST_POSTGRES_URL and drops it when the test ends.
func openPageRacePostgres(t *testing.T) database.Database {
	t.Helper()
	adminURL := os.Getenv(pageRacePostgresURLEnv)
	if adminURL == "" {
		t.Skipf("%s not set; skipping PostgreSQL-backed test", pageRacePostgresURLEnv)
	}
	testURL, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("%s must be a postgres:// URL: %v", pageRacePostgresURLEnv, err)
	}
	admin, err := sql.Open("postgres", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	name := fmt.Sprintf("windshift_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
	})

	testURL.Path = "/" + name
	db, err := database.NewPostgresDB(testURL.String(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedPageRaceFixtures(t *testing.T, db database.Database) (userID, workspaceID int) {
	t.Helper()
	if err := db.QueryRow(`INSERT INTO users (email, username, first_name, last_name) VALUES (?, ?, ?, ?) RETURNING id`,
		"page-race@example.com", "page-race", "Page", "Race").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO workspaces (name, key) VALUES (?, ?) RETURNING id`, "Page race", "PGRACE").Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	return userID, workspaceID
}

// waitForPageLockWaiters returns once n sessions on the test database are
// queued on a lock.
func waitForPageLockWaiters(t *testing.T, db database.Database, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d sessions to queue on the page row (have %d)", n, waiting)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPageUpdateConcurrentSavesWithSameExpectedHash(t *testing.T) {
	for _, backend := range []struct {
		name string
		open func(*testing.T) database.Database
	}{
		{name: "sqlite", open: openPageRaceSQLite},
		{name: "postgres", open: openPageRacePostgres},
	} {
		t.Run(backend.name, func(t *testing.T) {
			db := backend.open(t)
			userID, workspaceID := seedPageRaceFixtures(t, db)
			svc := NewPageService(db)
			page, err := svc.Create(userID, CreatePageInput{WorkspaceID: workspaceID, Title: "Runbook", Content: "original"})
			if err != nil {
				t.Fatal(err)
			}
			revisionsBefore, err := svc.CountRevisions(page.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := page.ContentHash

			// SQLite serializes write transactions, so the two saves can only run
			// one after the other. On PostgreSQL, hold the row lock so both saves
			// read the original hash and queue on the write before either commits:
			// the interleaving that used to let both succeed.
			release := func() {}
			if database.IsPostgresDriver(db.GetDriverName()) {
				blocker, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				release = func() { _ = blocker.Rollback() }
				t.Cleanup(release)
				var lockedID int
				if err := blocker.QueryRow(`SELECT id FROM pages WHERE id = ? FOR UPDATE`, page.ID).Scan(&lockedID); err != nil {
					t.Fatal(err)
				}
			}

			contents := []string{"edit from session A", "edit from session B"}
			errs := make([]error, len(contents))
			var wg sync.WaitGroup
			for i, content := range contents {
				wg.Go(func() {
					_, errs[i] = svc.Update(userID, UpdatePageInput{ID: page.ID, Title: page.Title, Content: content, ExpectedContentHash: &expected})
				})
			}
			if database.IsPostgresDriver(db.GetDriverName()) {
				waitForPageLockWaiters(t, db, len(contents))
			}
			release()
			wg.Wait()

			var winner string
			successes, conflicts := 0, 0
			for i, err := range errs {
				switch {
				case err == nil:
					successes++
					winner = contents[i]
				case errors.Is(err, ErrPageContentConflict):
					conflicts++
				default:
					t.Fatalf("save %d: unexpected error: %v", i, err)
				}
			}
			if successes != 1 || conflicts != 1 {
				t.Fatalf("want exactly one success and one ErrPageContentConflict, got errors %v", errs)
			}

			stored, err := svc.GetByID(page.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Content != winner {
				t.Fatalf("stored content = %q, want the successful save %q", stored.Content, winner)
			}
			revisionsAfter, err := svc.CountRevisions(page.ID)
			if err != nil {
				t.Fatal(err)
			}
			if revisionsAfter != revisionsBefore+1 {
				t.Fatalf("revisions = %d, want %d: the rejected save must not record one", revisionsAfter, revisionsBefore+1)
			}
		})
	}
}

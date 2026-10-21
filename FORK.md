# Fork notes: live co-editing

This fork adds live co-editing of knowledge pages on top of
[Windshiftapp/core](https://github.com/Windshiftapp/core). The feature is kept
almost entirely in fork-owned files so merging upstream stays routine. This
file lists everything the fork changes in upstream-owned files and how to
merge.

## Fork-owned (upstream never touches these)

| Path | What it is |
| --- | --- |
| `internal/collab/` | Co-editing server: WebSocket relay, rooms, update log, page backend, its own schema (`schema_*.sql`, created by `collab.Install`) |
| `internal/services/page_collab.go` | Checkpoint writes for live sessions. Lives in `services` to reuse unexported page helpers; changes no existing code |
| `frontend/src/lib/collab/` | Browser session (Yjs), Milkdown binding, merge, presence, strings for all locales, page-view controller and components |
| `frontend/vite.collab.js` | Vite settings for co-editing, added as a plugin |
| `FORK.md` | This file |

Tests: `internal/collab/hub_test.go`, `internal/services/page_collab_test.go`,
`frontend/src/lib/collab/*.spec.js` (force-added; the repo ignores test files
by default).

## Seams in upstream files

Each seam is a call site, kept as small as possible. List them all with:

```sh
git diff upstream/main -- . ':!internal/collab' ':!frontend/src/lib/collab' \
  ':!internal/services/page_collab*.go' ':!frontend/vite.collab.js' ':!FORK.md' \
  ':!*.spec.js' ':!frontend/package-lock.json' ':!frontend/src/lib/generated'
```

| File | Seam |
| --- | --- |
| `internal/server/server.go` | Import plus one `collab.Install(...)` call after `routes.RegisterAll`. Shutdown hooks itself via `http.Server.RegisterOnShutdown` |
| `frontend/src/lib/editors/MilkdownEditor.svelte` | `collab` prop; `prepareCollabBinding` call sites in mount/destroy; three `!collabBinding?.bound` guards on whole-document replacements |
| `frontend/src/lib/editors/LazyMilkdownEditor.svelte` | Passes `collab`; re-keys on `collabKey` |
| `frontend/src/lib/features/pages/PagesView.svelte` | `useLivePage(...)`; `<LivePageNotices>`; passes `live` to the toolbar and editor props; title/appearance saves omit the body while live (`omitNullBody`, `live.bodyless`) |
| `frontend/src/lib/features/pages/PageEditor.svelte` | Passes `collab`/`collabKey` to the editor |
| `frontend/src/lib/features/pages/PageToolbar.svelte` | `live` prop rendering `<LivePageStatus>` |
| `frontend/vite.config.js` | Import plus `collabVitePlugin()` |
| `frontend/package.json`, `go.mod` | Dependencies: `yjs`, `y-prosemirror`, `y-protocols`, `lib0`, `@milkdown/plugin-collab` (pinned to the installed Milkdown version), `diff-match-patch`; `github.com/coder/websocket` |
| Generated: `frontend/package-lock.json`, `go.sum`, `frontend/src/lib/generated/license-manifest.json` | Regenerate rather than hand-merge (below) |

Deliberately avoided: migrations and schema files, locale catalogs, config
flags, CSRF and rate-limit middleware, and hooks in the page service. The
equivalents live in `internal/collab` (own schema, `WINDSHIFT_DISABLE_COLLAB`,
own WebSocket origin check, socket path ending in `/events` so the existing
limiter treats it as a stream, polling for outside writes).

## Upstream candidate

The commit `fix(pages): keep the body on title-only saves; make the content
hash check atomic` fixes two bugs that exist upstream and is independent of
co-editing (`page_repository.go`, `page_service.go`,
`page_application_service.go`, `page_update_test.go`). Send it upstream; once
it is merged the fork carries nothing in those files. Co-editing relies on its
title-only behavior so a rename cannot write back a stale body.

## Merging upstream

```sh
git fetch upstream
git merge upstream/main
```

Conflicts in generated files:

- `frontend/package-lock.json`: take upstream's (`git checkout --theirs`), then
  `cd frontend && npm install` to re-add the fork's dependencies.
- `go.sum`: take upstream's, then `go mod tidy`.
- `frontend/src/lib/generated/license-manifest.json`: take upstream's, then
  `cd frontend && npm run licenses:generate`.

Then check that the seams still fit:

```sh
go build ./... && go test ./internal/collab/ ./internal/services/ -run 'Collab|PageUpdate|PageRepository'
cd frontend && npm run check && npm run typecheck && npx vitest run src/lib/collab
```

`page_collab.go` uses unexported page helpers (`contentHash`, `deriveExcerpt`,
`chunkPageMarkdown`, `buildPageChunks`, `snapshotAndRebuildChunks`,
`PageRepository.GetByIDTx/UpdateTx/DeleteChunksForPageTx/InsertChunkTx`). If
upstream renames them, the build fails there, never silently. `PageBackend.Probe`
reads `pages.content_hash`, `title`, `metadata` and `archived_at` directly.

## Runtime

- Tables `collab_documents` and `collab_updates` are created at startup with
  `CREATE ... IF NOT EXISTS`. Keep future changes to them idempotent and
  additive. Dropping co-editing later: drop both tables.
- `WINDSHIFT_DISABLE_COLLAB=1` turns co-editing off; the page editor falls back
  to upstream's autosave.
- Single server process only (sessions live in memory, like upstream's live
  updates).

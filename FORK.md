# Fork notes: live co-editing and word-level history

This fork adds live co-editing of knowledge pages and work-item descriptions,
and word-level diffs in item history, on top of
[Windshiftapp/core](https://github.com/Windshiftapp/core). Each feature is kept
almost entirely in fork-owned files so merging upstream stays routine. This
file lists everything the fork changes in upstream-owned files and how to
merge.

## Fork-owned (upstream never touches these)

| Path | What it is |
| --- | --- |
| `internal/collab/` | Co-editing server: WebSocket relay, rooms, update log, page and item backends, settling of item edits, its own schema (`schema_*.sql`, created by `collab.Install`) |
| `internal/services/page_collab.go` | Checkpoint writes for live page sessions. Lives in `services` to reuse unexported page helpers; changes no existing code |
| `internal/services/item_collab.go` | Silent description checkpoints and the settled history/event/notification step for items. Same reasoning as `page_collab.go` |
| `frontend/src/lib/collab/` | Browser session (Yjs), Milkdown binding, merge, presence, strings for all locales, page and item-description controllers and components |
| `frontend/vite.collab.js` | Vite settings for co-editing, added as a plugin |
| `frontend/src/lib/historydiff/` | Word-level diff for text changes in item history: `wordDiff.js` (diff, collapsing, which rows qualify), `HistoryTextDiff.svelte`, own strings for all locales |
| `FORK.md` | This file |

Tests: `internal/collab/hub_test.go`, `internal/services/page_collab_test.go`,
`internal/services/item_collab_test.go`,
`frontend/src/lib/collab/*.spec.js`, `frontend/src/lib/historydiff/*.spec.js`
(force-added; the repo ignores test files by default).

## Seams in upstream files

Each seam is a call site, kept as small as possible. List them all with:

```sh
git diff upstream/main -- . ':!internal/collab' ':!frontend/src/lib/collab' \
  ':!frontend/src/lib/historydiff' ':!internal/services/page_collab*.go' \
  ':!internal/services/item_collab*.go' ':!frontend/vite.collab.js' ':!FORK.md' \
  ':!*.spec.js' ':!frontend/package-lock.json' ':!frontend/src/lib/generated'
```

| File | Seam |
| --- | --- |
| `internal/server/server.go` | Import plus one `collab.Install(...)` call after `routes.RegisterAll`. Shutdown hooks itself via `http.Server.RegisterOnShutdown` |
| `frontend/src/lib/editors/MilkdownEditor.svelte` | `collab` prop; `prepareCollabBinding` call sites in mount/destroy (destroy awaits the binding's detach); three `!collabBinding?.bound` guards on whole-document replacements |
| `frontend/src/lib/editors/LazyMilkdownEditor.svelte` | Passes `collab`; re-keys on `collabKey` |
| `frontend/src/lib/features/pages/PagesView.svelte` | `useLivePage(...)`; `<LivePageNotices>`; passes `live` to the toolbar and editor props; title/appearance saves omit the body while live (`omitNullBody`, `live.bodyless`) |
| `frontend/src/lib/features/pages/PageEditor.svelte` | Passes `collab`/`collabKey` to the editor |
| `frontend/src/lib/features/pages/PageToolbar.svelte` | `live` prop rendering `<LivePageStatus>` |
| `frontend/src/lib/features/items/ItemDetailDescription.svelte` | `useLiveItemDescription(...)` with `alwaysOn`; the editor branch also renders when `live.alwaysOn` (class `item-description-live`, `hideToolbarUntilFocus`); passes `collab`/`collabKey`/`readonly` to the editor; `<LiveItemDescriptionBar>` (Done, or just status when always on) instead of Save/Cancel while live; keydown defers to `live.leave` when always on, else Escape and Cmd/Ctrl+Enter finish a live edit; `insertImage` skips entering edit mode when always on; shows the just-edited text until the item reloads |
| `frontend/src/lib/features/items/ItemHistory.svelte` | One import; one `{:else if historyDiffApplies(change)}` branch before the generic `old → new` branch, rendering `<HistoryTextDiff>` |
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
cd frontend && npm run check && npm run typecheck && npx vitest run src/lib/collab src/lib/historydiff
```

`page_collab.go` uses unexported page helpers (`contentHash`, `deriveExcerpt`,
`chunkPageMarkdown`, `buildPageChunks`, `snapshotAndRebuildChunks`,
`PageRepository.GetByIDTx/UpdateTx/DeleteChunksForPageTx/InsertChunkTx`). If
upstream renames them, the build fails there, never silently. `PageBackend.Probe`
reads `pages.content_hash`, `title`, `metadata` and `archived_at` directly.

`item_collab.go` likewise uses `ItemUpdateApplicationService`'s fields and
`itemEventMetadata`, `stampHistorySource`, `itemHistoryEventChanges`,
`recordItemHistory`, `loadItemWithJoins` and `ItemRepository.UpdateFields`.
`ItemBackend.Probe` reads `items.description` and `merged_into_item_id`. If
upstream adds side effects to description saves (in `updateItem`), mirror them
in `SettleCollabDescription`.

## Word-level history

`item_history` already stores the full old and new text of a description
change (one row per save, or per settled co-editing burst), so no backend or
schema change is involved. `ItemHistory.svelte` upstream shows those as a
truncated `old → new` pair; the seam hands rows that `historyDiffApplies`
accepts (always `description`; other plain text once it is long enough to be
truncated; never resolved ids, JSON, attachment or diagram values) to
`HistoryTextDiff`, which renders insertions and deletions by word and folds
long unchanged runs behind an "N unchanged words" button.

If upstream restructures the `{#each group.changes}` block, re-add the branch
just before its final `{:else}`. Nothing else depends on upstream internals.

## Item descriptions: always-on editor

When co-editing is available, an item's description is a live editor for
everyone who may edit the item, from the moment the item opens. There is no
click-to-edit: text can be selected and copied like any text, and everyone
else's cursors and selections show inline, so people sharing a screen can
point at a passage. The editor has no input chrome until it has focus; the
toolbar then appears below the text (never above it, so the text under the
pointer does not move mid-drag). Escape and Cmd/Ctrl+Enter leave the text
(pending edits are stored, focus drops) rather than saving or cancelling.
Saving is automatic as before (`collab_pending_settlements`, below). Opening
an item without typing changes nothing: `CollabSession` never sends a
checkpoint whose markdown equals what it last confirmed, even when forced by
a flush or close, because the editor's serialization of the stored text can
differ in formatting (list markers, blank lines) and would otherwise be
written back as a change by someone who typed nothing.

Until the session is live (`/api/collab/status` pending, or the server has
co-editing disabled) the description shows as upstream's read-only view with
its click-to-edit, so a server without co-editing behaves exactly as upstream.
Users who cannot edit the item see upstream's read-only view.

## Item descriptions: when history and notifications fire

A live description saves itself every few seconds with no history, domain
event, notification, automation or webhook. Each checkpoint extends the item's
open "burst" (`collab_pending_settlements`). Once a burst has had no new
writes for two minutes (`collab.ItemSettleAfter`), the hub records it as one
ordinary description change: one history entry (old text before the burst,
new text after), one `item.updated` event (which drives notifications and
automations), webhooks and @mention notifications, attributed to the person
who typed last. A write made outside the session (API, agent) closes the open
burst first, so it is recorded on its own and never folded into the session's
change. Bursts are durable: ones pending at shutdown settle after restart.

## Runtime

- Tables `collab_documents`, `collab_updates` and
  `collab_pending_settlements` are created at startup with
  `CREATE ... IF NOT EXISTS`. Keep future changes to them idempotent and
  additive. Dropping co-editing later: drop the three tables (settle pending
  bursts first, or their history is never recorded).
- Items are hard-deleted without a foreign key to co-editing state; the hub
  prunes state of deleted items hourly.
- `WINDSHIFT_DISABLE_COLLAB=1` turns co-editing off; the page editor falls back
  to upstream's autosave and item descriptions to their Save button.
- Single server process only (sessions live in memory, like upstream's live
  updates).

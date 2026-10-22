import { untrack } from 'svelte';
import { pages } from '../../api/pages.js';
import { authStore } from '../../stores/auth.svelte.js';
import { uniquePeople } from '../collabPresence.js';
import { useCollabDocument } from '../useCollabDocument.svelte.js';

/**
 * Live co-editing for the knowledge-page view (PagesView.svelte). The view
 * keeps its own state and autosave; this controller decides when the body is
 * a shared document and exposes small helpers for the view's call sites, so
 * the view carries almost none of the co-editing logic.
 *
 * While live, the body is stored by the session; the view's autosave only
 * saves the title and appearance (`bodyless`). When co-editing is unavailable
 * every helper is a pass-through and the view behaves exactly as before.
 *
 * @param {{
 *   page: () => any,
 *   workspaceId: () => any,
 *   apply: (page: any, options: { body: boolean }) => void,
 * }} options `apply` receives a fresher copy of the page from the server:
 *   with `body` when the stored markdown is shown, otherwise to refresh the
 *   title and metadata around a live body.
 */
export function useLivePage({ page, workspaceId, apply }) {
  const doc = useCollabDocument(() => {
    const current = page();
    return current ? { kind: 'page', id: current.id } : null;
  });
  const active = $derived(doc.mode === 'live' && !!doc.session);
  const pending = $derived(doc.mode === 'pending');
  const seeded = $derived(active && !!doc.state?.seeded);
  // An unseeded document (no editor has opened it yet) or one with an outside
  // write no editor has merged yet is shown from the stored markdown.
  const showsStored = $derived(active && (!seeded || !!doc.state?.stale));
  const people = $derived(uniquePeople(doc.state?.peers, authStore.currentUser?.id));

  async function refresh(body) {
    const current = untrack(page);
    if (!current) return;
    try {
      const fresh = await pages.getPage(untrack(workspaceId), current.id);
      if (untrack(page)?.id === current.id) apply(fresh, { body });
    } catch (err) {
      console.error('failed to refresh page', err);
    }
  }

  // Keep a stored-markdown view current while the live document is not shown.
  $effect(() => {
    const hash = doc.state?.storedHash;
    const current = untrack(page);
    if (!showsStored || !current || !hash || hash === current.content_hash) return;
    void refresh(true);
  });

  // Someone renamed the page (or changed its icon) while it is open.
  $effect(() => {
    if ((doc.state?.metaVersion ?? 0) > 0) void refresh(false);
  });

  return {
    /** The body is a live shared document. */
    get active() {
      return active;
    },
    /** Still deciding whether co-editing is available. */
    get pending() {
      return pending;
    },
    get state() {
      return doc.state;
    },
    get people() {
      return people;
    },
    /** Session for the editor's `collab` prop. */
    get editorSession() {
      return active ? doc.session : null;
    },
    /** Re-mounts the editor when its binding must change. */
    get editorKey() {
      return active ? `live:${doc.state?.docVersion ?? 0}:${seeded && !doc.state?.stale}` : '';
    },
    /** The editor's readonly flag, given the view's own. */
    readonly(base) {
      return (
        base ||
        pending ||
        (active && (!doc.state?.writable || showsStored || doc.state?.status === 'closed'))
      );
    },
    /** Diagram saves merge into a live body, so they skip the hash check. */
    diagramHash(hash) {
      return active ? '' : hash;
    },
    /** Before a diagram opens, store the live body so the server edits current text. */
    beforeDiagramOpen(fallback) {
      return active ? () => doc.session.requestCheckpoint() : fallback;
    },
    /** A page update payload without the body while it is live. */
    bodyless(payload) {
      return active ? { ...payload, content: undefined, expectedContentHash: undefined } : payload;
    },
    /** Refresh the title and metadata around a live body. */
    refreshMeta() {
      return refresh(false);
    },
  };
}

/** A page update payload, omitting the body when the snapshot has none. */
export function omitNullBody(payload) {
  return payload.content === null
    ? { ...payload, content: undefined, expectedContentHash: undefined }
    : payload;
}

import { authStore } from '../../stores/auth.svelte.js';
import { uniquePeople } from '../collabPresence.js';
import { useCollabDocument } from '../useCollabDocument.svelte.js';

/**
 * Live co-editing for an item description (ItemDetailDescription.svelte).
 *
 * The session opens while the description is being edited. While it is live
 * the description saves itself as people type (no Save button); the server
 * records one history entry and sends notifications once the edits have gone
 * quiet for two minutes. When co-editing is unavailable the component keeps
 * its Save and Cancel buttons.
 *
 * @param {{ item: () => any, editing: () => boolean }} options
 */
export function useLiveItemDescription({ item, editing }) {
  const doc = useCollabDocument(() => {
    const current = item();
    return current?.id && editing() ? { kind: 'item', id: current.id } : null;
  });
  const active = $derived(doc.mode === 'live' && !!doc.session);
  const pending = $derived(doc.mode === 'pending');
  const seeded = $derived(active && !!doc.state?.seeded);
  // Until an editor has built the shared document, or while an outside write
  // waits to be merged, the stored description is shown read-only.
  const showsStored = $derived(active && (!seeded || !!doc.state?.stale));
  const people = $derived(uniquePeople(doc.state?.peers, authStore.currentUser?.id));

  return {
    /** The description is a live shared document. */
    get active() {
      return active;
    },
    /** Still deciding whether co-editing is available. */
    get pending() {
      return pending;
    },
    /** Saving is automatic: hide Save and Cancel. */
    get autosaves() {
      return active || pending;
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
    /** The editor's readonly flag. */
    get readonly() {
      return (
        pending ||
        (active && (!doc.state?.writable || showsStored || doc.state?.status === 'closed'))
      );
    },
    /** Markdown of the live description, or null when not live. */
    currentMarkdown() {
      try {
        return active ? doc.session.currentMarkdown() : null;
      } catch {
        return null;
      }
    },
    /** Store pending edits now, before the editor closes. */
    flush() {
      return active ? doc.session.requestCheckpoint() : Promise.resolve();
    },
  };
}

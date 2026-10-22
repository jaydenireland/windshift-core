import { authStore } from '../../stores/auth.svelte.js';
import { getShortcut, matchesShortcut } from '../../utils/keyboardShortcuts.js';
import { uniquePeople } from '../collabPresence.js';
import { useCollabDocument } from '../useCollabDocument.svelte.js';
import './liveItemDescription.css';

const saveShortcut = getShortcut('description', 'save');
const cancelShortcut = getShortcut('description', 'cancel');

/**
 * Live co-editing for an item description (ItemDetailDescription.svelte).
 *
 * With `alwaysOn`, the session opens as soon as the item is shown to someone
 * who may edit it, and the description is a live editor the whole time: no
 * click-to-edit, text can be selected like any other text, and everyone's
 * cursors and selections are visible. Otherwise the session opens while the
 * description is being edited. Either way, while it is live the description
 * saves itself as people type (no Save button); the server records one
 * history entry and sends notifications once the edits have gone quiet for
 * two minutes. When co-editing is unavailable the component keeps upstream's
 * click-to-edit view with its Save and Cancel buttons.
 *
 * @param {{ item: () => any, editing: () => boolean, alwaysOn?: () => boolean }} options
 */
export function useLiveItemDescription({ item, editing, alwaysOn = () => false }) {
  const doc = useCollabDocument(() => {
    const current = item();
    return current?.id && (editing() || alwaysOn()) ? { kind: 'item', id: current.id } : null;
  });
  const active = $derived(doc.mode === 'live' && !!doc.session);
  // Until the session is live, the stored description shows as upstream's
  // read-only view (visually the same), so an unavailable server never
  // flashes an editor.
  const alwaysOnNow = $derived(alwaysOn() && active);
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
    /** The live editor is shown without entering edit mode. */
    get alwaysOn() {
      return alwaysOnNow;
    },
    /**
     * Keyboard handling for an always-on editor: the save and cancel
     * shortcuts leave the text (store pending edits, drop focus) instead of
     * saving or cancelling anything. Returns whether the event was consumed.
     * @param {KeyboardEvent} event
     */
    leave(event) {
      if (!alwaysOnNow) return false;
      if (!matchesShortcut(event, saveShortcut) && !matchesShortcut(event, cancelShortcut))
        return false;
      event.preventDefault();
      // The first Escape leaves the text; a second one reaches the modal.
      event.stopPropagation();
      void this.flush();
      const focused = /** @type {HTMLElement | null} */ (document.activeElement);
      focused?.blur?.();
      return true;
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

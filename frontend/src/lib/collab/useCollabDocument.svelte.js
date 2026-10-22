import { untrack } from 'svelte';
import { authStore } from '../stores/auth.svelte.js';
import { collabColor } from './collabPresence.js';

let statusRequest = null;

/**
 * Resolve the document kinds this server co-edits. The route is absent when
 * co-editing is disabled, which resolves to an empty list.
 * @returns {Promise<string[]>}
 */
export function collabKinds() {
  if (!statusRequest) {
    statusRequest = fetch('/api/collab/status', {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
      .then(async (response) => {
        if (!response.ok) return [];
        const body = await response.json();
        return body?.enabled && Array.isArray(body.kinds) ? body.kinds : [];
      })
      .catch(() => {
        // A transient failure should not disable co-editing for the session.
        statusRequest = null;
        return [];
      });
  }
  return statusRequest;
}

/**
 * Open a co-editing session for the document returned by getTarget, replacing
 * it when the target changes.
 *
 * `mode` drives the caller's editor:
 *   - 'off'      no document
 *   - 'pending'  deciding; show the stored markdown read-only
 *   - 'live'     connected at least once; bind the editor to the session
 *   - 'fallback' co-editing unavailable; use the regular autosave editor
 *
 * @param {() => ({ kind: string, id: number|string } | null)} getTarget
 */
export function useCollabDocument(getTarget) {
  let session = $state.raw(null);
  let state = $state.raw(null);
  let mode = $state('off');

  const target = $derived(getTarget());
  const key = $derived(target ? `${target.kind}:${target.id}` : '');

  $effect(() => {
    if (!key) {
      mode = 'off';
      return;
    }
    // Only the key re-opens the session; a new target object for the same
    // document must not.
    const { kind, id } = untrack(() => target);
    let cancelled = false;
    let created = null;
    let unsubscribe = null;
    mode = 'pending';

    void import('./collabSession.js').then(async ({ CollabSession }) => {
      const kinds = await collabKinds();
      if (cancelled) return;
      if (!kinds.includes(kind) || typeof WebSocket === 'undefined') {
        mode = 'fallback';
        return;
      }
      const user = authStore.currentUser;
      created = new CollabSession({
        kind,
        id,
        user: user ? { name: displayName(user), color: collabColor(user.id) } : null,
      });
      unsubscribe = created.subscribe((next) => {
        state = next;
        if (next.synced) mode = 'live';
        else if (next.status === 'failed') mode = 'fallback';
      });
      session = created;
      created.connect();
    });

    return () => {
      cancelled = true;
      unsubscribe?.();
      created?.destroy();
      session = null;
      state = null;
    };
  });

  return {
    get session() {
      return session;
    },
    get state() {
      return state;
    },
    get mode() {
      return mode;
    },
  };
}

function displayName(user) {
  const name = `${user.first_name || ''} ${user.last_name || ''}`.trim();
  return name || user.username || '';
}

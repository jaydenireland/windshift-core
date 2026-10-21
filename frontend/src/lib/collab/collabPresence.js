// Cursor and presence colors come from the theme's accent text tokens so they
// follow light/dark mode and custom themes.
const PALETTE = [
  'var(--ds-text-accent-blue)',
  'var(--ds-text-accent-purple)',
  'var(--ds-text-accent-teal)',
  'var(--ds-text-accent-orange)',
  'var(--ds-text-accent-green)',
  'var(--ds-text-accent-red)',
  'var(--ds-text-accent-yellow)',
];

/** Stable color for a user. */
export function collabColor(userId) {
  const n = Number(userId) || 0;
  return PALETTE[Math.abs(n) % PALETTE.length];
}

/**
 * Remote-cursor options for y-prosemirror. Labels come from the server's peer
 * list (keyed by Yjs client id) rather than the self-reported awareness name,
 * so a client cannot impersonate someone else's cursor.
 *
 * @param {() => Array<{ client_id?: number, user_id: number, name: string }>} getPeers
 */
export function collabCursorOptions(getPeers) {
  const identify = (user, clientId) => {
    const peer = getPeers().find((p) => p.client_id === clientId);
    return {
      name: peer?.name || user?.name || '',
      color: peer ? collabColor(peer.user_id) : user?.color || PALETTE[0],
    };
  };
  return {
    cursorBuilder: (user, clientId) => {
      const { name, color } = identify(user, clientId);
      const cursor = document.createElement('span');
      cursor.classList.add('ProseMirror-yjs-cursor');
      cursor.style.setProperty('--collab-color', color);
      const label = document.createElement('div');
      label.textContent = name;
      cursor.insertBefore(document.createTextNode('⁠'), null);
      cursor.insertBefore(label, null);
      cursor.insertBefore(document.createTextNode('⁠'), null);
      return cursor;
    },
    selectionBuilder: (user, clientId) => {
      const { color } = identify(user, clientId);
      return {
        class: 'ProseMirror-yjs-selection',
        style: `--collab-color: ${color}`,
      };
    },
  };
}

/** Distinct people in a peer list (one entry per user, first connection wins). */
export function uniquePeople(peers, selfUserId) {
  const seen = new Set();
  const people = [];
  for (const peer of peers || []) {
    if (peer.user_id === selfUserId || seen.has(peer.user_id)) continue;
    seen.add(peer.user_id);
    people.push(peer);
  }
  return people;
}

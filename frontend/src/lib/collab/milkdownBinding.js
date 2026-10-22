import { defaultValueCtx, parserCtx, schemaCtx, serializerCtx } from '@milkdown/kit/core';
import { collabCursorOptions } from './collabPresence.js';
import './collab.css';

// Milkdown's listener debounces markdown updates by 200ms.
const LISTENER_SETTLE_MS = 250;

/**
 * Prepare a Milkdown editor's co-editing binding, or null without a session.
 *
 * The editor always lends the session its markdown codec (the leader needs it
 * to seed, checkpoint and merge). When the shared document is already seeded
 * the editor is also bound to it, via @milkdown/plugin-collab (y-prosemirror),
 * and shows it instead of its `content`. The binding is fixed for the editor's
 * lifetime; callers re-key the editor when it should change.
 *
 * Kept outside MilkdownEditor.svelte so the editor only carries call sites.
 *
 * @param {import('./collabSession.js').CollabSession | null} session
 */
export async function prepareCollabBinding(session) {
  if (!session) return null;
  const bound = !!session.state?.seeded;
  const plugin = bound ? await import('@milkdown/plugin-collab') : null;
  let codec = null;
  let attached = null;
  return {
    /** Whether the editor shows the shared document. */
    bound,
    /** Call from the editor's config, after it sets the default value. */
    configure(ctx) {
      if (bound) ctx.set(defaultValueCtx, '');
    },
    /** Call before builder.create(). */
    use(builder) {
      if (plugin) builder.use(plugin.collab);
    },
    /** Call once the editor is created. */
    attach(editor) {
      editor.action((ctx) => {
        if (plugin) {
          ctx
            .get(plugin.collabServiceCtx)
            .bindDoc(session.doc)
            .setAwareness(session.awareness)
            .setOptions({ yCursorOpts: collabCursorOptions(() => session.state.peers) })
            .connect();
        }
        codec = {
          schema: ctx.get(schemaCtx),
          parse: ctx.get(parserCtx),
          serialize: ctx.get(serializerCtx),
        };
      });
      session.attachCodec(codec);
      attached = editor;
    },
    /**
     * Call when the editor is destroyed; await it before destroying the
     * editor. Milkdown's listener serializes each change after a debounce,
     * reading the editor view, and a bound editor keeps receiving remote
     * changes up to this point. Destroying the editor inside that window
     * throws, so the editor outlives the last scheduled run.
     */
    detach() {
      if (plugin && attached) {
        // Stop the shared document from driving a view that is going away.
        try {
          attached.action((ctx) => ctx.get(plugin.collabServiceCtx).disconnect());
        } catch {
          // The editor is already torn down.
        }
      }
      attached = null;
      // The codec stays lent until the editor goes, so a session closing at
      // the same time can still store its final checkpoint.
      return new Promise((resolve) =>
        setTimeout(() => {
          if (codec) session.detachCodec(codec);
          codec = null;
          resolve();
        }, LISTENER_SETTLE_MS)
      );
    },
  };
}

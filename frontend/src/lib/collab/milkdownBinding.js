import { defaultValueCtx, parserCtx, schemaCtx, serializerCtx } from '@milkdown/kit/core';
import { collabCursorOptions } from './collabPresence.js';
import './collab.css';

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
    },
    /** Call when the editor is destroyed. */
    detach() {
      if (codec) session.detachCodec(codec);
    },
  };
}

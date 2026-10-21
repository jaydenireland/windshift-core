// Vite settings for live co-editing (src/lib/collab), contributed as a plugin
// so vite.config.js only carries one line for it. Vite deep-merges what
// `config` returns into the main config.
export function collabVitePlugin() {
  return {
    name: 'windshift-collab',
    config() {
      return {
        optimizeDeps: {
          // The co-editing binding plugs into the editor, so it must resolve
          // Milkdown and ProseMirror exactly as the editor does: a pre-bundled
          // copy would carry its own @milkdown/core and prosemirror-state.
          exclude: ['@milkdown/plugin-collab', 'y-prosemirror'],
        },
        server: {
          // The co-editing socket lives under /api.
          proxy: { '/api': { ws: true } },
        },
      };
    },
  };
}

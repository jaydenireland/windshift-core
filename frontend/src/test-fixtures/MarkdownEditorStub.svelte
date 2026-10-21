<script>
  // Stands in for LazyMilkdownEditor with the same content contract: an
  // editable editor reads `content` once when it mounts and ignores later
  // prop changes, while edits flow back through `bind:content`. Readonly
  // renders follow `content`.
  let { content = $bindable(''), readonly = false } = $props();

  // svelte-ignore state_referenced_locally
  let doc = $state(content || '');
  let textarea = $state(null);

  export function focus() {
    textarea?.focus();
  }

  export function focusEnd() {
    textarea?.focus();
  }

  export function insertImage() {}
</script>

{#if readonly}
  <div data-testid="markdown-view">{content}</div>
{:else}
  <textarea
    bind:this={textarea}
    data-testid="markdown-editor"
    value={doc}
    oninput={(event) => {
      doc = event.currentTarget.value;
      content = doc;
    }}
  ></textarea>
{/if}

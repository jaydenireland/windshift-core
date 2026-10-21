<!--
  Word-level rendering of one text change in item history. Fork-owned (see
  FORK.md). Upstream's ItemHistory.svelte delegates here through one branch
  guarded by `historyDiffApplies`, re-exported from this module so the seam
  is a single import.
-->
<script module>
  export { historyDiffApplies } from './wordDiff.js';
</script>

<script>
  import { collapseDiff, diffWords } from './wordDiff.js';
  import { historyDiffText } from './strings.js';

  let { oldText = '', newText = '' } = $props();

  let expanded = $state(false);

  const full = $derived(diffWords(oldText ?? '', newText ?? ''));
  const collapsed = $derived(collapseDiff(full));
  const collapsible = $derived(collapsed.some((seg) => seg.op === 'skip'));
  const shown = $derived(expanded ? full : collapsed);
</script>

<div class="history-diff" data-testid="item-history-diff">
  {#each shown as seg, i (i)}
    {#if seg.op === 'skip'}
      <button type="button" class="history-diff__skip" onclick={() => (expanded = true)}>
        {historyDiffText('unchanged', seg.words)}
      </button>
    {:else if seg.op === 'insert'}
      <ins class="history-diff__ins" title={historyDiffText('added')}>{seg.text}</ins>
    {:else if seg.op === 'delete'}
      <del class="history-diff__del" title={historyDiffText('removed')}>{seg.text}</del>
    {:else}
      <span>{seg.text}</span>
    {/if}
  {/each}
  {#if expanded && collapsible}
    <button type="button" class="history-diff__skip" onclick={() => (expanded = false)}>
      {historyDiffText('showLess')}
    </button>
  {/if}
</div>

<style>
  /* Takes the full row under "changed description" inside the flex-wrapped
     `.change` line of ItemHistory.svelte. */
  .history-diff {
    flex-basis: 100%;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-size: 0.8125rem;
    line-height: 1.5;
    color: var(--ds-text);
    padding: 0.375rem 0.5rem;
    border-radius: 0.375rem;
    background: var(--ds-surface-sunken, var(--ds-surface-hovered));
    max-height: 24rem;
    overflow-y: auto;
  }

  .history-diff__ins {
    text-decoration: none;
    background: var(--ds-success-subtle);
    color: var(--ds-text);
    border-radius: 2px;
  }

  .history-diff__del {
    background: var(--ds-danger-subtle);
    color: var(--ds-text-subtle);
    border-radius: 2px;
  }

  .history-diff__skip {
    display: block;
    width: 100%;
    margin: 0.25rem 0;
    padding: 0.125rem 0.375rem;
    border: 1px dashed var(--ds-border);
    border-radius: 0.25rem;
    background: transparent;
    color: var(--ds-text-subtle);
    font: inherit;
    font-size: 0.75rem;
    text-align: center;
    cursor: pointer;
    white-space: normal;
  }

  .history-diff__skip:hover {
    background: var(--ds-surface-hovered);
  }
</style>

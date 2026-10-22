<script>
  import { collabText } from '../strings.js';

  /** Live-editing notices shown above the page. */
  let { live } = $props();
</script>

{#if live.active && live.state?.closeReason === 'gone'}
  <div class="live-notice live-notice--error" role="alert" data-testid="page-live-gone">
    {collabText('gone')}
  </div>
{:else if live.active && live.state?.mergeWarning}
  <div class="live-notice" role="status" data-testid="page-live-merge-warning">
    {collabText('mergeWarning')}
  </div>
{/if}

<style>
  .live-notice {
    margin: 0.5rem var(--page-gutter, 1rem) 0;
    padding: 0.5rem 0.75rem;
    border: 1px solid var(--ds-status-warning-border);
    border-radius: 0.375rem;
    background: var(--ds-status-warning-bg);
    color: var(--ds-status-warning-text);
    font-size: 0.8125rem;
  }

  .live-notice--error {
    border-color: transparent;
    background: var(--ds-status-danger-bg);
    color: var(--ds-text-danger);
  }
</style>

<script>
  import Button from '../../components/Button.svelte';
  import LivePageStatus from '../pages/LivePageStatus.svelte';
  import { collabText } from '../strings.js';

  /**
   * Replaces the description's Save and Cancel while it is edited live. With
   * an always-on editor there is nothing to finish, so only the live status
   * and who else is here remain.
   */
  let { live, ondone } = $props();
</script>

{#if live.state?.closeReason === 'gone'}
  <div class="live-notice live-notice--error" role="alert" data-testid="item-description-live-gone">
    {collabText('itemGone')}
  </div>
{:else if live.state?.mergeWarning}
  <div class="live-notice" role="status" data-testid="item-description-live-merge-warning">
    {collabText('itemMergeWarning')}
  </div>
{/if}
<div class="live-bar" class:live-bar--always-on={live.alwaysOn}>
  {#if !live.alwaysOn}
    <Button variant="default" onclick={ondone} dataTestid="item-description-done">
      {collabText('done')}
    </Button>
    <span class="live-bar__hint">{collabText('autosaves')}</span>
  {/if}
  <span class="live-bar__status">
    <LivePageStatus
      {live}
      testidPrefix="item-description"
      peopleLabel={collabText('itemPeopleAria')}
    />
  </span>
</div>

<style>
  .live-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem 0.75rem;
  }

  .live-bar--always-on {
    min-height: 1.5rem;
    margin-top: -0.25rem;
  }

  .live-bar__hint {
    font-size: 0.75rem;
    color: var(--ds-text-subtle);
  }

  .live-bar__status {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    margin-left: auto;
  }

  .live-notice {
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

<script>
  import Tooltip from '../../components/Tooltip.svelte';
  import { collabColor } from '../collabPresence.js';
  import { collabText } from '../strings.js';

  /** Live indicator and who else is on the page, for the page toolbar. */
  let { live } = $props();

  let label = $derived.by(() => {
    if (!live.active) return live.pending ? collabText('connecting') : '';
    switch (live.state?.status) {
      case 'open':
        return collabText('connected');
      case 'closed':
        return collabText('closed');
      default:
        return collabText('reconnecting');
    }
  });
</script>

{#if label}
  <span
    class="live-status"
    class:live-status--offline={live.active && live.state?.status !== 'open'}
    data-testid="page-live-status"
    data-status={live.active ? live.state?.status : 'pending'}
  >
    <span class="live-status__dot" aria-hidden="true"></span>
    {label}
  </span>
{/if}
{#if live.people.length > 0}
  <div class="live-people" data-testid="page-live-people" aria-label={collabText('peopleAria')}>
    {#each live.people.slice(0, 5) as person (person.user_id)}
      <Tooltip content={person.name} placement="bottom">
        <span
          class="live-person"
          style="--collab-color: {collabColor(person.user_id)}"
          data-testid="page-live-person"
          data-user-id={person.user_id}
        >
          {#if person.avatar_url}
            <img src={person.avatar_url} alt="" />
          {:else}
            {(person.name || '?').slice(0, 1).toUpperCase()}
          {/if}
        </span>
      </Tooltip>
    {/each}
    {#if live.people.length > 5}
      <span class="live-person live-person--more">+{live.people.length - 5}</span>
    {/if}
  </div>
{/if}

<style>
  .live-status {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    font-size: 0.75rem;
    color: var(--ds-text-subtle);
  }

  .live-status__dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 9999px;
    background: var(--ds-icon-success);
  }

  .live-status--offline .live-status__dot {
    background: var(--ds-icon-warning);
  }

  .live-people {
    display: inline-flex;
    align-items: center;
  }

  .live-person {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.5rem;
    height: 1.5rem;
    margin-left: -0.25rem;
    border-radius: 9999px;
    border: 2px solid var(--collab-color, var(--ds-border));
    background: var(--ds-surface-raised);
    color: var(--ds-text);
    font-size: 0.6875rem;
    font-weight: 600;
    overflow: hidden;
  }

  .live-person img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }

  .live-person--more {
    border-color: var(--ds-border);
    color: var(--ds-text-subtle);
  }
</style>

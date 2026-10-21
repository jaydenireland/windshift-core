<script>
  import { untrack } from 'svelte';
  import { Check, X } from '@lucide/svelte';
  import Button from "../../components/Button.svelte";
  import Input from '../../components/Input.svelte';
  import { errorToast } from '../../stores/toasts.svelte.js';
  import { t } from '../../stores/i18n.svelte.js';

  let {
    item,
    workspace,
    editingTitle = false,
    editTitle = '',
    saving = false,
    canEdit = false,
    onstartEditingTitle = undefined,
    onsavefield = undefined,
    oncanceledit = undefined,
  } = $props();

  // The open input's draft, and the title it started from. Seeded once per
  // editing session: store updates while the input is open (another field's
  // edit, a live reload) must not reset what the user typed.
  let titleDraft = $state('');
  let titleBase = '';
  let wasEditingTitle = false;

  $effect.pre(() => {
    const isEditing = editingTitle;
    if (isEditing && !wasEditingTitle) {
      titleBase = untrack(() => editTitle) || '';
      titleDraft = titleBase;
    }
    wasEditingTitle = isEditing;
  });

  function startEditingTitle() {
    onstartEditingTitle?.();
  }

  function cancelEditingTitle() {
    oncanceledit?.({ field: "title" });
  }

  function validateAndSaveTitle() {
    const trimmedTitle = titleDraft.trim();

    if (!trimmedTitle) {
      // Show error toast
      errorToast(t('items.previousValueRemains'), t('items.titleCannotBeEmpty'));

      // Revert to original title
      cancelEditingTitle();
      return;
    }

    // An untouched title is not an edit: saving its starting text would revert
    // a rename made elsewhere while the input was open.
    if (trimmedTitle === titleBase.trim()) {
      cancelEditingTitle();
      return;
    }

    onsavefield?.({ field: "title", value: trimmedTitle });
  }

  function handleKeydown(event) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      validateAndSaveTitle();
    } else if (event.key === "Escape") {
      event.preventDefault();
      cancelEditingTitle();
    }
  }

</script>

<div class="mb-8 w-full max-w-full">
  <div class="w-full min-w-0 overflow-hidden max-w-full">
    <div class="mb-2">
      {#if editingTitle}
        <div class="flex items-center gap-3 w-full pr-4 ">
          <!-- Item key (in edit mode) -->

          <div class="min-w-[80%]">
            <Input
              type="text"
              variant="ghost"
              dataTestid="item-title-input"
              bind:value={titleDraft}
              onkeydown={handleKeydown}
              class="w-full text-2xl font-semibold bg-transparent border-0 py-1 focus:outline-none break-words"
              style="color: var(--ds-text); word-wrap: break-word; overflow-wrap: break-word;"
              placeholder={t('items.enterTitle')}
              autofocus
            />
          </div>
          <div class="flex gap-2 mt-2 hidden">
            <Button
              variant="primary"
              size="small"
              icon={Check}
              onclick={validateAndSaveTitle}
              disabled={saving}
            />
            <Button
              variant="default"
              size="small"
              icon={X}
              onclick={cancelEditingTitle}
            />
          </div>
        </div>
      {:else}
        <!-- Item key -->

        {#if canEdit}
          <button
            onclick={startEditingTitle}
            data-testid="item-title-edit"
            class="text-2xl font-semibold pr-4 py-1 rounded transition-colors text-left cursor-pointer w-full title-button break-words"
            style="color: var(--ds-text); word-wrap: break-word; overflow-wrap: break-word;"
            title={t('items.clickToEditTitle')}
          >
            {item.title}
          </button>
        {:else}
          <!-- Read-only title for users without item.edit (server enforces the
               PATCH; the UI simply must not offer the affordance). -->
          <h1
            class="text-2xl font-semibold pr-4 py-1 text-left w-full break-words"
            style="color: var(--ds-text); word-wrap: break-word; overflow-wrap: break-word;"
            data-testid="item-title-readonly"
          >
            {item.title}
          </h1>
        {/if}
      {/if}
    </div>
  </div>
</div>


<style>
  .title-button:hover {
    background-color: var(--ds-surface);
  }
</style>

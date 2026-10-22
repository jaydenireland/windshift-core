<script>
  import Checkbox from '../../components/Checkbox.svelte';
  import DialogFooter from '../../dialogs/DialogFooter.svelte';
  import Modal from '../../dialogs/Modal.svelte';
  import ModalHeader from '../../dialogs/ModalHeader.svelte';
  import Milestone from '@lucide/svelte/icons/milestone';
  import { t } from '../../stores/i18n.svelte.js';
  import { formatDateOnly } from '../../utils/dateFormatter.js';

  let {
    isOpen = false,
    globalMilestones = [],
    selectedGlobalMilestoneIds = [],
    showCompleted = true,
    saving = false,
    onclose = null,
    onsave = null,
  } = $props();

  let draftIds = $state(new Set());
  let draftShowCompleted = $state(true);
  let wasOpen = false;

  $effect(() => {
    if (isOpen && !wasOpen) {
      draftIds = new Set((selectedGlobalMilestoneIds || []).map(Number));
      draftShowCompleted = showCompleted !== false;
    }
    wasOpen = isOpen;
  });

  const selectedCount = $derived(draftIds.size);

  const hasChanges = $derived.by(() => {
    if (draftShowCompleted !== (showCompleted !== false)) return true;
    const current = new Set((selectedGlobalMilestoneIds || []).map(Number));
    if (current.size !== draftIds.size) return true;
    for (const id of draftIds) {
      if (!current.has(id)) return true;
    }
    return false;
  });

  function toggle(id) {
    const next = new Set(draftIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    draftIds = next;
  }

  function close() {
    if (!saving) onclose?.();
  }

  function save() {
    if (saving || !hasChanges) return;
    onsave?.({
      globalMilestoneIds: [...draftIds],
      showCompleted: draftShowCompleted,
    });
  }

  const isInactive = (milestone) => {
    const status = String(milestone?.status || '').toLowerCase();
    return status === 'completed' || status === 'cancelled';
  };
</script>

<Modal
  {isOpen}
  preventClose={saving}
  closeOnBackdropClick={false}
  maxWidth="max-w-lg"
  onclose={close}
  onSubmit={save}
  submitDisabled={!hasChanges}
  dataTestid="milestone-swimlane-config-dialog"
>
  <ModalHeader
    title={t('boardControls.configureMilestones')}
    subtitle={t('boardControls.configureMilestonesDescription')}
    icon={Milestone}
    onclose={close}
  />

  <div class="space-y-5 px-6 py-5">
    <div
      class="rounded-lg border p-4"
      style="border-color: var(--ds-border); background-color: var(--ds-surface);"
    >
      <Checkbox
        id="milestone-swimlanes-show-completed"
        checked={draftShowCompleted}
        onchange={(checked) => (draftShowCompleted = checked)}
        label={t('boardControls.showCompletedMilestones')}
        hint={t('boardControls.showCompletedMilestonesHint')}
        dataTestid="milestone-swimlanes-show-completed"
      />
    </div>

    <div>
      <div class="mb-2 flex items-center justify-between gap-2">
        <span class="text-sm font-medium" style="color: var(--ds-text);">
          {t('boardControls.globalMilestones')}
        </span>
        <span class="text-xs" style="color: var(--ds-text-subtle);">
          {t('boardControls.globalMilestonesSelected', { count: selectedCount })}
        </span>
      </div>
      <p class="mb-3 text-xs" style="color: var(--ds-text-subtle);">
        {t('boardControls.globalMilestonesHint')}
      </p>
      {#if globalMilestones.length === 0}
        <p
          class="rounded-md border px-3 py-2 text-sm"
          style="border-color: var(--ds-border); color: var(--ds-text-subtle);"
          data-testid="milestone-swimlanes-no-globals"
        >
          {t('boardControls.noGlobalMilestones')}
        </p>
      {:else}
        <div
          class="max-h-64 space-y-0.5 overflow-y-auto rounded-md border p-1"
          style="border-color: var(--ds-border);"
          data-testid="milestone-swimlanes-global-list"
        >
          {#each globalMilestones as milestone (milestone.id)}
            <div class="flex items-center gap-2">
              <Checkbox
                checked={draftIds.has(Number(milestone.id))}
                onchange={() => toggle(Number(milestone.id))}
                label={milestone.name}
                hint={milestone.target_date ? formatDateOnly(milestone.target_date) : null}
                class="min-w-0 flex-1"
                dataTestid={`milestone-swimlanes-global-${milestone.id}`}
              />
              {#if isInactive(milestone)}
                <span class="shrink-0 text-xs" style="color: var(--ds-text-subtle);">
                  {milestone.status}
                </span>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>

  <DialogFooter
    cancelLabel={t('dialogs.cancel')}
    confirmLabel={t('common.save')}
    loadingLabel={t('common.saving')}
    loading={saving}
    confirmDisabled={!hasChanges}
    onCancel={close}
    onConfirm={save}
    showKeyboardHint={true}
    cancelTestid="milestone-swimlanes-cancel"
    confirmTestid="milestone-swimlanes-save"
  />
</Modal>

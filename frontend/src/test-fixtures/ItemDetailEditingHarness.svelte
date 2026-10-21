<script>
  // Wires ItemDetailContent to the real itemDetailStore the way ItemDetail
  // does for the title and description editing paths.
  import ItemDetailContent from '../lib/features/items/ItemDetailContent.svelte';
  import { itemDetailStore } from '../lib/stores/itemDetailStore.svelte.js';

  function startEditing(field) {
    itemDetailStore.editing.customFields.active = {};
    itemDetailStore.startEditing(field);
  }
</script>

<ItemDetailContent
  loading={itemDetailStore.loading}
  error={itemDetailStore.error}
  item={itemDetailStore.item}
  workspace={itemDetailStore.workspace}
  canEdit={true}
  saving={itemDetailStore.saving}
  editingTitle={itemDetailStore.editing.title.active}
  editTitle={itemDetailStore.editing.title.value}
  editingDescription={itemDetailStore.editing.description.active}
  editDescription={itemDetailStore.editing.description.value}
  onsaveField={({ field, value }) => itemDetailStore.saveField(field, value)}
  oncancelEdit={({ field }) => itemDetailStore.cancelEditing(field)}
  onstartEditingTitle={() => startEditing('title')}
  onstartEditingDescription={() => startEditing('description')}
/>

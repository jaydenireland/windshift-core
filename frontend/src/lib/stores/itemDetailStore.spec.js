/** @vitest-environment jsdom */
// jsdom makes Vitest compile the store for the browser, where `$state` is a
// reactive proxy; the node environment compiles it as plain server objects.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { itemDetailStore } from './itemDetailStore.svelte.js';

const WORKSPACE_ID = 7;

const mocks = vi.hoisted(() => ({
  server: new Map(),
  getDetailSummary: vi.fn(),
  getItem: vi.fn(),
  getWorkspace: vi.fn(),
}));

vi.mock('../api.js', () => ({
  api: {
    items: { getDetailSummary: mocks.getDetailSummary, get: mocks.getItem },
    workspaces: { get: mocks.getWorkspace },
  },
}));
vi.mock('./workspaceDataStore.svelte.js', () => ({
  workspaceDataStore: {
    initialized: false,
    initialize: async () => {},
    screenConfig: async () => null,
  },
}));
vi.mock('./workspaces.svelte.js', () => ({ workspacesStore: { load: async () => [] } }));

function putOnServer(id, fields) {
  mocks.server.set(id, {
    id,
    workspace_id: WORKSPACE_ID,
    title: `Item ${id}`,
    description: `Description of item ${id}`,
    priority_id: 1,
    custom_field_values: { 5: 'stored five', 6: 'stored six' },
    ...mocks.server.get(id),
    ...fields,
  });
}

function loadItem(id) {
  return itemDetailStore.loadItem(WORKSPACE_ID, id);
}

describe('itemDetailStore editing state across reloads', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.server.clear();
    mocks.getDetailSummary.mockImplementation(async (id) => ({
      item: structuredClone(mocks.server.get(Number(id))),
    }));
    mocks.getItem.mockImplementation(async (id) => structuredClone(mocks.server.get(Number(id))));
    mocks.getWorkspace.mockResolvedValue({ id: WORKSPACE_ID, key: 'WI' });
  });

  afterEach(() => {
    itemDetailStore.reset();
  });

  it('keeps the values of fields being edited when the open item reloads', async () => {
    putOnServer(42);
    await loadItem(42);
    itemDetailStore.startEditing('title');
    itemDetailStore.startEditing('description');
    itemDetailStore.startEditing('custom_field_5');
    itemDetailStore.editing.customFields.values[5] = 'typed five';

    putOnServer(42, {
      title: 'Renamed elsewhere',
      description: 'Changed elsewhere',
      priority_id: 3,
      custom_field_values: { 5: 'server five', 6: 'server six' },
    });
    await loadItem(42);

    const { editing, item } = itemDetailStore;
    expect(editing.title).toEqual({ active: true, value: 'Item 42' });
    expect(editing.description).toEqual({ active: true, value: 'Description of item 42' });
    expect(editing.customFields.active[5]).toBe(true);
    expect(editing.customFields.values[5]).toBe('typed five');
    // Fields nobody is editing follow the server, and so does the item itself.
    expect(editing.customFields.values[6]).toBe('server six');
    expect(editing.priority.value).toBe(3);
    expect(item.title).toBe('Renamed elsewhere');
    expect(item.description).toBe('Changed elsewhere');
  });

  it('ends the edits on the previous item when another item loads', async () => {
    putOnServer(42);
    putOnServer(43);
    await loadItem(42);
    itemDetailStore.startEditing('description');
    itemDetailStore.startEditing('custom_field_5');

    await loadItem(43);

    const { editing } = itemDetailStore;
    expect(editing.description).toEqual({ active: false, value: 'Description of item 43' });
    expect(editing.customFields.active).toEqual({});
    expect(editing.customFields.values[5]).toBe('stored five');
  });

  it('starts text edits from an empty string when the item has none', async () => {
    putOnServer(42, { description: null });
    await loadItem(42);

    itemDetailStore.startEditing('description');
    expect(itemDetailStore.editing.description.value).toBe('');
    await loadItem(42);

    expect(itemDetailStore.editing.description).toEqual({ active: true, value: '' });
  });

  it('takes the latest text after an agent run without touching drafts', async () => {
    putOnServer(42);
    await loadItem(42);
    itemDetailStore.startEditing('description');
    itemDetailStore.startEditing('priority');
    itemDetailStore.startEditing('custom_field_5');
    itemDetailStore.editing.customFields.values[5] = 'typed five';

    putOnServer(42, {
      description: 'Rewritten by an agent',
      priority_id: 3,
      custom_field_values: { 5: 'agent five', 6: 'agent six' },
    });
    await itemDetailStore.refreshCurrentItem();

    const { editing, item } = itemDetailStore;
    expect(item.description).toBe('Rewritten by an agent');
    expect(editing.description).toEqual({ active: true, value: 'Description of item 42' });
    expect(editing.customFields.values[5]).toBe('typed five');
    // Pickers and custom fields being edited keep their value on the item.
    expect(item.priority_id).toBe(1);
    expect(item.custom_field_values[5]).toBe('stored five');
    expect(item.custom_field_values[6]).toBe('agent six');
  });
});

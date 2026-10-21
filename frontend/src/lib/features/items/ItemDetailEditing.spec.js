/** @vitest-environment jsdom */

import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ItemDetailEditingHarness from '../../../test-fixtures/ItemDetailEditingHarness.svelte';
import { itemDetailStore } from '../../stores/itemDetailStore.svelte.js';

const ITEM_ID = 42;
const WORKSPACE_ID = 7;

const mocks = vi.hoisted(() => ({
  server: { item: null },
  getDetailSummary: vi.fn(),
  getWorkspace: vi.fn(),
  update: vi.fn(),
}));

vi.mock('../../api.js', () => ({
  api: {
    items: { getDetailSummary: mocks.getDetailSummary, update: mocks.update },
    workspaces: { get: mocks.getWorkspace },
  },
}));
vi.mock('../../stores', () => ({
  aiStore: { available: false },
  attachmentStatus: { enabled: false },
}));
vi.mock('../../stores/workspaceDataStore.svelte.js', () => ({
  workspaceDataStore: {
    initialized: false,
    initialize: async () => {},
    screenConfig: async () => null,
  },
}));
vi.mock('../../stores/workspaces.svelte.js', () => ({ workspacesStore: { load: async () => [] } }));
vi.mock('../../stores/i18n.svelte.js', () => ({ t: (key) => key }));
vi.mock('../../stores/toasts.svelte.js', () => ({ errorToast: vi.fn(), successToast: vi.fn() }));
vi.mock('../../composables/useConfirm.js', () => ({ confirm: vi.fn() }));
vi.mock(
  '../../editors/LazyMilkdownEditor.svelte',
  () => import('../../../test-fixtures/MarkdownEditorStub.svelte')
);
// The panels around the header and description are out of scope here.
vi.mock('./ItemDetailBreadcrumbs.svelte', () => import('../../../test-fixtures/EmptyStub.svelte'));
vi.mock('./ItemIncidentPanel.svelte', () => import('../../../test-fixtures/EmptyStub.svelte'));
vi.mock('./ItemSLAPanel.svelte', () => import('../../../test-fixtures/EmptyStub.svelte'));
vi.mock('./ItemDetailLinks.svelte', () => import('../../../test-fixtures/EmptyStub.svelte'));
vi.mock('./ItemDetailTabs.svelte', () => import('../../../test-fixtures/EmptyStub.svelte'));
vi.mock('./ItemDetailSidebar.svelte', () => import('../../../test-fixtures/EmptyStub.svelte'));
vi.mock(
  '../../layout/SidebarResizeHandle.svelte',
  () => import('../../../test-fixtures/EmptyStub.svelte')
);
vi.mock(
  '../assets/AttachmentDiagramList.svelte',
  () => import('../../../test-fixtures/EmptyStub.svelte')
);

function serverItem(overrides = {}) {
  return {
    id: ITEM_ID,
    workspace_id: WORKSPACE_ID,
    workspace_key: 'WI',
    workspace_item_number: 1,
    title: 'Original title',
    description: 'Original description',
    custom_field_values: {},
    ...overrides,
  };
}

// Another client saves the item; the next reload returns the change.
function changeOnServer(changes) {
  mocks.server.item = { ...mocks.server.item, ...changes };
}

// The full reload that the live-update (SSE) reconcile, `reload-item-detail`,
// approval changes and the desktop focus refresh all run.
function reloadItem() {
  return itemDetailStore.loadItem(WORKSPACE_ID, ITEM_ID);
}

async function openItem(overrides) {
  mocks.server.item = serverItem(overrides);
  await reloadItem();
  render(ItemDetailEditingHarness);
}

async function openDescriptionEditor() {
  await fireEvent.click(
    screen.queryByTestId('item-description-display') ?? screen.getByTestId('item-description-empty')
  );
  return screen.findByTestId('markdown-editor');
}

async function typeDescriptionDraft(text) {
  await fireEvent.input(await openDescriptionEditor(), { target: { value: text } });
}

async function typeTitleDraft(text) {
  await fireEvent.click(screen.getByTestId('item-title-edit'));
  await fireEvent.input(screen.getByTestId('item-title-input'), { target: { value: text } });
}

function saveDescription() {
  return fireEvent.click(screen.getByTestId('item-description-save'));
}

function expectDescriptionSaved(description) {
  return waitFor(() => expect(mocks.update).toHaveBeenCalledWith(ITEM_ID, { description }));
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getDetailSummary.mockImplementation(async () => ({
    item: structuredClone(mocks.server.item),
  }));
  mocks.getWorkspace.mockResolvedValue({ id: WORKSPACE_ID, key: 'WI', name: 'Work' });
  mocks.update.mockImplementation(async (_id, changes) => {
    changeOnServer(changes);
    return structuredClone(mocks.server.item);
  });
});

afterEach(() => {
  cleanup();
  itemDetailStore.reset();
});

describe('item detail description editing', () => {
  it('keeps the draft when a sidebar field opens and closes its editor', async () => {
    await openItem();
    await typeDescriptionDraft('My draft');

    itemDetailStore.startEditing('status');
    await tick();
    itemDetailStore.cancelEditing('status');
    await tick();

    expect(screen.getByTestId('markdown-editor')).toHaveValue('My draft');
    await saveDescription();
    await expectDescriptionSaved('My draft');
  });

  it('keeps the draft when a title edit is started and cancelled', async () => {
    await openItem();
    await typeDescriptionDraft('My draft');

    await typeTitleDraft('Abandoned title');
    await fireEvent.keyDown(screen.getByTestId('item-title-input'), { key: 'Escape' });
    await tick();

    await saveDescription();
    await expectDescriptionSaved('My draft');
  });

  it('keeps the draft after another field is saved', async () => {
    await openItem();
    await typeDescriptionDraft('My draft');

    await typeTitleDraft('Renamed');
    await fireEvent.keyDown(screen.getByTestId('item-title-input'), { key: 'Enter' });
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith(ITEM_ID, { title: 'Renamed' }));

    await saveDescription();
    await expectDescriptionSaved('My draft');
  });

  it.each([
    ['an empty', null],
    ['an unchanged', 'Original description'],
  ])('keeps the draft through a live reload of %s description', async (_label, description) => {
    await openItem({ description });
    await typeDescriptionDraft('My draft');

    await reloadItem();
    await tick();

    expect(screen.getByTestId('markdown-editor')).toHaveValue('My draft');
    expect(screen.queryByText('items.descriptionChangedElsewhere')).not.toBeInTheDocument();
    await saveDescription();
    await expectDescriptionSaved('My draft');
  });

  it('keeps the draft and flags a description changed elsewhere', async () => {
    await openItem();
    await typeDescriptionDraft('My draft');

    changeOnServer({ description: 'Changed elsewhere' });
    await reloadItem();
    await tick();

    expect(screen.getByTestId('markdown-editor')).toHaveValue('My draft');
    expect(screen.getByText('items.descriptionChangedElsewhere')).toBeInTheDocument();
    await saveDescription();
    await expectDescriptionSaved('My draft');
  });

  it('shows the newer description when the conflicting draft is cancelled', async () => {
    await openItem();
    await typeDescriptionDraft('My draft');
    changeOnServer({ description: 'Changed elsewhere' });
    await reloadItem();

    await fireEvent.click(screen.getByRole('button', { name: 'common.cancel' }));

    expect(await screen.findByTestId('markdown-view')).toHaveTextContent('Changed elsewhere');
    expect(mocks.update).not.toHaveBeenCalled();
  });

  it('does not write an untouched editor over a newer description', async () => {
    await openItem();
    await openDescriptionEditor();
    changeOnServer({ description: 'Changed elsewhere' });
    await reloadItem();

    await saveDescription();

    expect(await screen.findByTestId('markdown-view')).toHaveTextContent('Changed elsewhere');
    expect(mocks.update).not.toHaveBeenCalled();
  });

  it('leaves focus alone when another field starts editing', async () => {
    await openItem();
    await typeDescriptionDraft('My draft');
    await typeTitleDraft('Renamed');
    const titleInput = screen.getByTestId('item-title-input');
    titleInput.focus();

    itemDetailStore.startEditing('status');
    await tick();
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(document.activeElement).toBe(titleInput);
  });
});

describe('item detail title editing', () => {
  it('keeps the title draft when another field starts editing', async () => {
    await openItem();
    await typeTitleDraft('My title');

    await openDescriptionEditor();
    itemDetailStore.startEditing('status');
    await tick();

    expect(screen.getByTestId('item-title-input')).toHaveValue('My title');
    await fireEvent.keyDown(screen.getByTestId('item-title-input'), { key: 'Enter' });
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith(ITEM_ID, { title: 'My title' }));
  });

  it('keeps the title draft through a live reload that renames the item', async () => {
    await openItem();
    await typeTitleDraft('My title');

    changeOnServer({ title: 'Renamed elsewhere' });
    await reloadItem();
    await tick();

    expect(screen.getByTestId('item-title-input')).toHaveValue('My title');
    await fireEvent.keyDown(screen.getByTestId('item-title-input'), { key: 'Enter' });
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith(ITEM_ID, { title: 'My title' }));
  });

  it('does not write an untouched title over a rename made elsewhere', async () => {
    await openItem();
    await fireEvent.click(screen.getByTestId('item-title-edit'));
    changeOnServer({ title: 'Renamed elsewhere' });
    await reloadItem();

    await fireEvent.keyDown(screen.getByTestId('item-title-input'), { key: 'Enter' });

    expect(await screen.findByTestId('item-title-edit')).toHaveTextContent('Renamed elsewhere');
    expect(mocks.update).not.toHaveBeenCalled();
  });
});

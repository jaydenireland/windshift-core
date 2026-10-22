import { fromBase64, toBase64 } from 'lib0/buffer';
import { Schema } from 'prosemirror-model';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { prosemirrorToYXmlFragment, yXmlFragmentToProseMirrorRootNode } from 'y-prosemirror';
import * as Y from 'yjs';
import { COLLAB_FRAGMENT, CollabSession, hasYjsContent } from './collabSession.js';

// A paragraphs-only schema and codec stand in for Milkdown's markdown codec.
const schema = new Schema({
  nodes: {
    doc: { content: 'paragraph+' },
    paragraph: { content: 'text*', toDOM: () => ['p', 0] },
    text: {},
  },
});

const codec = {
  schema,
  parse: (input) => {
    // Like Milkdown, a trailing newline is not content.
    const markdown = input.replace(/\n+$/, '');
    return schema.node(
      'doc',
      null,
      (markdown ? markdown.split('\n\n') : ['']).map((line) =>
        schema.node('paragraph', null, line ? [schema.text(line)] : [])
      )
    );
  },
  serialize: (node) => {
    const lines = [];
    node.forEach((child) => lines.push(child.textContent));
    return lines.join('\n\n');
  },
};

const b64 = (bytes) => toBase64(bytes);
const unb64 = (text) => fromBase64(text);

function markdownOf(doc) {
  return codec.serialize(
    yXmlFragmentToProseMirrorRootNode(doc.getXmlFragment(COLLAB_FRAGMENT), schema)
  );
}

function seedUpdate(markdown) {
  const doc = new Y.Doc();
  prosemirrorToYXmlFragment(codec.parse(markdown), doc.getXmlFragment(COLLAB_FRAGMENT));
  const update = Y.encodeStateAsUpdate(doc);
  doc.destroy();
  return update;
}

class FakeSocket {
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    this.sent = [];
    /** @type {(() => void) | null} */
    this.onopen = null;
    /** @type {((event: { data: string }) => void) | null} */
    this.onmessage = null;
    /** @type {((event: { code: number }) => void) | null} */
    this.onclose = null;
  }

  open() {
    this.readyState = 1;
    this.onopen?.();
  }

  send(data) {
    this.sent.push(JSON.parse(data));
  }

  receive(message) {
    this.onmessage?.({ data: JSON.stringify(message) });
  }

  close(code = 1000) {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.onclose?.({ code });
  }

  ofType(type) {
    return this.sent.filter((m) => m.t === type);
  }
}

function setup() {
  const sockets = [];
  const session = new CollabSession({
    kind: 'page',
    id: 1,
    url: 'ws://test/api/collab/page/1/socket',
    createSocket: (url) => {
      const socket = new FakeSocket(url);
      sockets.push(socket);
      return /** @type {any} */ (socket);
    },
  });
  session.connect();
  const socket = sockets[0];
  socket.open();
  return { session, socket, sockets };
}

/** Insert text at the start of paragraph `index` as a local editor change. */
function typeInto(session, index, text, at = 0) {
  const paragraph = /** @type {any} */ (session.doc.getXmlFragment(COLLAB_FRAGMENT).get(index));
  session.doc.transact(() => paragraph.get(0).insert(at, text));
}

describe('CollabSession', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('introduces itself with its Yjs client id', () => {
    const { session, socket } = setup();
    expect(socket.sent[0]).toEqual({ t: 'hello', client_id: session.doc.clientID });
  });

  it('seeds once as leader and checkpoints only real edits', () => {
    const { session, socket } = setup();
    session.attachCodec(codec);
    socket.receive({
      t: 'init',
      seeded: false,
      role: 'leader',
      writable: true,
      seq: 0,
      hash: 'h0',
    });
    socket.receive({ t: 'seed_request', md: 'Hello\n\nWorld', hash: 'h0', epoch: 'e1' });

    const [seed] = socket.ofType('seed');
    expect(seed.epoch).toBe('e1');
    const check = new Y.Doc();
    Y.applyUpdate(check, unb64(seed.u));
    expect(markdownOf(check)).toBe('Hello\n\nWorld');
    expect(session.state.seeded).toBe(true);
    // The seed's own changes are never sent as updates or checkpoints.
    expect(socket.ofType('update')).toHaveLength(0);
    socket.receive({ t: 'ack', seq: 1 });
    vi.advanceTimersByTime(10_000);
    expect(socket.ofType('checkpoint')).toHaveLength(0);

    typeInto(session, 0, 'Oh ');
    const [update] = socket.ofType('update');
    expect(update.epoch).toBe('e1');
    socket.receive({ t: 'ack', seq: 2 });
    vi.advanceTimersByTime(1_000);
    const [checkpoint] = socket.ofType('checkpoint');
    expect(checkpoint).toEqual({ t: 'checkpoint', md: 'Oh Hello\n\nWorld', hash: 'h0', seq: 2 });

    socket.receive({ t: 'checkpointed', hash: 'h1', seq: 2 });
    expect(session.state.storedHash).toBe('h1');
    vi.advanceTimersByTime(10_000);
    expect(socket.ofType('checkpoint')).toHaveLength(1);
  });

  it('keeps checkpointing at least every few seconds while typing continues', () => {
    const { session, socket } = setup();
    session.attachCodec(codec);
    socket.receive({
      t: 'init',
      seeded: false,
      role: 'leader',
      writable: true,
      seq: 0,
      hash: 'h0',
    });
    socket.receive({ t: 'seed_request', md: 'a', hash: 'h0', epoch: 'e1' });
    for (let i = 0; i < 12; i++) {
      typeInto(session, 0, 'x');
      vi.advanceTimersByTime(500);
    }
    expect(socket.ofType('checkpoint').length).toBeGreaterThanOrEqual(1);
  });

  it('applies remote updates without echoing them', () => {
    const { session, socket } = setup();
    const seed = seedUpdate('One');
    socket.receive({
      t: 'init',
      seeded: true,
      epoch: 'e1',
      role: 'follower',
      writable: true,
      seq: 1,
      updates: [b64(seed)],
    });
    expect(session.state.seeded).toBe(true);

    const remote = new Y.Doc();
    Y.applyUpdate(remote, seed);
    let delta = null;
    remote.on('update', (u) => {
      delta = u;
    });
    /** @type {any} */ (remote.getXmlFragment(COLLAB_FRAGMENT).get(0)).get(0).insert(3, '!');
    socket.receive({ t: 'update', u: b64(delta), seq: 2 });

    session.attachCodec(codec);
    expect(session.currentMarkdown()).toBe('One!');
    expect(socket.ofType('update')).toHaveLength(0);
    // Followers never checkpoint.
    vi.advanceTimersByTime(10_000);
    expect(socket.ofType('checkpoint')).toHaveLength(0);
  });

  it('merges an outside write into the live document without losing typing', () => {
    const { session, socket } = setup();
    session.attachCodec(codec);
    socket.receive({
      t: 'init',
      seeded: false,
      role: 'leader',
      writable: true,
      seq: 0,
      hash: 'h0',
    });
    socket.receive({ t: 'seed_request', md: 'Intro\n\nBody', hash: 'h0', epoch: 'e1' });
    typeInto(session, 1, ' typed', 4);
    expect(session.currentMarkdown()).toBe('Intro\n\nBody typed');

    socket.receive({
      t: 'reconcile',
      base: 'Intro\n\nBody',
      md: 'Intro by agent\n\nBody',
      hash: 'h2',
    });
    expect(session.currentMarkdown()).toBe('Intro by agent\n\nBody typed');
    const checkpoints = socket.ofType('checkpoint');
    expect(checkpoints.at(-1)).toMatchObject({ md: 'Intro by agent\n\nBody typed', hash: 'h2' });
    expect(session.state.mergeWarning).toBe(false);
  });

  it('waits for the editor codec before seeding', () => {
    const { session, socket } = setup();
    socket.receive({
      t: 'init',
      seeded: false,
      role: 'leader',
      writable: true,
      seq: 0,
      hash: 'h0',
    });
    socket.receive({ t: 'seed_request', md: 'Later', hash: 'h0', epoch: 'e1' });
    expect(socket.ofType('seed')).toHaveLength(0);
    session.attachCodec(codec);
    expect(socket.ofType('seed')).toHaveLength(1);
  });

  it('sends edits typed while offline after reconnecting', () => {
    const { session, socket, sockets } = setup();
    const seed = seedUpdate('Draft');
    socket.receive({
      t: 'init',
      seeded: true,
      epoch: 'e1',
      role: 'follower',
      writable: true,
      seq: 1,
      updates: [b64(seed)],
    });

    socket.close(1006);
    expect(session.state.status).toBe('reconnecting');
    typeInto(session, 0, 'Offline ');
    expect(socket.ofType('update')).toHaveLength(0);

    vi.advanceTimersByTime(60_000);
    const next = sockets[1];
    next.open();
    next.receive({
      t: 'init',
      seeded: true,
      epoch: 'e1',
      role: 'follower',
      writable: true,
      seq: 1,
      updates: [b64(seed)],
    });
    const [missing] = next.ofType('update');
    expect(missing.epoch).toBe('e1');
    const server = new Y.Doc();
    Y.applyUpdate(server, seed);
    Y.applyUpdate(server, unb64(missing.u));
    expect(markdownOf(server)).toBe('Offline Draft');
  });

  it('replaces the document when the server moves to a new epoch', () => {
    const { session, socket } = setup();
    socket.receive({
      t: 'init',
      seeded: true,
      epoch: 'e1',
      role: 'follower',
      writable: false,
      seq: 1,
      updates: [b64(seedUpdate('Old'))],
    });
    const firstDoc = session.doc;
    socket.receive({ t: 'reset', hash: 'h9' });
    expect(session.state.seeded).toBe(false);
    expect(session.state.docVersion).toBe(1);
    socket.receive({
      t: 'init',
      seeded: true,
      epoch: 'e2',
      role: 'follower',
      writable: false,
      seq: 1,
      updates: [b64(seedUpdate('New'))],
    });
    expect(session.doc).not.toBe(firstDoc);
    expect(session.doc.clientID).toBe(firstDoc.clientID);
    session.attachCodec(codec);
    expect(session.currentMarkdown()).toBe('New');
  });

  it('gives up before ever connecting so the caller can fall back', () => {
    const sockets = [];
    const session = new CollabSession({
      kind: 'page',
      id: 1,
      url: 'ws://test/x',
      createSocket: (url) => {
        const socket = new FakeSocket(url);
        sockets.push(socket);
        return /** @type {any} */ (socket);
      },
    });
    session.connect();
    for (let i = 0; i < 5; i++) {
      sockets.at(-1).close(1006);
      vi.advanceTimersByTime(60_000);
    }
    expect(session.state.status).toBe('failed');
    expect(sockets).toHaveLength(3);
  });

  it('stops for good when the document is gone', () => {
    const { session, socket, sockets } = setup();
    socket.close(4404);
    vi.advanceTimersByTime(60_000);
    expect(session.state).toMatchObject({ status: 'closed', closeReason: 'gone' });
    expect(sockets).toHaveLength(1);
  });

  it('flushes through the leader before diagram saves', async () => {
    const { session, socket } = setup();
    socket.receive({
      t: 'init',
      seeded: true,
      epoch: 'e1',
      role: 'follower',
      writable: true,
      seq: 1,
      updates: [b64(seedUpdate('x'))],
    });
    const flushed = session.requestCheckpoint();
    expect(socket.ofType('flush')).toHaveLength(1);
    socket.receive({ t: 'checkpointed', hash: 'h3', seq: 1 });
    await flushed;
    expect(session.state.storedHash).toBe('h3');
  });

  it('never stores a text nobody changed, even when flushed or closed', async () => {
    const { session, socket } = setup();
    session.attachCodec(codec);
    socket.receive({
      t: 'init',
      seeded: false,
      role: 'leader',
      writable: true,
      seq: 0,
      hash: 'h0',
    });
    // The stored markdown and the editor's serialization of it differ.
    socket.receive({ t: 'seed_request', md: 'a\n\n\n\nb\n', hash: 'h0', epoch: 'e1' });
    await session.requestCheckpoint();
    expect(socket.ofType('checkpoint')).toHaveLength(0);
    session.destroy();
    expect(socket.ofType('checkpoint')).toHaveLength(0);
  });

  it('checkpoints only its own edits after taking over a clean document', () => {
    const { session, socket } = setup();
    session.attachCodec(codec);
    socket.receive({
      t: 'init',
      seeded: true,
      epoch: 'e1',
      role: 'follower',
      writable: true,
      seq: 1,
      hash: 'h1',
      updates: [b64(seedUpdate('a\n\nb'))],
    });
    socket.receive({ t: 'role', role: 'leader', dirty: false, hash: 'h1' });
    session.requestCheckpoint();
    expect(socket.ofType('checkpoint')).toHaveLength(0);
    typeInto(session, 0, 'x');
    vi.advanceTimersByTime(1_000);
    expect(socket.ofType('checkpoint').at(-1)).toMatchObject({ md: 'xa\n\nb', hash: 'h1' });
  });

  it('stores pending edits when a leader closes the session', () => {
    const { session, socket } = setup();
    session.attachCodec(codec);
    socket.receive({
      t: 'init',
      seeded: false,
      role: 'leader',
      writable: true,
      seq: 0,
      hash: 'h0',
    });
    socket.receive({ t: 'seed_request', md: 'a', hash: 'h0', epoch: 'e1' });
    typeInto(session, 0, 'b');
    session.destroy();
    expect(socket.ofType('checkpoint').at(-1)).toMatchObject({ md: 'ba' });
  });
});

describe('hasYjsContent', () => {
  it('treats the empty update as empty', () => {
    const doc = new Y.Doc();
    expect(hasYjsContent(Y.encodeStateAsUpdate(doc))).toBe(false);
    doc.getText('t').insert(0, 'x');
    expect(hasYjsContent(Y.encodeStateAsUpdate(doc))).toBe(true);
  });
});

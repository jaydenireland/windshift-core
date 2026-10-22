import { fromBase64, toBase64 } from 'lib0/buffer';
import {
  prosemirrorToYXmlFragment,
  updateYFragment,
  yXmlFragmentToProseMirrorRootNode,
} from 'y-prosemirror';
import {
  Awareness,
  applyAwarenessUpdate,
  encodeAwarenessUpdate,
  removeAwarenessStates,
} from 'y-protocols/awareness';
import * as Y from 'yjs';
import { toExternal } from '../runtime/contextPath.js';
import { mergeMarkdown } from './mergeMarkdown.js';

/** The XmlFragment @milkdown/plugin-collab binds by default. */
export const COLLAB_FRAGMENT = 'prosemirror';

// The leader stores markdown after a short pause in typing, and at least this
// often while typing continues.
export const CHECKPOINT_IDLE_MS = 1000;
export const CHECKPOINT_MAX_WAIT_MS = 5000;
const RECONNECT_BASE_MS = 1000;
const RECONNECT_MAX_MS = 30000;
// Attempts without ever reaching the server before the session reports
// failure, so the caller can fall back to plain autosave.
const MAX_INITIAL_ATTEMPTS = 3;
const FLUSH_TIMEOUT_MS = 5000;

// Transaction origins. Remote and seed changes are never echoed to the server.
export const ORIGIN_REMOTE = 'collab-remote';
const ORIGIN_SEED = 'collab-seed';
const ORIGIN_RECONCILE = 'collab-reconcile';

// Close codes from the server (internal/collab/handler.go).
const CLOSE_GONE = 4404;
const CLOSE_ACCESS_CHANGED = 4403;

/**
 * @typedef {{ conn_id: string, user_id: number, name: string, avatar_url?: string, client_id?: number, writable: boolean }} CollabPeer
 * @typedef {{ schema: any, parse: (markdown: string) => any, serialize: (node: any) => string }} CollabCodec
 * @typedef {{
 *   status: 'connecting' | 'open' | 'reconnecting' | 'failed' | 'closed',
 *   closeReason: string,
 *   synced: boolean,
 *   seeded: boolean,
 *   role: 'leader' | 'follower' | '',
 *   writable: boolean,
 *   peers: CollabPeer[],
 *   self: CollabPeer | null,
 *   stale: boolean,
 *   storedHash: string,
 *   docVersion: number,
 *   metaVersion: number,
 *   mergeWarning: boolean,
 * }} CollabState
 */

/**
 * One live co-editing connection for a markdown document.
 *
 * The browser owns the CRDT (Yjs); the server relays and persists updates and
 * coordinates which client acts for everyone (internal/collab). When this
 * client is the leader it seeds the document from stored markdown, writes
 * markdown checkpoints, and merges outside writes into the live document.
 * Leader duties need the editor's markdown parser and serializer, which the
 * editor provides through attachCodec.
 */
export class CollabSession {
  /**
   * @param {{
   *   kind: string,
   *   id: number|string,
   *   user?: { name?: string, color?: string } | null,
   *   createSocket?: (url: string) => WebSocket,
   *   url?: string,
   * }} options
   */
  constructor({ kind, id, user = null, createSocket = null, url = null }) {
    this.kind = kind;
    this.id = String(id);
    this.url = url || collabSocketURL(kind, id);
    this.createSocket = createSocket || ((socketURL) => new WebSocket(socketURL));
    this.user = user;

    /** @type {CollabState} */
    this.state = {
      status: 'connecting',
      closeReason: '',
      synced: false,
      seeded: false,
      role: '',
      writable: false,
      peers: [],
      self: null,
      stale: false,
      storedHash: '',
      docVersion: 0,
      metaVersion: 0,
      mergeWarning: false,
    };
    this.epoch = '';
    this.seq = 0;
    /** @type {CollabCodec | null} */
    this.codec = null;
    this.pendingSeed = null;
    this.pendingReconcile = null;
    this.checkpointTimer = null;
    this.checkpointDeadline = null;
    this.confirmedMarkdown = null;
    this.leaderClean = false;
    this.awaitingMarkdown = null;
    this.flushWaiters = [];
    this.listeners = new Set();
    this.socket = null;
    this.reconnectTimer = null;
    this.attempts = 0;
    this.everOpened = false;
    this.destroyed = false;
    this.#createDoc(null);
  }

  /** Subscribe to state changes; returns an unsubscribe function. */
  subscribe(listener) {
    this.listeners.add(listener);
    listener(this.state);
    return () => this.listeners.delete(listener);
  }

  connect() {
    if (this.destroyed || this.socket) return;
    this.attempts++;
    let socket;
    try {
      socket = this.createSocket(this.url);
    } catch (error) {
      console.warn('Co-editing connection failed:', error);
      this.#scheduleReconnect();
      return;
    }
    this.socket = socket;
    socket.onopen = () => {
      if (socket !== this.socket) return;
      this.attempts = 0;
      this.everOpened = true;
      this.#send({ t: 'hello', client_id: this.doc.clientID });
      this.#sendLocalAwareness();
    };
    socket.onmessage = (event) => {
      if (socket !== this.socket) return;
      let message;
      try {
        message = JSON.parse(event.data);
      } catch {
        return;
      }
      this.#handle(message);
    };
    socket.onclose = (event) => {
      if (socket !== this.socket) return;
      this.socket = null;
      this.#dropRemoteAwareness();
      this.#rejectFlushWaiters();
      if (this.destroyed) return;
      if (event.code === CLOSE_GONE) {
        this.#setState({ status: 'closed', closeReason: 'gone' });
        return;
      }
      if (event.code === CLOSE_ACCESS_CHANGED) {
        // Reconnect right away to pick up the new access level; a revoked
        // user is refused at the handshake and runs out of attempts.
        this.attempts = 0;
        this.everOpened = false;
        this.#setState({ status: 'reconnecting' });
        this.connect();
        return;
      }
      this.#scheduleReconnect();
    };
  }

  /** Close the session. A leader stores pending edits first. */
  destroy() {
    if (this.destroyed) return;
    if (this.state.role === 'leader') this.#checkpointNow();
    this.destroyed = true;
    clearTimeout(this.reconnectTimer);
    this.#clearCheckpointTimer();
    this.#rejectFlushWaiters();
    const socket = this.socket;
    this.socket = null;
    if (socket) {
      try {
        socket.close(1000, 'closed');
      } catch {
        // Already closing.
      }
    }
    this.awareness.destroy();
    this.doc.destroy();
    this.listeners.clear();
  }

  /** Provide the editor's markdown codec (needed for leader duties). */
  attachCodec(codec) {
    this.codec = codec;
    this.#runLeaderTasks();
  }

  detachCodec(codec) {
    if (this.codec === codec) this.codec = null;
  }

  /**
   * Ask the leader to store the live document now and resolve once a
   * checkpoint lands. Used before features that edit the stored markdown
   * directly (diagram saves) so they start from the current text.
   */
  requestCheckpoint() {
    if (!this.state.seeded || this.state.status !== 'open') return Promise.resolve();
    const done = new Promise((resolve) => {
      const timer = setTimeout(() => {
        this.flushWaiters = this.flushWaiters.filter((waiter) => waiter.resolve !== resolve);
        resolve();
      }, FLUSH_TIMEOUT_MS);
      this.flushWaiters.push({ resolve, timer });
    });
    if (this.state.role === 'leader') {
      this.#checkpointNow(true);
    } else {
      this.#send({ t: 'flush' });
    }
    return done;
  }

  /** Markdown of the live document, or null without a codec. */
  currentMarkdown() {
    if (!this.codec || !this.state.seeded) return null;
    return this.codec.serialize(this.#currentNode());
  }

  // --- protocol ---------------------------------------------------------

  #handle(m) {
    switch (m.t) {
      case 'init':
        this.#onInit(m);
        break;
      case 'update':
        Y.applyUpdate(this.doc, fromBase64(m.u), ORIGIN_REMOTE);
        this.#advance(m.seq);
        break;
      case 'ack':
        this.#advance(m.seq);
        break;
      case 'seed_request':
        this.pendingSeed = m;
        this.#runLeaderTasks();
        break;
      case 'role':
        this.#setState({ role: m.role, storedHash: m.hash || this.state.storedHash });
        if (m.role === 'leader') {
          this.confirmedMarkdown = null;
          this.leaderClean = !m.dirty;
          if (m.dirty) this.#scheduleCheckpoint();
          this.#runLeaderTasks();
        }
        break;
      case 'reconcile':
        this.pendingReconcile = m;
        this.#runLeaderTasks();
        break;
      case 'checkpointed':
        this.#setState({ storedHash: m.hash, stale: false });
        if (this.awaitingMarkdown !== null) {
          this.confirmedMarkdown = this.awaitingMarkdown;
          this.awaitingMarkdown = null;
        }
        this.#resolveFlushWaiters();
        break;
      case 'stored':
        this.#setState({ storedHash: m.hash });
        if (this.state.role === 'leader' && this.awaitingMarkdown !== null) {
          // The checkpoint was refused (the base moved). A reconcile follows
          // when another writer changed the page; otherwise retry on the new base.
          this.awaitingMarkdown = null;
          this.#scheduleCheckpoint();
        }
        break;
      case 'peers':
        this.#setState({ peers: m.peers || [] });
        break;
      case 'awareness':
        applyAwarenessUpdate(this.awareness, fromBase64(m.u), ORIGIN_REMOTE);
        break;
      case 'leave':
        removeAwarenessStates(this.awareness, m.clients || [], ORIGIN_REMOTE);
        break;
      case 'state':
        if (typeof m.stale === 'boolean') this.#setState({ stale: m.stale });
        break;
      case 'reset':
        // The server discarded the document; the next editor reseeds it.
        this.#createDoc(this.doc);
        this.epoch = '';
        this.seq = 0;
        this.#setState({ seeded: false, stale: false, storedHash: m.hash || '' });
        break;
      case 'compact_request':
        if (this.state.role === 'leader') {
          this.#send({ t: 'compact', u: toBase64(Y.encodeStateAsUpdate(this.doc)), seq: this.seq });
        }
        break;
      case 'meta':
        // The title or other page metadata changed elsewhere.
        this.#setState({ metaVersion: this.state.metaVersion + 1 });
        break;
      case 'checkpoint_request':
        if (this.state.role === 'leader') this.#checkpointNow(true);
        break;
      case 'error':
        this.#onError(m.error);
        break;
      default:
        break;
    }
  }

  #onInit(m) {
    const updates = (m.updates || []).map(fromBase64);
    const seeded = !!m.seeded;
    if (seeded) {
      if (this.epoch && this.epoch !== m.epoch) this.#createDoc(this.doc);
      this.epoch = m.epoch;
      this.doc.transact(() => {
        for (const update of updates) Y.applyUpdate(this.doc, update, ORIGIN_REMOTE);
      }, ORIGIN_REMOTE);
    } else if (this.epoch) {
      // The server no longer holds the document this client had.
      this.#createDoc(this.doc);
      this.epoch = '';
    }
    this.seq = m.seq || 0;
    for (const update of m.awareness || []) {
      applyAwarenessUpdate(this.awareness, fromBase64(update), ORIGIN_REMOTE);
    }
    this.#setState({
      status: 'open',
      synced: true,
      seeded,
      role: m.role || 'follower',
      writable: !!m.writable,
      peers: m.peers || [],
      self: m.self || null,
      stale: !!m.stale,
      storedHash: m.hash || '',
    });
    if (seeded && this.state.writable) this.#sendMissingUpdates(updates);
    if (this.state.role === 'leader') {
      this.confirmedMarkdown = null;
      this.leaderClean = !m.dirty;
      if (m.dirty) this.#scheduleCheckpoint();
      this.#runLeaderTasks();
    }
  }

  /**
   * After a reconnect, send whatever this client holds that the server does
   * not: edits typed while offline, or updates the server lost in a crash.
   */
  #sendMissingUpdates(serverUpdates) {
    const server = new Y.Doc();
    try {
      for (const update of serverUpdates) Y.applyUpdate(server, update);
      const missing = Y.encodeStateAsUpdate(this.doc, Y.encodeStateVector(server));
      if (hasYjsContent(missing)) this.#sendUpdate(missing);
    } finally {
      server.destroy();
    }
  }

  #onError(code) {
    if (code === 'stale_epoch') {
      // This client's document is from a previous epoch: resynchronize.
      this.#restartSocket();
    } else if (code === 'gone') {
      this.#setState({ status: 'closed', closeReason: 'gone' });
    }
  }

  #advance(seq) {
    if (typeof seq === 'number' && seq > this.seq) this.seq = seq;
  }

  // --- leader duties ----------------------------------------------------

  #runLeaderTasks() {
    if (!this.codec || this.state.role !== 'leader' || this.state.status !== 'open') return;
    if (this.leaderClean && this.confirmedMarkdown === null && this.state.seeded) {
      // Took over a document with nothing pending: what the editor serializes
      // now is already stored, so only later edits need a checkpoint.
      this.confirmedMarkdown = this.codec.serialize(this.#currentNode());
      this.leaderClean = false;
    }
    if (this.pendingSeed) {
      const request = this.pendingSeed;
      this.pendingSeed = null;
      this.#seed(request);
    }
    if (this.pendingReconcile && this.state.seeded) {
      const request = this.pendingReconcile;
      this.pendingReconcile = null;
      this.#reconcile(request);
    }
  }

  #seed(request) {
    if (this.epoch || this.#fragment().length > 0) this.#createDoc(this.doc);
    const node = this.codec.parse(request.md || '');
    this.doc.transact(() => {
      prosemirrorToYXmlFragment(node, this.#fragment());
    }, ORIGIN_SEED);
    this.epoch = request.epoch;
    this.seq = 1;
    // The seed reproduces the stored markdown; only later edits need storing.
    this.confirmedMarkdown = this.codec.serialize(this.#currentNode());
    this.#send({ t: 'seed', u: toBase64(Y.encodeStateAsUpdate(this.doc)), epoch: request.epoch });
    this.#setState({ seeded: true, storedHash: request.hash || '' });
  }

  #reconcile(request) {
    const current = this.codec.serialize(this.#currentNode());
    const { markdown, failed } = mergeMarkdown(request.base || '', current, request.md || '');
    if (markdown !== current) {
      const node = this.codec.parse(markdown);
      this.doc.transact(() => {
        updateYFragment(this.doc, this.#fragment(), node, {
          mapping: new Map(),
          isOMark: new Map(),
        });
      }, ORIGIN_RECONCILE);
    }
    this.#setState({ storedHash: request.hash || '', mergeWarning: failed > 0 });
    // Store the merged result on the new base right away.
    this.confirmedMarkdown = null;
    this.#checkpointNow(true);
  }

  #scheduleCheckpoint() {
    if (this.state.role !== 'leader' || !this.state.seeded) return;
    clearTimeout(this.checkpointTimer);
    const now = Date.now();
    if (this.checkpointDeadline === null) this.checkpointDeadline = now + CHECKPOINT_MAX_WAIT_MS;
    const delay = Math.max(0, Math.min(CHECKPOINT_IDLE_MS, this.checkpointDeadline - now));
    this.checkpointTimer = setTimeout(() => this.#checkpointNow(), delay);
  }

  #clearCheckpointTimer() {
    clearTimeout(this.checkpointTimer);
    this.checkpointTimer = null;
    this.checkpointDeadline = null;
  }

  #checkpointNow(force = false) {
    this.#clearCheckpointTimer();
    if (this.state.role !== 'leader' || !this.state.seeded || this.state.status !== 'open') return;
    if (!this.codec) {
      // Retry once the editor provides its serializer again.
      this.checkpointTimer = setTimeout(() => this.#checkpointNow(force), CHECKPOINT_IDLE_MS);
      return;
    }
    if (this.pendingReconcile) return;
    const markdown = this.codec.serialize(this.#currentNode());
    // An identical checkpoint is already in flight; its confirmation resolves
    // any waiters.
    if (markdown === this.awaitingMarkdown) return;
    if (markdown === this.confirmedMarkdown) {
      // Nothing to store. The stored text can differ in formatting from what
      // the editor serializes (list markers, blank lines); sending it anyway
      // would rewrite the document and record a change by someone who typed
      // nothing. A forced request (flush, close) only needs its answer.
      if (force) this.#resolveFlushWaiters();
      return;
    }
    this.awaitingMarkdown = markdown;
    this.#send({ t: 'checkpoint', md: markdown, hash: this.state.storedHash, seq: this.seq });
  }

  // --- document plumbing ------------------------------------------------

  /**
   * Create a fresh document (and awareness) for a new epoch, keeping this
   * client's Yjs id so the server's view of our awareness stays valid.
   */
  #createDoc(previous) {
    const doc = new Y.Doc();
    if (previous) doc.clientID = previous.clientID;
    const awareness = new Awareness(doc);
    const localState = this.awareness?.getLocalState() ?? (this.user ? { user: this.user } : null);
    if (this.awareness) this.awareness.destroy();
    if (previous) previous.destroy();
    awareness.setLocalState(localState);
    doc.on('update', (update, origin) => this.#onDocUpdate(update, origin));
    awareness.on('update', ({ added, updated, removed }, origin) => {
      if (origin !== 'local') return;
      const changed = added.concat(updated, removed);
      if (changed.length > 0)
        this.#send({ t: 'awareness', u: toBase64(encodeAwarenessUpdate(awareness, changed)) });
    });
    this.doc = doc;
    this.awareness = awareness;
    if (previous) this.#setState({ docVersion: this.state.docVersion + 1 });
  }

  #onDocUpdate(update, origin) {
    if (origin === ORIGIN_SEED) return;
    if (origin !== ORIGIN_REMOTE && this.state.writable && this.state.seeded) {
      this.#sendUpdate(update);
    }
    if (this.state.role === 'leader') this.#scheduleCheckpoint();
  }

  #sendUpdate(update) {
    this.#send({ t: 'update', u: toBase64(update), epoch: this.epoch });
  }

  #fragment() {
    return this.doc.getXmlFragment(COLLAB_FRAGMENT);
  }

  #currentNode() {
    return yXmlFragmentToProseMirrorRootNode(this.#fragment(), this.codec.schema);
  }

  #sendLocalAwareness() {
    const local = this.awareness.getLocalState();
    if (local) {
      this.#send({
        t: 'awareness',
        u: toBase64(encodeAwarenessUpdate(this.awareness, [this.doc.clientID])),
      });
    }
  }

  #dropRemoteAwareness() {
    const remote = [...this.awareness.getStates().keys()].filter((id) => id !== this.doc.clientID);
    if (remote.length > 0) removeAwarenessStates(this.awareness, remote, ORIGIN_REMOTE);
  }

  #send(message) {
    const socket = this.socket;
    if (socket?.readyState !== 1) return false;
    socket.send(JSON.stringify(message));
    return true;
  }

  #restartSocket() {
    const socket = this.socket;
    this.socket = null;
    if (socket) {
      try {
        socket.close(1000, 'resync');
      } catch {
        // Already closing.
      }
    }
    this.#setState({ status: 'reconnecting' });
    this.connect();
  }

  #scheduleReconnect() {
    if (this.destroyed) return;
    if (!this.everOpened && this.attempts >= MAX_INITIAL_ATTEMPTS) {
      this.#setState({ status: 'failed', closeReason: 'unreachable' });
      return;
    }
    const exponent = Math.min(this.attempts, 5);
    const delay =
      Math.min(RECONNECT_MAX_MS, RECONNECT_BASE_MS * 2 ** exponent) * (0.5 + Math.random() / 2);
    this.#setState({ status: this.everOpened ? 'reconnecting' : 'connecting' });
    clearTimeout(this.reconnectTimer);
    this.reconnectTimer = setTimeout(() => this.connect(), delay);
  }

  #resolveFlushWaiters() {
    const waiters = this.flushWaiters;
    this.flushWaiters = [];
    for (const { resolve, timer } of waiters) {
      clearTimeout(timer);
      resolve();
    }
  }

  #rejectFlushWaiters() {
    // Waiters only gate optional work; resolve rather than fail it.
    this.#resolveFlushWaiters();
  }

  #setState(patch) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener(this.state);
  }
}

/** An encoded Yjs update with no structs and no deletions is two zero bytes. */
export function hasYjsContent(update) {
  return update.length > 2 || update.some((byte) => byte !== 0);
}

/** WebSocket URL for a document, honoring the deployment's context path. */
export function collabSocketURL(kind, id) {
  const path = toExternal(
    `/api/collab/${encodeURIComponent(kind)}/${encodeURIComponent(String(id))}/events`
  );
  const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${scheme}//${window.location.host}${path}`;
}

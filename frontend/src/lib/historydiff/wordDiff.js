// Word-level diffs for item history. Fork-owned (see FORK.md): upstream's
// history view shows a truncated "old → new" pair for text fields; this module
// turns the stored old/new text into insert/delete/equal runs so the view can
// show exactly which words changed.
import DiffMatchPatch from 'diff-match-patch';

// Fields always rendered as a diff. Other free-text values get a diff only
// once they are long enough that "old → new" would be truncated anyway.
const DIFF_FIELDS = new Set(['description']);
const DIFF_MIN_LENGTH = 80;

// Collapsing: an unchanged run longer than this many tokens is folded down to
// CONTEXT tokens on each side of the neighbouring change.
const CONTEXT = 12;
const COLLAPSE_OVER = 40;

// Distinct tokens map onto single UTF-16 units below the surrogate range.
const MAX_DISTINCT_TOKENS = 0xd800 - 1;

/**
 * Whether a history row should render as a word diff instead of "old → new".
 * Server-resolved values (ids resolved to names) and encoded values
 * (attachments, diagrams, JSON custom fields) keep upstream's rendering.
 * @param {{field_name?: string, old_value?: unknown, new_value?: unknown,
 *   resolved_old_value?: unknown, resolved_new_value?: unknown}} change
 */
export function historyDiffApplies(change) {
  if (!change) return false;
  if (change.resolved_old_value || change.resolved_new_value) return false;
  const oldText = asText(change.old_value);
  const newText = asText(change.new_value);
  if (oldText === null || newText === null) return false;
  if (looksEncoded(oldText) || looksEncoded(newText)) return false;
  if (DIFF_FIELDS.has(change.field_name ?? '')) return true;
  return Math.max(oldText.length, newText.length) >= DIFF_MIN_LENGTH;
}

function asText(value) {
  if (value === null || value === undefined) return '';
  return typeof value === 'string' ? value : null;
}

function looksEncoded(text) {
  const head = text.trimStart();
  return (
    head.startsWith('{') ||
    head.startsWith('[') ||
    head.startsWith('attachment:') ||
    head.startsWith('diagram:')
  );
}

/**
 * Diff two texts by word. Whitespace runs are tokens of their own so line
 * breaks survive, and the result is rendered with `white-space: pre-wrap`.
 * @param {string} oldText
 * @param {string} newText
 * @returns {Array<{op: 'equal' | 'insert' | 'delete', text: string}>}
 */
export function diffWords(oldText, newText) {
  const oldTokens = tokenize(oldText ?? '');
  const newTokens = tokenize(newText ?? '');
  const codes = new Map();
  const dmp = new DiffMatchPatch();
  if (new Set([...oldTokens, ...newTokens]).size > MAX_DISTINCT_TOKENS) {
    // The token alphabet would overflow diff-match-patch's 16-bit units; fall
    // back to a character diff, which is still exact, just less word-shaped.
    const diffs = dmp.diff_main(oldText ?? '', newText ?? '');
    dmp.diff_cleanupSemantic(diffs);
    return toSegments(diffs, (text) => text);
  }
  const encode = (tokens) =>
    tokens
      .map((tok) => {
        let code = codes.get(tok);
        if (code === undefined) {
          // Code points 1.. keep each token one UTF-16 unit for diff-match-patch
          // (which indexes by unit), skipping surrogates so no pair is formed.
          code = codes.size + 1;
          if (code >= 0xd800) code += 0x800;
          codes.set(tok, code);
        }
        return String.fromCharCode(code);
      })
      .join('');
  const chars1 = encode(oldTokens);
  const chars2 = encode(newTokens);
  const decode = new Map([...codes].map(([tok, code]) => [String.fromCharCode(code), tok]));

  const diffs = dmp.diff_main(chars1, chars2, false);
  dmp.diff_cleanupSemantic(diffs);
  return toSegments(diffs, (encoded) => [...encoded].map((c) => decode.get(c) ?? '').join(''));
}

/**
 * @param {import("diff-match-patch").Diff[]} diffs
 * @param {(encoded: string) => string} decodeText
 * @returns {Array<{op: 'equal' | 'insert' | 'delete', text: string}>}
 */
function toSegments(diffs, decodeText) {
  /** @type {Array<{op: 'equal' | 'insert' | 'delete', text: string}>} */
  const out = [];
  for (const diff of diffs) {
    const op = diff[0];
    const text = decodeText(diff[1]);
    if (text === '') continue;
    /** @type {'equal' | 'insert' | 'delete'} */
    const kind = op === 0 ? 'equal' : op === 1 ? 'insert' : 'delete';
    const last = out[out.length - 1];
    if (last && last.op === kind) last.text += text;
    else out.push({ op: kind, text });
  }
  return out;
}

function tokenize(text) {
  return text.match(/\s+|\S+/g) ?? [];
}

/**
 * Fold long unchanged runs. A folded run becomes `{op: 'skip', words}` so the
 * view can show "N unchanged words" in its place; runs at the very start or end
 * keep only the context nearest the change.
 * @param {ReturnType<typeof diffWords>} segments
 * @returns {Array<{op: 'equal' | 'insert' | 'delete' | 'skip', text: string, words?: number}>}
 */
export function collapseDiff(segments) {
  const out = [];
  segments.forEach((seg, i) => {
    if (seg.op !== 'equal') {
      out.push(seg);
      return;
    }
    const tokens = tokenize(seg.text);
    if (tokens.length <= COLLAPSE_OVER) {
      out.push(seg);
      return;
    }
    const first = i === 0;
    const last = i === segments.length - 1;
    const headLen = first ? 0 : CONTEXT;
    const tailLen = last ? 0 : CONTEXT;
    const hidden = tokens.slice(headLen, tokens.length - tailLen);
    if (headLen) out.push({ op: 'equal', text: tokens.slice(0, headLen).join('') });
    out.push({ op: 'skip', text: hidden.join(''), words: countWords(hidden) });
    if (tailLen) out.push({ op: 'equal', text: tokens.slice(tokens.length - tailLen).join('') });
  });
  return out;
}

function countWords(tokens) {
  return tokens.filter((tok) => /\S/.test(tok)).length;
}

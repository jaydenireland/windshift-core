import DiffMatchPatch from 'diff-match-patch';

/**
 * Three-way merge of markdown for co-editing. `base` is the markdown the live
 * document last stored, `next` is what an outside writer (API, agent, diagram
 * save, revision restore) stored on top of it, and `current` is the live
 * document now.
 *
 * The merge is line-based (diff3): Milkdown writes each block as its own
 * line(s), so a line is the natural unit of an edit. Changes to different
 * lines both apply; insertions at the same place keep both, live text first.
 * When both sides changed the same lines the live text wins and the conflict
 * is counted in `failed` — the outside write is not lost, it is the newest
 * entry in the page history.
 *
 * @param {string} base
 * @param {string} current
 * @param {string} next
 * @returns {{ markdown: string, failed: number }}
 */
export function mergeMarkdown(base, current, next) {
  if (base === next) return { markdown: current, failed: 0 };
  if (base === current) return { markdown: next, failed: 0 };
  // Writers disagree on a final newline; without one the last line would
  // never compare equal across sides and every edit near the end would
  // conflict.
  base = withFinalNewline(base);
  current = withFinalNewline(current);
  next = withFinalNewline(next);

  const baseLines = splitLines(base);
  const live = lineHunks(base, current);
  const outside = lineHunks(base, next);

  const out = [];
  let failed = 0;
  let pos = 0;
  let i = 0;
  let j = 0;
  const emitBase = (until) => {
    while (pos < until) out.push(baseLines[pos++]);
  };

  while (i < live.length || j < outside.length) {
    const a = live[i];
    const b = outside[j];
    if (b && (!a || precedes(b, a))) {
      emitBase(b.start);
      out.push(...b.lines);
      pos = b.end;
      j++;
      continue;
    }
    if (a && (!b || precedes(a, b))) {
      emitBase(a.start);
      out.push(...a.lines);
      pos = a.end;
      i++;
      continue;
    }
    // Overlapping changes. Gather every hunk on either side that overlaps
    // the combined region, so chains of overlaps resolve as one conflict.
    let start = Math.min(a.start, b.start);
    let end = Math.max(a.end, b.end);
    const liveGroup = [a];
    const outsideGroup = [b];
    i++;
    j++;
    for (;;) {
      if (i < live.length && overlapsRegion(live[i], start, end)) {
        end = Math.max(end, live[i].end);
        liveGroup.push(live[i++]);
      } else if (j < outside.length && overlapsRegion(outside[j], start, end)) {
        end = Math.max(end, outside[j].end);
        outsideGroup.push(outside[j++]);
      } else {
        break;
      }
    }
    start = Math.min(start, liveGroup[0].start, outsideGroup[0].start);
    emitBase(start);
    if (isPureInsertAt(liveGroup, start) && isPureInsertAt(outsideGroup, start)) {
      // Both sides only added text at the same spot: keep both.
      for (const hunk of liveGroup) out.push(...hunk.lines);
      for (const hunk of outsideGroup) out.push(...hunk.lines);
    } else {
      // Keep the live side for the whole region.
      out.push(...applyHunks(baseLines, start, end, liveGroup));
      failed += 1;
    }
    pos = end;
  }
  emitBase(baseLines.length);
  return { markdown: out.join(''), failed };
}

/**
 * Changed regions of `from` on the way to `to`, in order. Each hunk replaces
 * base lines [start, end) with `lines`; a pure insertion has start === end.
 */
function lineHunks(from, to) {
  // The line-mode helpers are typed private but are diff-match-patch's
  // documented way to diff by line.
  const dmp = /** @type {any} */ (new DiffMatchPatch());
  const { chars1, chars2, lineArray } = dmp.diff_linesToChars_(from, to);
  const diffs = dmp.diff_main(chars1, chars2, false);
  dmp.diff_charsToLines_(diffs, lineArray);

  const hunks = [];
  let line = 0;
  let open = null;
  for (const [op, text] of diffs) {
    const lines = splitLines(text);
    if (op === 0) {
      if (open) {
        hunks.push(open);
        open = null;
      }
      line += lines.length;
      continue;
    }
    if (!open) open = { start: line, end: line, lines: [] };
    if (op === -1) {
      line += lines.length;
      open.end = line;
    } else {
      open.lines.push(...lines);
    }
  }
  if (open) hunks.push(open);
  return hunks;
}

function withFinalNewline(text) {
  return text === '' || text.endsWith('\n') ? text : `${text}\n`;
}

function splitLines(text) {
  if (!text) return [];
  return text.match(/[^\n]*\n|[^\n]+$/g) || [];
}

/**
 * Whether hunk x applies entirely before hunk y. Hunks that merely touch (an
 * insertion right before or after a replaced range, or adjacent replacements)
 * do not conflict; two insertions at the same point are handled together.
 */
function precedes(x, y) {
  if (x.end !== y.start) return x.end < y.start;
  return !(x.start === x.end && y.start === y.end);
}

/** Whether a hunk falls inside the conflict region [start, end). */
function overlapsRegion(hunk, start, end) {
  if (hunk.start === hunk.end) {
    return (start < hunk.start && hunk.start < end) || (start === end && hunk.start === start);
  }
  return hunk.start < end && start < hunk.end;
}

function isPureInsertAt(group, at) {
  return group.every((hunk) => hunk.start === at && hunk.end === at);
}

function applyHunks(baseLines, start, end, hunks) {
  const out = [];
  let pos = start;
  for (const hunk of hunks) {
    while (pos < hunk.start) out.push(baseLines[pos++]);
    out.push(...hunk.lines);
    pos = hunk.end;
  }
  while (pos < end) out.push(baseLines[pos++]);
  return out;
}

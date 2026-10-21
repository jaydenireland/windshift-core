import { describe, expect, it } from 'vitest';
import { collapseDiff, diffWords, historyDiffApplies } from './wordDiff.js';

describe('diffWords', () => {
  it('marks only the changed word', () => {
    expect(diffWords('The quick brown fox', 'The quick red fox')).toEqual([
      { op: 'equal', text: 'The quick ' },
      { op: 'delete', text: 'brown' },
      { op: 'insert', text: 'red' },
      { op: 'equal', text: ' fox' },
    ]);
  });

  it('keeps line breaks inside tokens', () => {
    const segs = diffWords('one\n\ntwo', 'one\n\nthree');
    expect(segs.map((s) => s.text).join('')).toContain('one\n\n');
    expect(segs.find((s) => s.op === 'insert')).toEqual({ op: 'insert', text: 'three' });
  });

  it('handles an empty old value as a pure insertion', () => {
    expect(diffWords('', 'brand new')).toEqual([{ op: 'insert', text: 'brand new' }]);
  });

  it('handles identical text', () => {
    expect(diffWords('same', 'same')).toEqual([{ op: 'equal', text: 'same' }]);
  });

  it('survives many distinct tokens', () => {
    const words = Array.from({ length: 70000 }, (_, i) => `w${i}`).join(' ');
    const segs = diffWords(words, `${words} tail`);
    expect(segs[segs.length - 1]).toEqual({ op: 'insert', text: ' tail' });
    expect(segs.filter((s) => s.op !== 'equal')).toHaveLength(1);
  });
});

describe('collapseDiff', () => {
  const filler = (n, prefix = 'x') =>
    Array.from({ length: n }, (_, i) => `${prefix}${i}`).join(' ');

  it('leaves short unchanged runs alone', () => {
    const segs = diffWords('a b c', 'a b d');
    expect(collapseDiff(segs)).toEqual(segs);
  });

  it('folds a long unchanged middle but keeps context on both sides', () => {
    const before = `start ${filler(100)} end`;
    const after = `begin ${filler(100)} stop`;
    const out = collapseDiff(diffWords(before, after));
    const skip = out.find((s) => s.op === 'skip');
    expect(skip).toBeDefined();
    expect(skip?.words).toBeGreaterThan(50);
    const idx = out.indexOf(skip);
    expect(out[idx - 1].op).toBe('equal');
    expect(out[idx + 1].op).toBe('equal');
    expect(out[idx + 1].op).toBe('equal');
    expect(out[idx + 2].op).toBe('delete');
  });

  it('keeps no trailing context when the unchanged run ends the text', () => {
    const out = collapseDiff(diffWords(`start ${filler(100)}`, `begin ${filler(100)}`));
    expect(out[out.length - 1].op).toBe('skip');
  });

  it('keeps no leading context at the very start of the text', () => {
    const before = `${filler(100)} old`;
    const after = `${filler(100)} new`;
    const out = collapseDiff(diffWords(before, after));
    expect(out[0].op).toBe('skip');
  });
});

describe('historyDiffApplies', () => {
  it('always applies to description', () => {
    expect(historyDiffApplies({ field_name: 'description', old_value: 'a', new_value: 'b' })).toBe(
      true
    );
    expect(historyDiffApplies({ field_name: 'description', old_value: null, new_value: 'b' })).toBe(
      true
    );
  });

  it('applies to other long free text only', () => {
    const long = 'word '.repeat(30);
    expect(historyDiffApplies({ field_name: 'title', old_value: 'a', new_value: 'b' })).toBe(false);
    expect(
      historyDiffApplies({ field_name: 'title', old_value: long, new_value: `${long}!` })
    ).toBe(true);
  });

  it('skips resolved and encoded values', () => {
    expect(
      historyDiffApplies({
        field_name: 'description',
        old_value: '1',
        new_value: '2',
        resolved_new_value: 'Done',
      })
    ).toBe(false);
    expect(
      historyDiffApplies({ field_name: 'description', old_value: '{"a":1}', new_value: '{"a":2}' })
    ).toBe(false);
    expect(
      historyDiffApplies({
        field_name: 'attachment_uploaded',
        old_value: '',
        new_value: `attachment:1:${'x'.repeat(100)}`,
      })
    ).toBe(false);
  });
});

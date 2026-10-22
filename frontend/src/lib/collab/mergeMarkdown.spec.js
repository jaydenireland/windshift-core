import { describe, expect, it } from 'vitest';
import { mergeMarkdown } from './mergeMarkdown.js';

describe('mergeMarkdown', () => {
  it('takes the outside write when the live document has not moved', () => {
    expect(mergeMarkdown('a', 'a', 'b')).toEqual({ markdown: 'b', failed: 0 });
  });

  it('keeps the live document when the outside write changed nothing', () => {
    expect(mergeMarkdown('a', 'live', 'a')).toEqual({ markdown: 'live', failed: 0 });
  });

  it('replays an outside change onto concurrent live edits', () => {
    const base = '# Plan\n\nWe ship on Friday.\n\n## Risks\n\nNone yet.\n';
    const next = '# Plan\n\nWe ship on Friday.\n\n## Risks\n\nThe API is late.\n';
    const current = '# Plan\n\nWe ship on Friday, pending review.\n\n## Risks\n\nNone yet.\n';
    expect(mergeMarkdown(base, current, next)).toEqual({
      markdown: '# Plan\n\nWe ship on Friday, pending review.\n\n## Risks\n\nThe API is late.\n',
      failed: 0,
    });
  });

  it('appends an outside addition after live edits elsewhere', () => {
    const base = 'Intro\n';
    const next = 'Intro\n\n```excalidraw\n{"id":1}\n```\n';
    const current = 'Intro, edited live\n';
    const { markdown, failed } = mergeMarkdown(base, current, next);
    expect(failed).toBe(0);
    expect(markdown).toContain('Intro, edited live');
    expect(markdown).toContain('```excalidraw');
  });

  it('reports a change whose context was rewritten', () => {
    const base = 'The quick brown fox jumps over the lazy dog.';
    const next = 'The quick brown fox jumps over the sleepy dog.';
    const current =
      'Completely different text that shares nothing with the original sentence at all.';
    expect(mergeMarkdown(base, current, next).failed).toBeGreaterThan(0);
  });

  it('keeps both sides when each appended at the end', () => {
    expect(mergeMarkdown('a\n', 'a\nlive\n', 'a\nagent\n')).toEqual({
      markdown: 'a\nlive\nagent\n',
      failed: 0,
    });
  });

  it('keeps the live line when both sides rewrote it', () => {
    expect(mergeMarkdown('x\ny\n', 'x live\ny\n', 'x agent\ny\n')).toEqual({
      markdown: 'x live\ny\n',
      failed: 1,
    });
  });

  it('applies an outside deletion of a line the live side left alone', () => {
    expect(mergeMarkdown('a\nb\nc\n', 'a live\nb\nc\n', 'a\nc\n')).toEqual({
      markdown: 'a live\nc\n',
      failed: 0,
    });
  });

  it('treats a missing final newline as equal', () => {
    const { markdown, failed } = mergeMarkdown('Intro', 'Intro, edited\n', 'Intro\n\nBlock');
    expect(failed).toBe(0);
    expect(markdown).toBe('Intro, edited\n\nBlock\n');
  });
});

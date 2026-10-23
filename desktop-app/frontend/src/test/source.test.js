// The source helper the wiring tests read the app through. Pinned on names
// that are not going anywhere: utils.js exports and the preset grid, which
// stays non-importable on purpose (the view-extraction trap).
import { describe, it, expect } from 'vitest';
import { allSource, declaredIn, fileSource, sourceFiles, srcBetween, srcContaining, srcOf } from './source.js';

describe('allSource', () => {
  it('joins every non-test source file under src/, each under a line naming it', () => {
    const all = allSource();
    expect(sourceFiles()).toContain('main.js');
    expect(sourceFiles()).toContain('views/settings.js');
    expect(all).toContain('\n// ===== main.js =====\n');
    expect(all).toContain('\n// ===== views/settings.js =====\n');
  });

  it('leaves the tests and this helper out', () => {
    expect(sourceFiles().some((f) => f.endsWith('.test.js'))).toBe(false);
    expect(sourceFiles().some((f) => f.startsWith('test/'))).toBe(false);
  });

  it('normalises line endings, so a slice keyed on a newline cuts the same text everywhere', () => {
    expect(allSource()).not.toContain('\r');
  });
});

describe('srcOf', () => {
  it('cuts a declaration out by name, ending at its own closing brace', () => {
    const fn = srcOf('escapeHtml');
    expect(fn).toMatch(/^function escapeHtml\(/);
    expect(fn.trimEnd().endsWith('}')).toBe(true);
    // The cut is a complete function: it can be defined on its own.
    expect(() => new Function(fn)).not.toThrow();
    expect(declaredIn('escapeHtml')).toBe('utils.js');
  });

  it('cuts the whole of a long function, not up to the first unindented brace', () => {
    const grid = srcOf('renderPresets');
    expect(grid).toMatch(/^function renderPresets\(/);
    expect(() => new Function(grid)).not.toThrow();
  });

  it('finds a const table as well as a function', () => {
    expect(srcOf('LOCALE_FLAG_CC')).toMatch(/^const LOCALE_FLAG_CC = \{/);
  });

  it('names the missing declaration instead of matching nothing', () => {
    expect(() => srcOf('noSuchFunctionAnywhere')).toThrow(/noSuchFunctionAnywhere/);
    expect(() => srcOf('escapeHtml', { file: 'main.js' })).toThrow(/not declared in main.js/);
    expect(() => srcOf('escapeHtml', { has: 'text that is not in there' })).toThrow(/no escapeHtml/);
  });
});

describe('the marker-based cuts', () => {
  it('srcContaining returns the whole top-level statement around a marker', () => {
    // The statement is the export; srcOf cuts the declaration inside it.
    const stmt = srcContaining('function escapeHtml(');
    expect(stmt).toBe('export ' + srcOf('escapeHtml'));
  });

  it('srcBetween slices from one marker to the next inside the file that holds the first', () => {
    const text = srcBetween('function escapeHtml(', '}');
    expect(text).toMatch(/^function escapeHtml\(/);
    expect(text).not.toContain('}');
    expect(() => srcBetween('function escapeHtml(', 'nothing like this follows it')).toThrow(/does not follow/);
  });

  it('refuses a marker that is nowhere or in more than one file', () => {
    expect(() => srcContaining('no such marker in any source file')).toThrow(/no source file contains/);
    expect(() => srcContaining('import ')).toThrow(/more than one file/);
  });
});

describe('fileSource', () => {
  it('reads one file by its path relative to src/', () => {
    expect(fileSource('utils.js')).toContain('function escapeHtml(');
    expect(() => fileSource('nope.js')).toThrow(/no such source file/);
  });
});

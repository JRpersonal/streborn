// The round Play/Pause button of the UI 2.0 player: which glyph it draws and
// the words it keeps as its name, read from what nowplaying.js writes.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileSource, srcOf } from '../test/source.js';
import { transportGlyph, transportLabel } from './transport.js';

// Line endings follow the checkout; the rules below are matched on LF.
const css = readFileSync(new URL('./shell2.css', import.meta.url), 'utf8').replace(/\r\n/g, '\n');

// The symbols nowplaying.js and the markup write as &#9205; &#9208; &#9209;.
const PLAY = String.fromCodePoint(9205);
const PAUSE = String.fromCodePoint(9208);
const STOP = String.fromCodePoint(9209);
const EMOJI_VARIANT = String.fromCodePoint(0xFE0F);

describe('UI 2.0 transport buttons', () => {
  it('tells Play from Pause by the symbol, in any language', () => {
    expect(transportGlyph(`${PAUSE} Pause`)).toBe('pause');
    expect(transportGlyph(`${PLAY} Play`)).toBe('play');
    expect(transportGlyph(`${PLAY} Wiedergabe`)).toBe('play');
    expect(transportGlyph(`${PAUSE} Duraklat`)).toBe('pause');
    // The markup's first state, and anything unknown, draws Pause.
    expect(transportGlyph('')).toBe('pause');
    expect(transportGlyph(undefined)).toBe('pause');
  });

  it('keeps the words without the symbol', () => {
    expect(transportLabel(`${PAUSE} Pause`)).toBe('Pause');
    expect(transportLabel(`${PLAY} Play`)).toBe('Play');
    expect(transportLabel(`${STOP} Stop`)).toBe('Stop');
    expect(transportLabel(`${PLAY}${EMOJI_VARIANT} Wiedergabe`)).toBe('Wiedergabe');
    expect(transportLabel('Play it again')).toBe('Play it again');
    expect(transportLabel('')).toBe('');
  });

  it('reads the symbols nowplaying.js and the markup write', () => {
    const bar = srcOf('renderNowPlayingBar');
    expect(bar).toContain("'&#9205; ' + escapeHtml(t('controls.play'))");
    expect(bar).toContain("'&#9208; ' + escapeHtml(t('controls.pause'))");
    expect(fileSource('views/music/markup.js')).toContain('id="stopBtn">&#9209; ');
  });

  it('names the buttons where the preview mounts, and only there', () => {
    expect(srcOf('mountShell2')).toContain('nameTransport();');
    const name = srcOf('nameTransport');
    expect(name).toContain('pp.dataset.ui2Glyph = transportGlyph(pp.textContent)');
    expect(name).toContain('new MutationObserver(sync)');
    // The words stay in the button (its accessible name); only a tooltip and
    // the glyph mark are added.
    expect(name).not.toMatch(/textContent\s*=|innerHTML\s*=|aria-label/);
  });

  it('draws one round primary button and keeps the others quiet', () => {
    const rule = (sel) => {
      const i = css.indexOf(sel + ' {');
      expect(i, sel).toBeGreaterThan(0);
      return css.slice(i, css.indexOf('}', i));
    };
    const primary = rule('html.ui2 .ui2-player .controls #pauseBtn');
    expect(primary).toContain('background: var(--u-text)');
    expect(primary).toContain('color: var(--u-bg)');
    const quiet = rule('html.ui2 .ui2-player .controls > .btn,\nhtml.ui2 .ui2-player .volume-control .vol-step');
    expect(quiet).toContain('background: transparent');
    expect(quiet).toContain('border-radius: 50%');
    // The words are still there for a screen reader: hidden by size, not by
    // display or visibility.
    expect(rule('html.ui2 .ui2-player .controls :is(#pauseBtn, #stopBtn)')).toContain('font-size: 0');
    expect(css).toMatch(/#pauseBtn\[data-ui2-glyph="play"\]::before/);
    // Power stays in the transport (#1796) and is lit while the speaker is on.
    expect(rule('html.ui2 .ui2-player .controls #powerBtn.on')).toContain('var(--u-accent)');
  });
});

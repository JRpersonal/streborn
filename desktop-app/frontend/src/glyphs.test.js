// #1948: on Debian the Music view's On/Off button was a tofu box. Its symbol,
// U+23FB, is in Segoe UI Symbol and the macOS fonts but in neither of the two
// fonts a Debian desktop draws symbols from (DejaVu Sans, Noto Color Emoji);
// the same holds for the medium play triangle U+23F5 and the circled i U+24D8.
// They are inline SVG now (glyphs.js), and none of the three may come back as
// a character, in the desktop app or on the phone remote.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { fileSource, srcOf } from './test/source.js';
import { INFO_ICON, PLAY_ICON, POWER_ICON, POWER_ICON_PATHS } from './glyphs.js';

const SRC = dirname(fileURLToPath(import.meta.url));
const PHONE = join(SRC, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html');

// The code points without a glyph on a stock Debian desktop.
const NO_FONT = [0x23F5, 0x23FB, 0x24D8];

function sourceFiles(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name !== 'node_modules') sourceFiles(p, out);
    } else if (/\.(js|css|html|json)$/.test(name) && !/\.test\.js$/.test(name)) {
      out.push(p);
    }
  }
  return out;
}

function fontlessUses(text) {
  const hits = [];
  for (const cp of NO_FONT) {
    const forms = [
      String.fromCodePoint(cp),
      `&#${cp};`,
      `&#x${cp.toString(16)};`,
      `&#x${cp.toString(16).toUpperCase()};`,
      `\\u${cp.toString(16)}`,
      `\\u${cp.toString(16).toUpperCase()}`,
    ];
    for (const f of forms) if (text.includes(f)) hits.push(cp.toString(16) + ' as ' + JSON.stringify(f));
  }
  return hits;
}

describe('symbols a Debian desktop has no font for (#1948)', () => {
  it('are written nowhere in the desktop frontend', () => {
    const bad = [];
    for (const p of sourceFiles(SRC)) {
      const hits = fontlessUses(readFileSync(p, 'utf8'));
      if (hits.length) bad.push(relative(SRC, p) + ': ' + hits.join(', '));
    }
    expect(bad).toEqual([]);
  });

  it('are written nowhere on the phone remote', () => {
    expect(fontlessUses(readFileSync(PHONE, 'utf8'))).toEqual([]);
  });

  it('are SVG icons in the text colour, hidden from a screen reader', () => {
    for (const icon of [PLAY_ICON, POWER_ICON, INFO_ICON]) {
      expect(icon.startsWith('<svg class="glyph"')).toBe(true);
      expect(icon).toContain('currentColor');
      expect(icon).toContain('aria-hidden="true"');
      expect(fontlessUses(icon)).toEqual([]);
    }
    expect(PLAY_ICON).toContain('data-glyph="play"');
    expect(POWER_ICON).toContain(POWER_ICON_PATHS);
  });

  it('keep the power button named by its label, not by its icon', () => {
    const markup = fileSource('views/music/markup.js');
    const btn = markup.slice(markup.indexOf('id="powerBtn"'), markup.indexOf('</button>', markup.indexOf('id="powerBtn"')));
    // The template placeholders are matched as text; "$" + "{" keeps eslint
    // from reading them as a template literal written with plain quotes.
    const tpl = (expr) => '$' + '{' + expr + '}';
    expect(btn).toContain(`aria-label="${tpl("escapeAttr(t('controls.powerTitle'))")}"`);
    expect(btn).toContain(`title="${tpl("escapeAttr(t('controls.powerTitle'))")}"`);
    expect(btn).toContain(tpl('POWER_ICON'));
  });

  it('draws the remote\'s power key as a path, not as a label', () => {
    const remote = srcOf('remoteSvg');
    expect(remote).toContain("key('power', 16, 22, 80, 46, POWER_KEY)");
    expect(remote).toContain('$' + '{POWER_ICON_PATHS}');
  });

  it('keeps the preview drawing its own transport and power glyphs', () => {
    const css = readFileSync(join(SRC, 'shell2', 'shell2.css'), 'utf8');
    expect(css).toContain('html.ui2 .ui2-player .controls :is(#pauseBtn, #stopBtn, #powerBtn) > svg { display: none; }');
  });
});

// Every text colour token clears WCAG AA (4.5:1) on every surface it sits on,
// in all three themes. A tester could not read the preset hint ("hold = save
// the current station here"), the bitrate line on a tile or the settings
// descriptions in the dark theme: --c-faint was tuned against the page only,
// while a preset tile is --c-surface-2 and turns --c-surface-3 under the
// mouse (4.10:1 and 3.61:1). This test reads the tokens straight out of
// style.css so a later palette tweak cannot quietly drop below the line.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');

function block(selector) {
  const i = css.indexOf(selector + ' {');
  if (i < 0) throw new Error('no block ' + selector);
  const end = css.indexOf('}', i);
  const tok = {};
  for (const m of css.slice(i, end).matchAll(/--([a-z0-9-]+):\s*(#[0-9a-fA-F]{3,6})\b/g)) {
    let h = m[2].toLowerCase();
    if (h.length === 4) h = '#' + [...h.slice(1)].map((c) => c + c).join('');
    tok[m[1]] = h;
  }
  return tok;
}

const root = block(':root');
const THEMES = {
  dark: root,
  light: { ...root, ...block('html.a11y-light') },
  contrast: { ...root, ...block('html.a11y-contrast') },
};

function lum(hex) {
  const c = [1, 3, 5].map((i) => {
    const x = parseInt(hex.slice(i, i + 2), 16) / 255;
    return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
}
function ratio(a, b) {
  const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

// Surfaces any grey text can land on: page, card, tile, hovered tile, the
// selected (brand) card, and both ends of the playing / error tile gradients.
const SURFACES = ['c-bg', 'c-surface', 'c-surface-2', 'c-surface-3', 'brand-bg',
  'c-tile-play-a', 'c-tile-play-b', 'c-tile-err-a', 'c-tile-err-b'];
// The surfaces most text actually sits on, where the muted tiers get a margin
// on top of AA instead of scraping past it.
const MAIN = ['c-bg', 'c-surface', 'c-surface-2'];

const GREYS = ['c-text', 'c-text-2', 'c-muted', 'c-faint'];
const ACCENTS = ['brand', 'brand-text-mid', 'signal-green', 'signal-yellow', 'signal-red', 'c-spotify-text', 'c-amber-text'];

describe('theme text contrast', () => {
  for (const [name, t] of Object.entries(THEMES)) {
    describe(name, () => {
      it('defines every token the checks need', () => {
        for (const k of [...SURFACES, ...GREYS, ...ACCENTS]) expect(t[k], `--${k}`).toMatch(/^#[0-9a-f]{6}$/);
      });

      it('grey text tokens clear 4.5:1 on every surface', () => {
        const bad = [];
        for (const fg of GREYS) for (const bg of SURFACES) {
          const r = ratio(t[fg], t[bg]);
          if (r < 4.5) bad.push(`--${fg} ${t[fg]} on --${bg} ${t[bg]}: ${r.toFixed(2)}`);
        }
        expect(bad).toEqual([]);
      });

      it('muted and faint text keep a margin (5.5:1) on the main surfaces', () => {
        const bad = [];
        for (const fg of ['c-muted', 'c-faint']) for (const bg of MAIN) {
          const r = ratio(t[fg], t[bg]);
          if (r < 5.5) bad.push(`--${fg} ${t[fg]} on --${bg} ${t[bg]}: ${r.toFixed(2)}`);
        }
        expect(bad).toEqual([]);
      });

      it('accent colours used as text clear 4.5:1 on the main surfaces', () => {
        const bad = [];
        for (const fg of ACCENTS) for (const bg of MAIN) {
          const r = ratio(t[fg], t[bg]);
          if (r < 4.5) bad.push(`--${fg} ${t[fg]} on --${bg} ${t[bg]}: ${r.toFixed(2)}`);
        }
        // The state lines on the playing / error tiles.
        for (const bg of ['c-tile-play-a', 'c-tile-play-b']) {
          const r = ratio(t['signal-green'], t[bg]);
          if (r < 4.5) bad.push(`--signal-green on --${bg}: ${r.toFixed(2)}`);
        }
        for (const bg of ['c-tile-err-a', 'c-tile-err-b']) {
          const r = ratio(t['signal-red'], t[bg]);
          if (r < 4.5) bad.push(`--signal-red on --${bg}: ${r.toFixed(2)}`);
        }
        expect(bad).toEqual([]);
      });

      if (name !== 'contrast') {
        // High contrast collapses every tier to white on purpose.
        it('keeps the hierarchy: primary > secondary > muted > faint', () => {
          const r = GREYS.map((k) => ratio(t[k], t['c-surface']));
          for (let i = 1; i < r.length; i++) expect(r[i], `${GREYS[i]} vs ${GREYS[i - 1]}`).toBeLessThan(r[i - 1]);
        });
      }
    });
  }
});

describe('small text uses colour tokens, not opacity', () => {
  // opacity over --c-text gave a grey nobody could measure per theme.
  for (const sel of ['.preset-source', '.preset-box-hint', '.preset-hint', '.preset-bitrate', '.zone-card-host']) {
    it(sel, () => {
      const m = css.match(new RegExp('\\n' + sel.replace('.', '\\.') + ' \\{([^}]*)\\}'));
      expect(m, sel).not.toBeNull();
      expect(m[1]).not.toMatch(/opacity/);
      expect(m[1]).toMatch(/color:\s*var\(--c-(muted|faint)\)/);
    });
  }

  it('the .muted utility (settings descriptions) routes through a grey token', () => {
    expect(css).toMatch(/\n\.muted \{ color: var\(--c-(muted|faint)\);/);
  });

  it('the playing and error tiles draw their fills from the tile tokens', () => {
    expect(css).toMatch(/\.preset\.playing \{\s*background: linear-gradient\(135deg, var\(--c-tile-play-a\) 0%, var\(--c-tile-play-b\) 100%\)/);
    expect(css).toMatch(/\.preset\.error \{\s*background: linear-gradient\(135deg, var\(--c-tile-err-a\) 0%, var\(--c-tile-err-b\) 100%\)/);
  });
});

// #1486: .btn-secondary and .btn:hover paint the button with --c-border, which
// high contrast sets to pure white, under white button text. Each theme must
// leave the button label readable on both fills.
describe('button fills keep their label readable', () => {
  // The background a rule sets, read from the line that starts with it.
  function bg(selector) {
    const at = css.indexOf('\n' + selector + ' { background: ');
    if (at < 0) return null;
    const from = at + selector.length + 15;
    return css.slice(from, css.indexOf(';', from)).trim();
  }
  function fill(rule, theme) {
    const v = (theme === 'contrast' && bg('html.a11y-contrast ' + rule)) || bg(rule);
    const tok = v.match(/^var\(--([a-z0-9-]+)\)$/);
    return tok ? THEMES[theme][tok[1]] : v;
  }
  for (const theme of Object.keys(THEMES)) {
    for (const rule of ['.btn-secondary', '.btn:hover']) {
      it(`${theme}: ${rule}`, () => {
        expect(ratio(THEMES[theme]['c-text'], fill(rule, theme))).toBeGreaterThanOrEqual(4.5);
      });
    }
  }
});

// #1554: the orange caution button (Restart speaker, Copy presets, the conflict
// clean-up) had white text on #d97a1d, 3.11:1 in every theme. Its fill and
// label are theme tokens now; each pair must clear AA, hovered too, and the
// rules must actually use them.
describe('caution button (.btn-warning) label contrast', () => {
  for (const [name, t] of Object.entries(THEMES)) {
    it(`${name}: label on the fill and on the hover fill`, () => {
      for (const k of ['c-warn-on', 'c-warn-bg', 'c-warn-bg-hover']) expect(t[k], `--${k}`).toMatch(/^#[0-9a-f]{6}$/);
      expect(ratio(t['c-warn-on'], t['c-warn-bg'])).toBeGreaterThanOrEqual(4.5);
      expect(ratio(t['c-warn-on'], t['c-warn-bg-hover'])).toBeGreaterThanOrEqual(4.5);
    });
  }

  it('the rules read the tokens, no hard-coded orange or white label', () => {
    expect(css).toMatch(/\n\.btn-warning \{ background: var\(--c-warn-bg\); color: var\(--c-warn-on\);/);
    expect(css).toMatch(/\n\.btn-warning:hover \{ background: var\(--c-warn-bg-hover\);/);
    expect(css).not.toMatch(/#d97a1d;\s*color:\s*white/);
  });

  it('high contrast does not paint the hovered caution and danger buttons #333', () => {
    expect(css).toMatch(/\nhtml\.a11y-contrast \.btn-warning:hover \{ background: var\(--c-warn-bg-hover\); \}/);
    expect(css).toMatch(/\nhtml\.a11y-contrast \.btn-danger:hover \{ background: var\(--signal-red\); \}/);
  });
});

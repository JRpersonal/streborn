// The UI 2.0 colour tokens (docs/UI-2.0.md) clear WCAG AA in all three themes.
// Read straight out of shell2.css, the way themecontrast.test.js reads
// style.css, so a later palette tweak cannot quietly drop below the line.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const css = readFileSync(new URL('./shell2.css', import.meta.url), 'utf8');
const styleCss = readFileSync(new URL('../style.css', import.meta.url), 'utf8');

function block(selector) {
  const i = css.indexOf(selector + ' {');
  if (i < 0) throw new Error('no block ' + selector);
  const end = css.indexOf('}', i);
  const tok = {};
  for (const m of css.slice(i, end).matchAll(/--(u-[a-z0-9-]+):\s*([^;]+);/g)) tok[m[1]] = m[2].trim();
  return tok;
}

const base = block('html.ui2');
const THEMES = {
  dark: base,
  light: { ...base, ...block('html.ui2.a11y-light') },
  contrast: { ...base, ...block('html.ui2.a11y-contrast') },
};
const COLORS = ['u-bg', 'u-sidebar', 'u-surface', 'u-selected', 'u-action', 'u-on-action', 'u-accent', 'u-text',
  'u-muted', 'u-border', 'u-ok', 'u-warn', 'u-danger', 'u-danger-fill', 'u-on-danger', 'u-focus'];

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

describe('UI 2.0 tokens', () => {
  for (const [name, tk] of Object.entries(THEMES)) {
    describe(name, () => {
      const col = (k) => tk[k].toLowerCase();
      it('defines every colour token as a six-digit hex value', () => {
        for (const k of COLORS) expect(tk[k], `--${k}`).toMatch(/^#[0-9a-fA-F]{6}$/);
      });
      it('text, muted and the state colours reach 4.5:1 on every surface', () => {
        for (const fg of ['u-text', 'u-muted', 'u-ok', 'u-warn', 'u-danger', 'u-accent']) {
          for (const bg of ['u-bg', 'u-sidebar', 'u-surface']) {
            expect(ratio(col(fg), col(bg)), `${fg} on ${bg}`).toBeGreaterThanOrEqual(4.5);
          }
        }
      });
      it('text, accent and the pill colours stay readable on a selected row', () => {
        // The danger and warn pills too: the speaker you control can be the
        // offline one, and its row is the selected one.
        for (const fg of ['u-text', 'u-accent', 'u-ok', 'u-muted', 'u-danger', 'u-warn']) {
          expect(ratio(col(fg), col('u-selected')), `${fg} on u-selected`).toBeGreaterThanOrEqual(4.5);
        }
      });
      it('labels on filled buttons reach 4.5:1', () => {
        expect(ratio(col('u-on-action'), col('u-action'))).toBeGreaterThanOrEqual(4.5);
        expect(ratio(col('u-on-danger'), col('u-danger-fill'))).toBeGreaterThanOrEqual(4.5);
      });
      it('filled buttons (action, danger) keep 3:1 against every surface they sit on', () => {
        for (const fill of ['u-action', 'u-danger-fill', 'u-danger']) {
          for (const bg of ['u-bg', 'u-sidebar', 'u-surface']) {
            expect(ratio(col(fill), col(bg)), `${fill} on ${bg}`).toBeGreaterThanOrEqual(3);
          }
        }
      });
      it('borders, the action colour and the focus ring reach 3:1 against the page', () => {
        for (const fg of ['u-border', 'u-action', 'u-focus']) {
          for (const bg of ['u-bg', 'u-sidebar']) {
            expect(ratio(col(fg), col(bg)), `${fg} on ${bg}`).toBeGreaterThanOrEqual(3);
          }
        }
      });
    });
  }

  it('greys an offline speaker by colour, never by opacity', () => {
    const offline = [...css.matchAll(/([^{}]*is-offline[^{}]*)\{([^}]*)\}/g)];
    expect(offline.length).toBeGreaterThan(0);
    for (const m of offline) {
      expect(m[2], m[1]).not.toMatch(/opacity/);
      expect(m[2], m[1]).toContain('var(--u-muted)');
    }
  });

  it('sizes the sidebar speaker lists with tokens', () => {
    expect(base['u-spk-row']).toBe('44px');
    expect(Number(base['u-spk-visible'])).toBeGreaterThan(Number(base['u-spk-pinned']));
  });

  it('draws the danger button edge in the danger colour', () => {
    expect(css).toMatch(/html\.ui2 \.u-btn--danger \{[^}]*border-color: var\(--u-danger\)/);
  });

  it('keeps the 8px grid, 40px targets and the system UI font', () => {
    expect(base['u-space']).toBe('8px');
    expect(base['u-target']).toBe('40px');
    expect(base['u-radius']).toBe('8px');
    expect(base['u-font']).toMatch(/^system-ui/);
  });

  it('lives in shell2.css alone, and reads none of the current tokens', () => {
    expect(styleCss).not.toMatch(/--u-[a-z]/);
    expect(css).not.toMatch(/var\(--c-/);
  });

  it('scopes every rule under html.ui2, so the preview off changes nothing', () => {
    const noComments = css.replace(/\/\*[\s\S]*?\*\//g, '');
    const selectors = [];
    // Every selector list in front of a declaration block, at-rules aside.
    for (const m of noComments.matchAll(/([^{}]+)\{/g)) {
      const sel = m[1].trim();
      if (sel.startsWith('@')) continue;
      for (const s of sel.split(/,(?![^(]*\))/)) selectors.push(s.trim());
    }
    expect(selectors.length).toBeGreaterThan(50);
    for (const s of selectors) expect(s, s).toMatch(/^html\.ui2($|[\s.:])/);
  });
});

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

  it('names the Levels sliders in a checked text colour', () => {
    expect(css).toMatch(/html\.ui2 \.ui2-player-pop \.group-member-name \{[^}]*color: var\(--u-text\)/);
    expect(css).toMatch(/html\.ui2 \.ui2-player-pop \.group-member-vol \.vol-val \{[^}]*color: var\(--u-muted\)/);
  });

  it('sizes the sidebar speaker lists with tokens', () => {
    expect(base['u-spk-row']).toBe('44px');
    expect(Number(base['u-spk-pinned'])).toBeGreaterThan(1);
    // The other speakers take the rest of the sidebar: no row cap of their own.
    expect(base['u-spk-visible']).toBeUndefined();
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

  it('defines the spacing scale, the type scale, the weights, the pill radius and the focus ring', () => {
    expect(['1', '2', '3', '4', '5', '6'].map((n) => base[`u-space-${n}`])).toEqual(['4px', '8px', '12px', '16px', '24px', '32px']);
    expect(base['u-space-2']).toBe(base['u-space']);
    expect(['xs', 'sm', 'md', 'md-plus', 'lg', 'lg-plus', 'xl', '2xl', '2xl-plus', '3xl'].map((n) => base[`u-fs-${n}`]))
      .toEqual(['12px', '13px', '14px', '15px', '16px', '17px', '18px', '20px', '22px', '24px']);
    expect([base['u-fw-regular'], base['u-fw-semibold'], base['u-fw-bold']]).toEqual(['400', '600', '700']);
    expect(base['u-radius-pill']).toBe('12px');
    expect(base['u-focus-ring']).toBe('2px solid var(--u-focus)');
    expect(base['u-focus-offset']).toBe('2px');
  });

  // The UI 2.0 stylesheets take their sizes from the scales above, so a later
  // redesign changes a size in one place. A literal that equals a step would
  // quietly stay behind when the step moves.
  const UI2_SHEETS = ['./shell2.css', './banners.css', './library.css', './recent.css', './state.css', './help/help.css',
    './setup/check.css', './setup/setup.css', './speakers/groups.css', './speakers/list.css', './speakers/speaker.css',
    './speakers/speakers.css', '../views/music/ui2radio.css'];
  const SPACE_STEPS = new Set(['4px', '8px', '12px', '16px', '24px', '32px']);
  const SPACING = /^(?:padding|margin|inset)(?:-[a-z-]+)?$|^(?:row-|column-)?gap$/;

  it('every UI 2.0 stylesheet reads sizes from the tokens and only tokens that exist', () => {
    for (const name of UI2_SHEETS) {
      const body = readFileSync(new URL(name, import.meta.url), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
      for (const m of body.matchAll(/var\(--(u-[a-z0-9-]+)\)/g)) expect(DEFINED.has(m[1]), `${name}: --${m[1]}`).toBe(true);
      for (const m of body.matchAll(/(?:^|[{;\s])([a-z-]+)\s*:\s*([^;{}]+)(?=[;}])/g)) {
        const [prop, value] = [m[1], m[2].trim()];
        if (prop === 'font-size') expect(value, `${name}: font-size`).toMatch(/^(?:var\(--u-fs-[a-z0-9-]+\)|0)$/);
        if (prop === 'outline') expect(value, `${name}: outline`).not.toMatch(/^2px solid var\(--u-focus\)/);
        if (SPACING.test(prop)) {
          for (const part of value.split(/\s+/)) expect(SPACE_STEPS.has(part), `${name}: ${prop}: ${value}`).toBe(false);
        }
      }
    }
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

// Phase 5: the banner region's cards (banners.css) and the outcome lines on a
// speaker's page (speakers/speaker.css) read only the tokens above, and every
// pair they draw clears the line in all three themes.
const bannersCss = readFileSync(new URL('./banners.css', import.meta.url), 'utf8');
const speakerCss = readFileSync(new URL('./speakers/speaker.css', import.meta.url), 'utf8');
const DEFINED = new Set(Object.keys(base));

function declarationsOf(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, '');
}

describe('UI 2.0 phase 5 banners and section messages', () => {
  it('banners.css scopes every rule under html.ui2 and the banner region', () => {
    const noComments = declarationsOf(bannersCss);
    const selectors = [];
    for (const m of noComments.matchAll(/([^{}]+)\{/g)) {
      for (const s of m[1].trim().split(/,(?![^(]*\))/)) selectors.push(s.trim());
    }
    expect(selectors.length).toBeGreaterThan(10);
    for (const s of selectors) expect(s, s).toMatch(/^html\.ui2 #ui2Banners /);
  });

  it('banners.css and speaker.css use no hex colours, only tokens shell2.css defines', () => {
    for (const [name, text] of [['banners.css', bannersCss], ['speaker.css', speakerCss]]) {
      const body = declarationsOf(text);
      expect(body, name).not.toMatch(/#[0-9a-fA-F]{3,8}\b(?![\w-]*\s*[{,])/);
      expect(body, name).not.toMatch(/rgba?\(|hsla?\(/);
      for (const m of body.matchAll(/var\(--(u-[a-z0-9-]+)\)/g)) expect(DEFINED.has(m[1]), `${name}: --${m[1]}`).toBe(true);
    }
  });

  it('draws the update card on the surface with an accent bar, the others with a warn bar', () => {
    expect(bannersCss).toMatch(/\.speaker-update-card \{[^}]*background: var\(--u-surface\)[^}]*\}/);
    expect(bannersCss).toMatch(/\.speaker-update-card \{[^}]*border-inline-start: 4px solid var\(--u-accent\)/);
    expect(bannersCss).toMatch(/\.global-security-banner \{[^}]*border-inline-start: 4px solid var\(--u-warn\)/);
    expect(bannersCss).toMatch(/#boxWedgeBanner,[\s\S]*#boxIssueBanner,[\s\S]*\{\s*display: none;/);
  });

  for (const [name, tk] of Object.entries(THEMES)) {
    it(`${name}: the banner and section message pairs reach their contrast`, () => {
      const col = (k) => tk[k].toLowerCase();
      // Words: the card's text, the muted version, the danger and warn lines
      // and the accent link, on the surface they sit on.
      for (const fg of ['u-text', 'u-muted', 'u-danger', 'u-warn', 'u-accent']) {
        expect(ratio(col(fg), col('u-surface')), `${fg} on u-surface`).toBeGreaterThanOrEqual(4.5);
      }
      // The update button and its label.
      expect(ratio(col('u-on-action'), col('u-action')), 'u-on-action on u-action').toBeGreaterThanOrEqual(4.5);
      // The 4px bars (accent, warn, danger) and the focus ring: 3:1 against
      // the card and the page around it.
      for (const bar of ['u-accent', 'u-warn', 'u-danger', 'u-focus', 'u-border']) {
        for (const bg of ['u-surface', 'u-bg']) {
          expect(ratio(col(bar), col(bg)), `${bar} on ${bg}`).toBeGreaterThanOrEqual(3);
        }
      }
      // The report's text field sits on the page colour inside the card.
      expect(ratio(col('u-text'), col('u-bg')), 'u-text on u-bg').toBeGreaterThanOrEqual(4.5);
    });
  }
});

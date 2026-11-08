import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// The phone remote (internal/webui/assets/index.html) repainted its mute glyph
// only from /api/box/settings, which it reads on load and on a return to the
// page, so a mute set in the desktop app or with the speaker's remote never
// showed while the page stayed open (#1748). The 5 s status poll now carries
// the agent's flag in X-STR-Muted and statusMute paints it. Lifted off the
// served page, the same way phonestoppedkey.test.js does it.

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8');

function lift(signature) {
  const start = page.indexOf(signature);
  expect(start, `${signature} missing from the phone page`).toBeGreaterThan(-1);
  const end = page.indexOf('\n}\n', start);
  return page.slice(start, end + 2);
}

const src = [lift('function renderMute('), lift('function shownMuted('), lift('function statusMute(')].join('\n');

function load(muteUntil = 0) {
  const attrs = { 'aria-pressed': 'false' };
  const button = {
    innerHTML: '',
    paints: 0,
    setAttribute(k, v) {
      attrs[k] = v;
      this.paints++;
    },
    getAttribute: (k) => attrs[k],
    classList: { toggle() {} },
  };
  const fns = new Function('document', 'env', `var muteUntil = env.muteUntil; ${src}; return { statusMute, shownMuted };`)(
    { getElementById: (id) => (id === 'volMute' ? button : null) },
    { muteUntil },
  );
  return { ...fns, button };
}

describe('phone page mute glyph from the status poll', () => {
  it('shows a mute set elsewhere, and its lifting', () => {
    const p = load();
    p.statusMute('1');
    expect(p.shownMuted()).toBe(true);
    p.statusMute('0');
    expect(p.shownMuted()).toBe(false);
  });

  it('keeps the glyph when the agent does not know the flag', () => {
    const p = load();
    p.statusMute('1');
    p.statusMute(null);
    p.statusMute('');
    expect(p.shownMuted()).toBe(true);
  });

  it('does not flip the glyph back right after a press on the phone', () => {
    const p = load(Date.now() + 10000);
    p.statusMute('1');
    expect(p.shownMuted()).toBe(false);
    expect(p.button.paints).toBe(0);
  });

  it('does not repaint every poll when nothing changed', () => {
    const p = load();
    p.statusMute('0');
    p.statusMute('0');
    expect(p.button.paints).toBe(0);
  });
});

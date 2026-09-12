// The firmware banner used to send every speaker to ONE Bose support article,
// the SoundTouch 20 Series III one. A SoundTouch 10 owner opened a page showing
// a different speaker, and the owner of a Series II ST20 was pointed at the
// Series III page. That surfaced on 2026-09-09 from a reporter whose Series II
// had lost its sound after a firmware update.
//
// The view is read as source here, the same way the bass-control tests read it:
// it cannot be imported without a DOM and the Wails runtime.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('./views/settings.js', import.meta.url), 'utf8');

// The models the app tracks a latest firmware for. Any model listed there can
// show the outdated banner, so any model listed there needs an article.
function modelsWithLatestFw() {
  const block = src.slice(src.indexOf('const LATEST_FW'), src.indexOf('};', src.indexOf('const LATEST_FW')));
  return [...block.matchAll(/'(SoundTouch[^']*)'/g)].map(m => m[1]);
}

function articleBlock() {
  const start = src.indexOf('const BOSE_FW_ARTICLES');
  expect(start, 'the per-model article table must exist').toBeGreaterThan(-1);
  return src.slice(start, src.indexOf('\n};', start));
}

describe('Bose firmware support links', () => {
  it('no longer sends every model to one hardcoded article', () => {
    expect(src).not.toContain('BOSE_FW_SUPPORT_URL');
  });

  it('has an article for every model that can show the outdated banner', () => {
    const block = articleBlock();
    const models = modelsWithLatestFw();
    expect(models.length).toBeGreaterThan(0);
    for (const m of models) {
      expect(block, `${m} has no support article`).toContain(`'${m}'`);
    }
  });

  it('points each model at its OWN article', () => {
    const block = articleBlock();
    const want = {
      'SoundTouch 10': 'soundtouch-10-updating',
      'SoundTouch Portable': 'soundtouch-portable-updating',
    };
    for (const [model, slug] of Object.entries(want)) {
      const from = block.indexOf(`'${model}'`);
      const section = block.slice(from, block.indexOf('],', from));
      expect(section, `${model} must link its own article`).toContain(slug);
    }
  });

  // The speaker does not report which series it is (docs/MODEL-VARIANTS.md:
  // moduleType separates sm2 from scm, not Series II from Series III), so the
  // two models that exist in both offer both articles instead of guessing.
  it('offers both series where the model exists twice', () => {
    const block = articleBlock();
    for (const model of ['SoundTouch 20', 'SoundTouch 30']) {
      const from = block.indexOf(`'${model}'`);
      const section = block.slice(from, block.indexOf('],\n', from));
      expect(section, `${model} must offer the Series II article`).toContain('Series II');
      expect(section, `${model} must offer the Series III article`).toContain('Series III');
    }
  });

  // Wired by class, because a model with two series renders two links and two
  // elements cannot share an id.
  it('wires the guide links by class rather than by id', () => {
    expect(src).toContain('fw-guide-link');
    expect(src).not.toContain(`$('fwGuideLink')`);
  });
});

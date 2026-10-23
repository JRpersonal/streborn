// The firmware banner used to send every speaker to ONE Bose support article,
// the SoundTouch 20 Series III one. A SoundTouch 10 owner opened a page showing
// a different speaker, and the owner of a Series II ST20 was pointed at the
// Series III page. That surfaced on 2026-09-09 from a reporter whose Series II
// had lost its sound after a firmware update.
//
// The view is read as source here, the same way the bass-control tests read it:
// it cannot be imported without a DOM and the Wails runtime.
import { describe, it, expect } from 'vitest';
import { allSource, fileSource, srcOf } from './test/source.js';

const all = allSource();

// The models the app tracks a latest firmware for. Any model listed there can
// show the outdated banner, so any model listed there needs an article.
function modelsWithLatestFw() {
  return [...srcOf('LATEST_FW').matchAll(/'(SoundTouch[^']*)'/g)].map(m => m[1]);
}

// modelSection returns one model's entry from the article table: from its key up
// to the next key or the end of the table. Slicing to the first '],' instead cut
// a two-series model off after its first article.
function modelSection(block, model) {
  const from = block.indexOf(`'${model}'`);
  expect(from, `${model} is not in the article table`).toBeGreaterThan(-1);
  const after = from + model.length + 2;
  const next = block.slice(after).search(/\n\s*'/);
  return next < 0 ? block.slice(from) : block.slice(from, after + next);
}

function articleBlock() {
  return srcOf('BOSE_FW_ARTICLES');
}

describe('Bose firmware support links', () => {
  it('no longer sends every model to one hardcoded article', () => {
    expect(all).not.toContain('BOSE_FW_SUPPORT_URL');
  });

  // Read by file on purpose: this test is about which module imports which.
  it('lives outside the views, where both of them can import it', () => {
    const settings = fileSource('views/settings.js');
    const setup = fileSource('views/setup.js');
    expect(settings).toContain("from '../firmware.js'");
    // setup.js reaches the firmware facts through the guide, which imports them.
    expect(setup).toContain("from '../fwguide.js'");
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
      expect(modelSection(block, model), `${model} must link its own article`).toContain(slug);
    }
  });

  // The speaker does not report which series it is (docs/MODEL-VARIANTS.md:
  // moduleType separates sm2 from scm, not Series II from Series III), so the
  // two models that exist in both offer both articles instead of guessing.
  it('offers both series where the model exists twice', () => {
    const block = articleBlock();
    for (const model of ['SoundTouch 20', 'SoundTouch 30']) {
      const section = modelSection(block, model);
      expect(section, `${model} must offer the Series II article`).toContain('Series II');
      expect(section, `${model} must offer the Series III article`).toContain('Series III');
    }
  });

  // Wired by class, because a model with two series renders two links and two
  // elements cannot share an id.
  it('wires the guide links by class rather than by id', () => {
    expect(all).toContain('fw-guide-link');
    expect(all).not.toContain(`$('fwGuideLink')`);
  });
});

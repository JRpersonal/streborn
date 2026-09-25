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

// Newlines are normalised: a Windows checkout has CRLF here and a Linux one LF,
// and the section slices below keyed on a newline. That cut a different amount
// of text on each platform, so a real assertion passed on one and failed on the
// other for a reason that had nothing to do with the code under test.
const src = readFileSync(new URL('./firmware.js', import.meta.url), 'utf8')
  .replace(/\r\n/g, '\n');

// The models the app tracks a latest firmware for. Any model listed there can
// show the outdated banner, so any model listed there needs an article.
function modelsWithLatestFw() {
  const block = src.slice(src.indexOf('const LATEST_FW'), src.indexOf('};', src.indexOf('const LATEST_FW')));
  return [...block.matchAll(/'(SoundTouch[^']*)'/g)].map(m => m[1]);
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
  const start = src.indexOf('const BOSE_FW_ARTICLES');
  expect(start, 'the per-model article table must exist').toBeGreaterThan(-1);
  return src.slice(start, src.indexOf('\n};', start));
}

describe('Bose firmware support links', () => {
  it('no longer sends every model to one hardcoded article', () => {
    expect(src).not.toContain('BOSE_FW_SUPPORT_URL');
  });

  it('lives outside the views, where both of them can import it', () => {
    const settings = readFileSync(new URL('./views/settings.js', import.meta.url), 'utf8');
    const setup = readFileSync(new URL('./views/setup.js', import.meta.url), 'utf8');
    expect(settings).toContain("from '../firmware.js'");
    expect(setup).toContain("from '../firmware.js'");
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
    const view = readFileSync(new URL('./views/settings.js', import.meta.url), 'utf8');
    expect(view).toContain('fw-guide-link');
    expect(view).not.toContain(`$('fwGuideLink')`);
  });
});

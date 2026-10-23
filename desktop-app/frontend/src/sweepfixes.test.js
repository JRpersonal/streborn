// Four things the pre-release sweep caught on 2026-09-11, three of them in code
// that had shipped or was about to.
//
// The worst was mine from the day before. The library delivery probe was written
// as a fetch() from this page to the media server, and a DLNA server sends no
// Access-Control-Allow-Origin, so the browser refuses the answer however healthy
// the server is. Measured against two servers on one LAN: both answer 206 with
// the requested range and no access-control header at all. The catch returned
// false, which is the server-is-at-fault verdict, so every failed library play
// would have blamed the user's NAS, including the high-resolution FLAC the whole
// function was written around (#139).
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { allSource, srcOf } from './test/source.js';

const all = allSource();
const probe = srcOf('probeDelivery');
const verify = srcOf('verifyLibraryPlayback');
const dissolve = srcOf('doDissolveStereo');
const bundleDir = fileURLToPath(new URL('./i18n/bundles/', import.meta.url));
const langs = readdirSync(bundleDir).filter((f) => f.endsWith('.json')).map((f) => f.replace('.json', ''));
const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));

describe('the library delivery probe', () => {
  it('does not fetch the media server from this page', () => {
    expect(all, 'the CORS-blind probe must be gone').not.toContain('serverDeliversTrack');
    expect(probe, 'the probe must go through the Go binding').toContain('ProbeTrackDelivery');
    expect(probe).not.toContain('fetch(');
    expect(all).toContain("callOptionalBinding('ProbeTrackDelivery'");
  });

  // The three-state answer is the whole point. A probe that could not be taken
  // says nothing about the server, and the old code's own comment said so while
  // the code did the opposite.
  it('only blames the server on a probe that actually answered', () => {
    const at = verify.indexOf('const probe = await probeDelivery');
    expect(at).toBeGreaterThan(-1);
    const decision = verify.slice(at, at + 520);
    expect(decision).toContain('probe.known && !probe.delivered');
    expect(decision).toContain('library.serverNotDelivering');
    expect(decision).toContain('library.formatMaybeUnsupported');
    // An unknown probe keeps the older wording, so a blocked app cannot invent
    // a verdict about a server the speaker reaches perfectly well.
    expect(probe).toContain('known: false');
  });
});

describe('the placeholders in every bundle', () => {
  // t() substitutes DOUBLE braces only. Both library failure toasts used single
  // braces, so they printed a literal "{title}" at the user in all 13 languages.
  // One key was new; the other had been shipping broken since v0.7.22.
  it('use double braces, in every language and every key', () => {
    const offenders = [];
    for (const l of langs) {
      const b = bundle(l);
      for (const [k, v] of Object.entries(b)) {
        if (/(?<!\{)\{\w+\}(?!\})/.test(v)) offenders.push(`${l}:${k}`);
      }
    }
    expect(offenders, 'single-brace placeholders are never substituted').toEqual([]);
  });

  it('kept the track name in both library failure toasts', () => {
    for (const l of langs) {
      const b = bundle(l);
      expect(b['library.formatMaybeUnsupported'], l).toContain('{{title}}');
      expect(b['library.serverNotDelivering'], l).toContain('{{title}}');
    }
  });
});

describe('undo stereo pair', () => {
  // The agent has two sentinels and the view knew only one, so the other was
  // printed at the user as "Could not change the group: stereo-undo-unconfirmed".
  // A released agent still produces it, so this is not hypothetical.
  it('never shows the raw stereo-undo-unconfirmed token', () => {
    expect(all).toContain("includes('stereo-undo-unconfirmed')");
    expect(all).toContain('multiroom.stereoUndoUnconfirmed');
    // Both dissolve paths, not just one.
    expect(all.match(/stereo-undo-unconfirmed/g).length).toBeGreaterThanOrEqual(2);
    expect(all.match(/multiroom\.stereoUndoUnconfirmed/g).length).toBeGreaterThanOrEqual(2);
  });

  // Not an error: the teardown may well have happened and the speaker simply did
  // not say so. It is shown as a warning, above the generic failure branch.
  it('reads as a warning, and is decided before the generic failure', () => {
    const at = dissolve.indexOf('} else if (unconfirmed) {');
    expect(at).toBeGreaterThan(-1);
    expect(at).toBeLessThan(dissolve.indexOf('} else if (failure) {'));
    expect(dissolve.slice(at, at + 420)).toContain('setup-warn');
  });

  it('has the text in every language', () => {
    for (const l of langs) {
      expect(bundle(l)['multiroom.stereoUndoUnconfirmed'], l).toBeTruthy();
    }
    expect(bundle('de')['multiroom.stereoUndoUnconfirmed']).not.toMatch(/\s[-–—]\s/);
    expect(bundle('de')['multiroom.stereoUndoUnconfirmed']).not.toMatch(/\b(ae|oe|ue)\b/);
  });
});

// Two reports from the same day, both about a network that went away, and both
// answered by blaming something that was not at fault.
//
// #916, shorty310: he pulled his Wi-Fi in the middle of the app-update download
// and got the copyable error modal, which reads as "send this to the
// developer", followed by a button that had turned into "Get it from the
// downloads page". His own two questions were the fix: nobody needs to report
// their own Wi-Fi, and the obvious next move is to press download again.
//
// #915, mash8336: his files are plain mp3 and the app told him the format was
// probably unsupported. His speaker logged AUDIO_ERROR_TIMEOUT and
// ERROR_NO_DECODED_DATA twenty seconds into every attempt, which is the box
// saying nothing ever arrived to decode, and his media server did not answer its
// own description fetch either.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
const library = readFileSync(new URL('./views/library.js', import.meta.url), 'utf8');
const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));
const en = bundle('en');
const de = bundle('de');

// The classifier, mirrored here so it can be exercised without importing main.js
// (which needs a DOM and the Wails runtime).
const interrupted = (err) => {
  const s = String(err || '').toLowerCase();
  return [
    'network is unreachable', 'unreachable network',
    'no route to host', 'no such host', 'dns',
    'connection reset', 'connection refused', 'broken pipe',
    'eof', 'unexpected eof',
    'timeout', 'deadline exceeded', 'timed out',
    'canceled', 'cancelled', 'aborted',
    'i/o error', 'connection was aborted',
  ].some((m) => s.includes(m));
};

describe('an app-update download the network cut off', () => {
  it('is told apart from a real failure', () => {
    for (const s of [
      'Get "https://github.com/...": dial tcp: connect: network is unreachable',
      'read tcp 10.0.0.5:1234->140.82.121.4:443: connection reset by peer',
      'unexpected EOF after 12345678 bytes',
      'context deadline exceeded',
    ]) expect(interrupted(s), s).toBe(true);
    // These are genuine failures of the update itself and keep the full error
    // and the web fallback.
    for (const s of [
      'sha256 mismatch: got abc, want def',
      'open /Applications/ST Reborn.app: permission denied',
      'no matching asset for darwin/arm64 in the release manifest',
    ]) expect(interrupted(s), s).toBe(false);
  });

  it('keeps the button as a retry instead of sending the user to a web page', () => {
    const at = main.indexOf('if (downloadWasInterrupted(e))');
    expect(at, 'the interrupted branch must exist').toBeGreaterThan(-1);
    // Only this arm: the else arm below it is the real-failure path and is
    // supposed to still carry showError and the downloads-page fallback.
    const branch = main.slice(at, main.indexOf('} else {', at));
    expect(branch).toContain('banner.retryAppUpdate');
    expect(branch).toContain('runAppUpdate(');
    // The two things his report was about: no copyable error, no downloads page.
    expect(branch).not.toContain('showError');
    expect(branch).not.toContain('getFromReleases');
  });

  it('still shows the full error for a failure that IS worth reporting', () => {
    const at = main.indexOf('if (downloadWasInterrupted(e))');
    const whole = main.slice(at, at + 1400);
    expect(whole).toContain('showError');
    expect(whole).toContain('banner.getFromReleases');
  });

  it('says nothing needs reporting, in English and German', () => {
    for (const [lang, b] of [['en', en], ['de', de]]) {
      expect(b['banner.retryAppUpdate'], lang).toBeTruthy();
      expect(b['banner.updateInterrupted'], lang).toBeTruthy();
    }
    expect(en['banner.updateInterrupted']).toMatch(/nothing to report/i);
    expect(de['banner.updateInterrupted']).toMatch(/nichts zu melden/i);
    expect(de['banner.updateInterrupted']).not.toMatch(/\s[-–—]\s/);
  });
});

describe('a library track that did not start', () => {
  it('waits past the speaker\'s own verdict before saying anything', () => {
    const at = library.indexOf('async function verifyLibraryPlayback');
    const fn = library.slice(at, at + 1600);
    const m = fn.match(/Date\.now\(\)\s*\+\s*(\d+)/);
    expect(m, 'the verify window must be findable').toBeTruthy();
    // The box reports AUDIO_ERROR_TIMEOUT about 20 s in. Deciding before that
    // is guessing, which is what produced the format claim on a plain mp3.
    expect(Number(m[1])).toBeGreaterThan(20000);
  });

  // The probe moved into Go on 2026-09-11: a fetch() from this page to a DLNA
  // server is refused by the browser (no Access-Control-Allow-Origin), so the
  // frontend version reported every server as dead. The intent is unchanged,
  // which is why this test stayed: measure, do not guess.
  it('measures whether the server delivers instead of blaming the format', () => {
    expect(library).toContain('async function probeDelivery');
    expect(library).toContain('ProbeTrackDelivery');
    const at = library.indexOf('const probe = await probeDelivery');
    expect(at).toBeGreaterThan(-1);
    const decision = library.slice(at, at + 520);
    expect(decision).toContain('library.formatMaybeUnsupported');
    expect(decision).toContain('library.serverNotDelivering');
  });

  // A probe the app itself could not run says nothing about the server: the app
  // may be blocked from it while the speaker is not.
  it('only blames the server on a probe that plainly produced no bytes', () => {
    // Three states, and the third is the point: a probe that could not be taken
    // is not a verdict against the server, because the app can be blocked from
    // one the speaker reaches perfectly well.
    const at = library.indexOf('const probe = await probeDelivery');
    expect(library.slice(at, at + 520)).toContain('probe.known && !probe.delivered');
    const fn = library.slice(library.indexOf('async function probeDelivery'),
                             library.indexOf('async function probeDelivery') + 600);
    expect(fn).toContain('known: false');
  });

  it('has the new wording, and it does not mention the format', () => {
    for (const [lang, b] of [['en', en], ['de', de]]) {
      expect(b['library.serverNotDelivering'], lang).toBeTruthy();
      expect(b['library.serverNotDelivering'], lang).not.toMatch(/FLAC|format|Format/);
    }
    expect(de['library.serverNotDelivering']).not.toMatch(/\s[-–—]\s/);
    expect(de['library.serverNotDelivering']).not.toMatch(/\b(ae|oe|ue)\b/);
  });
});

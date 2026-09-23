// The "press a saved Spotify button" notice, gated (#950, #973).
//
// A reporter with a FREE Spotify account was told to press a saved Spotify key
// while nothing was playing. He has no such key and a free account cannot make
// one, so the notice pointed at a button that does not exist, and it opened
// with "Playing on ..." over a paused session.
//
// It took three attempts. The first added "really playing" and "key exists".
// The second read state.spotifyPremiumRequired, which is written only while
// STR's OWN stream plays, and that is false exactly when this notice fires, so
// the flag was always undefined at the gate and nothing changed for the
// reporter. The third asks the speaker itself.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'main.js'), 'utf8');
const block = src.slice(src.indexOf('const spotifyKeySaved'), src.indexOf('const spotifyKeySaved') + 900);
const helper = src.slice(src.indexOf('function spotifyKeyUsableFor'), src.indexOf('function spotifyKeyUsableFor') + 1200);

describe('native Spotify notice', () => {
  it('needs a speaker that is actually playing', () => {
    expect(block).toContain("ps === 'PLAY_STATE'");
  });

  it('needs a saved Spotify key to point at', () => {
    expect(src).toContain("state.presets || []).some(p => p && p.type === 'spotify')");
    expect(block).toContain('spotifyKeySaved');
  });

  it('reads the play state before it decides, not after', () => {
    // The original bug: playStatus was parsed thirteen lines BELOW the notice.
    expect(src.indexOf('<playStatus>')).toBeLessThan(src.indexOf('play.nativeSpotifyHint'));
  });

  it('remembers per speaker, not globally', () => {
    expect(block).toContain('spotifyWarnKey');
    expect(block).not.toContain('nativeSpotifyWarned = true');
  });

  it('asks the speaker whether that key could play at all', () => {
    expect(block).toContain('spotifyKeyUsableFor(state.currentBox) === true');
    expect(helper).toContain('np.canRecall');
    expect(helper).toContain('np.premiumRequired');
  });

  it('does not read the flag that is never set on this path', () => {
    // state.spotifyPremiumRequired is written inside `if (isSpotifyNow)`, which
    // requires STR's own stream. The notice fires when the box's own receiver
    // plays, so isSpotifyNow is false and the flag never arrives. Gating on it
    // was the second fix, and it changed nothing.
    expect(block).not.toContain('state.spotifyPremiumRequired');
  });

  it('stays silent until the speaker has answered', () => {
    // Anything other than a definite yes must not produce a toast: undefined
    // means "not asked yet", and this notice has already gone out wrong twice.
    expect(block).toContain('=== true');
  });

  it('treats a missing canRecall as "cannot tell", never as no', () => {
    // An agent older than the field sends nothing. Reading that as no would
    // silence the notice for every speaker that has not been updated.
    expect(helper).toContain('np.canRecall === null || np.canRecall === undefined ? true');
  });

  it('forgets its verdict when the speaker leaves Spotify', () => {
    // The user may go and tap the speaker in Spotify precisely because the key
    // did nothing, and a cached no would outlive the fix.
    expect(block).toContain('state.spotifyKeyUsable = {}');
  });
});

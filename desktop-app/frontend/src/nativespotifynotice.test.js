// The "press a saved Spotify button" notice, gated (#950).
//
// A reporter with a FREE Spotify account was told to press a saved Spotify key
// while nothing was playing. He has no such key and a free account cannot make
// one, so the notice pointed at a button that does not exist, and it opened
// with "Playing on ..." over a paused session.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'main.js'), 'utf8');
const block = src.slice(src.indexOf('const spotifyKeySaved'), src.indexOf('const spotifyKeySaved') + 700);

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
});

// #925: "I can only add one Spotify playlist. After that I get 'This channel is
// already on Button 1'."
//
// The duplicate guard is right: for a Spotify preset it compares the playlist
// URI, and two playlists have different URIs. What the save captures is whatever
// the SPEAKER is playing, read live from it at save time, so saving while the
// previous playlist is still the one playing stores that URI again and the guard
// correctly calls it a duplicate.
//
// The message is what left him stuck. "This station is already on key 1" names
// nothing and calls a playlist a station, so it reads as a refusal to have more
// than one of them. The agent's 409 carries the other preset's name all along.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
const dir = fileURLToPath(new URL('./i18n/bundles/', import.meta.url));
const langs = readdirSync(dir).filter((f) => f.endsWith('.json')).map((f) => f.replace('.json', ''));
const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));

// The name extraction, mirrored here so it can be exercised on real 409 bodies.
const nameFrom = (s) => {
  const nm = (s.match(/"name":\s*"((?:[^"\\]|\\.)*)"/) || [])[1];
  return nm ? nm.replace(/\\(.)/g, '$1') : '';
};

describe('the already-on-key refusal', () => {
  it('pulls the colliding name out of the agent 409', () => {
    expect(nameFrom('{"code":"already-on-slot","slot":1,"name":"Rock Classics"}')).toBe('Rock Classics');
    // A name carrying a quote, which is what breaks a naive regex.
    const withQuote = '{"code":"already-on-slot","slot":2,"name":"Rock \\"Live\\""}';
    expect(nameFrom(withQuote)).toBe('Rock "Live"');
    // No name in the body: fall back rather than print an empty pair of quotes.
    expect(nameFrom('{"code":"already-on-slot","slot":3}')).toBe('');
  });

  it('uses the named message when there is a name, and the old one otherwise', () => {
    const at = main.indexOf('function showPresetSaveError');
    const fn = main.slice(at, at + 1200);
    expect(fn).toContain('preset.alreadyOnKeyNamed');
    expect(fn).toContain('preset.alreadyOnKey');
    // The choice is made on whether a name came back, so a 409 without one
    // still gets a sentence rather than an empty pair of quotes.
    expect(fn).toContain('showToast(name');
  });

  it('has the named message in every language, with both placeholders', () => {
    for (const l of langs) {
      const v = bundle(l)['preset.alreadyOnKeyNamed'];
      expect(v, l).toBeTruthy();
      expect(v, l + ' lost the name').toContain('{{name}}');
      expect(v, l + ' lost the key number').toContain('{{n}}');
    }
    // It has to say what to do, or it is only a politer refusal.
    expect(bundle('en')['preset.alreadyOnKeyNamed']).toMatch(/start/i);
    expect(bundle('de')['preset.alreadyOnKeyNamed']).toMatch(/Starte/);
    expect(bundle('de')['preset.alreadyOnKeyNamed']).not.toMatch(/\s[-–—]\s/);
  });
});

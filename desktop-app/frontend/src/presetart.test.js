// #696: station artwork on a hold-saved preset was covered by the grey
// chevron. The hold-to-save path persisted the playing ORION descriptor's
// imageUrl as the key's art. That value is built for the SPEAKER (the agent's
// loopback art proxy, internal/webui/lir.go: the box cannot fetch https, so
// the agent wraps the image), and from the user's machine 127.0.0.1:8888 is
// nothing, so the tile's <img> errored over to the DuckDuckGo stream-host
// icon, whose 404 answer is a grey-chevron image body the webview renders
// without firing onerror (documented in app_library.go). The reporter's one
// surviving key, WDCB 90.9, is the one station whose stream host has a real
// DDG icon, which is exactly the discriminator he suspected.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { appArtFromBoxArt, artCarriesBoxForm } from './utils.js';

// The wrapper exactly as the #696 bundle carries it (box-2.json, the playing
// slot's <art> tag): the loopback art proxy around the DDG icon URL the agent
// picked as box-drawable for EPIC CLASSICAL.
const WRAPPED = 'http://127.0.0.1:8888/art?u=aHR0cHM6Ly9pY29ucy5kdWNrZHVja2dvLmNvbS9pcDMvZXBpYy1jbGFzc2ljYWwuY29tLmljbw';
const ORIGIN = 'https://icons.duckduckgo.com/ip3/epic-classical.com.ico';

const b64url = (s) => Buffer.from(s).toString('base64')
  .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

describe('appArtFromBoxArt', () => {
  it('unwraps the box-loopback art-proxy URL from the #696 bundle', () => {
    expect(appArtFromBoxArt(WRAPPED)).toBe(ORIGIN);
  });
  it('unwraps a LAN-addressed wrapper too, the store rule is the origin URL', () => {
    expect(appArtFromBoxArt('http://192.0.2.60:8888/art?u=' + b64url('https://static.example/logo.png')))
      .toBe('https://static.example/logo.png');
  });
  it('drops the /icon.png stand-in: STR\'s own logo is not the station\'s art', () => {
    expect(appArtFromBoxArt('http://127.0.0.1:8888/icon.png')).toBe('');
  });
  it('drops a wrapper whose payload does not decode to a URL', () => {
    expect(appArtFromBoxArt('http://127.0.0.1:8888/art?u=YWJj')).toBe(''); // "abc"
  });
  it('passes a clean logo chain through byte-identical', () => {
    const chain = 'https://static.example/logo.png|https://icons.duckduckgo.com/ip3/example.com.ico';
    expect(appArtFromBoxArt(chain)).toBe(chain);
  });
  it('heals only the wrapped candidate inside a mixed chain', () => {
    expect(appArtFromBoxArt('https://static.example/logo.png|' + WRAPPED))
      .toBe('https://static.example/logo.png|' + ORIGIN);
  });
  it('keeps data: URIs and unparsable values untouched', () => {
    expect(appArtFromBoxArt('data:image/svg+xml;utf8,x')).toBe('data:image/svg+xml;utf8,x');
    expect(appArtFromBoxArt('not a url')).toBe('not a url');
  });
  it('returns empty for empty', () => {
    expect(appArtFromBoxArt('')).toBe('');
    expect(appArtFromBoxArt(null)).toBe('');
    expect(appArtFromBoxArt(undefined)).toBe('');
  });
});

// Unwrapping alone cannot give a #696 key its artwork back: the wrapped value
// is the ONE URL the agent had picked as box-drawable, and for the reporter's
// playing key that is icons.duckduckgo.com/ip3/epic-classical.com.ico, which
// answers 404 with the grey-chevron image body (probed live 2026-08-24). The
// webview draws that body without firing onerror (app_library.go), so the
// tile's cascade ends on the chevron with the unwrapped URL as its only
// candidate. Such keys must therefore reach healPresetLogos for a real
// re-lookup, which is what this predicate gates.
describe('artCarriesBoxForm', () => {
  it('flags the #696 bundle wrapper', () => {
    expect(artCarriesBoxForm(WRAPPED)).toBe(true);
  });
  it('flags the /icon.png stand-in', () => {
    expect(artCarriesBoxForm('http://127.0.0.1:8888/icon.png')).toBe(true);
  });
  it('flags a mixed chain with one wrapped candidate', () => {
    expect(artCarriesBoxForm('https://static.example/logo.png|' + WRAPPED)).toBe(true);
  });
  it('passes a clean chain', () => {
    expect(artCarriesBoxForm('https://static.example/logo.png|https://icons.duckduckgo.com/ip3/example.com.ico')).toBe(false);
  });
  it('passes empty and non-URL values', () => {
    expect(artCarriesBoxForm('')).toBe(false);
    expect(artCarriesBoxForm(null)).toBe(false);
    expect(artCarriesBoxForm('not a url')).toBe(false);
    expect(artCarriesBoxForm('data:image/svg+xml;utf8,x')).toBe(false);
  });
});

// The defect was never in a helper, it was the persist sites handing box-form
// art to SetPreset. There is no DOM here (the vitest environment is
// deliberately DOM-free and renderPresets writes innerHTML), so main.js is
// read as source and every site that persists or adopts art is checked for
// the unwrap, the same way utils.test.js pins the bass gate onto the settings
// view. Matched on the helper NAME so refactoring the calls stays possible;
// what must never come back is art persisted raw off the box report.
describe('main.js persists no box-form art (#696)', () => {
  const src = readFileSync(new URL('./main.js', import.meta.url), 'utf8');

  it('the hold-save orion branch unwraps the descriptor imageUrl', () => {
    // The exact poisoned shape that shipped in v0.9.56:
    expect(src).not.toMatch(/orion\.imageUrl \|\| state\.nowIcon/);
    expect(src).toContain('appArtFromBoxArt(orion.imageUrl)');
  });
  it('the direct branch and the adopt path unwrap the box <art> value', () => {
    expect(src).toContain('appArtFromBoxArt(state.nowIcon)');
    // No SetPreset argument list may carry state.nowIcon raw any more. The
    // sanitizer call itself contains the name, so blank it out first and
    // require that nothing else in the call still touches the raw icon.
    for (const call of src.match(/SetPreset\([^;]*?\);/gs) || []) {
      const scrubbed = call.replace(/appArtFromBoxArt\([^)]*\)/g, 'UNWRAPPED');
      expect(scrubbed, 'a SetPreset call persists the raw box icon:\n' + call)
        .not.toContain('state.nowIcon');
    }
  });
  it('the tile adopts and persists only the unwrapped icon', () => {
    expect(src).toContain('const adoptIcon = appArtFromBoxArt(state.nowIcon);');
    expect(src).toContain('p.art = adoptIcon;');
    // The redraw gate in refreshStatus must judge the same unwrapped value,
    // or it re-renders for an adopt the grid never performs.
    expect(src).toContain('shouldAdoptPresetArt(appArtFromBoxArt(state.nowIcon), ap)');
  });
  it('stored art from older saves is unwrapped at render time', () => {
    // Keys hold-saved by v0.9.56 and older still carry the wrapper in the
    // store; the tile must not feed it to the <img> cascade as-is.
    expect(src).toContain('addCands(appArtFromBoxArt(p.art));');
  });
  it('the logo healer treats box-form art as needing a real re-lookup', () => {
    // A key whose stored art is only box-local renders artless everywhere but
    // the box itself, and a key still carrying the wrapper unwraps to a single
    // URL that can itself be the DDG chevron 404 (the reporter's playing key
    // does, probed 2026-08-24); truthy p.art used to keep healPresetLogos
    // away from both.
    expect(src).toContain('p.name && (!appArtFromBoxArt(p.art) || artCarriesBoxForm(p.art))');
  });
});

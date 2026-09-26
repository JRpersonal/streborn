// Playing an ALBUM from the Spotify app, the now-playing line called it a
// playlist: the label was a constant, so everything Spotify sent was announced
// the same way (#709, shorty310, with the Spotify screenshot next to it).
//
// The balance in speaker settings had the opposite problem: no label at all. It
// sat under the "Volume" heading and said "Balance: centred" in the value column,
// so the reading carried the word and the control carried nothing.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const main = readFileSync(join(here, 'main.js'), 'utf8').replace(/\r\n/g, '\n');
const settings = readFileSync(join(here, 'views', 'settings.js'), 'utf8').replace(/\r\n/g, '\n');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const bundle = (l) => JSON.parse(readFileSync(join(here, 'i18n', 'bundles', `${l}.json`), 'utf8'));

// The real mapper, lifted out of main.js.
const at = main.indexOf('function spotifyContextLabelKey');
// eslint-disable-next-line no-new-func
const labelKey = new Function(`${main.slice(at, main.indexOf('\n}\n', at) + 2)}\nreturn spotifyContextLabelKey;`)();

describe('what the now-playing line calls a Spotify context', () => {
  it('calls an album an album', () => {
    expect(labelKey('spotify:album:5W9OT0a5iZlBr83a9WMKFY')).toBe('status.albumLabel');
  });

  it('still calls a playlist a playlist', () => {
    expect(labelKey('spotify:playlist:37i9dQZF1DXcBWIGoYBM5M')).toBe('status.playlistLabel');
  });

  it('names an artist and a podcast', () => {
    expect(labelKey('spotify:artist:0OdUWJ0sBjDrqHygGUXeCF')).toBe('status.artistLabel');
    expect(labelKey('spotify:show:4rOoJ6Egrf8K2IrywzwOMk')).toBe('status.podcastLabel');
    expect(labelKey('spotify:episode:512ojhOuo1ktJprKbVcKyQ')).toBe('status.podcastLabel');
  });

  it('keeps the old word when there is nothing to go on', () => {
    // An empty context, or a kind nobody has seen yet. Inventing a word here
    // would be worse than the one wrong word this fixes.
    expect(labelKey('')).toBe('status.playlistLabel');
    expect(labelKey(undefined)).toBe('status.playlistLabel');
    expect(labelKey('spotify:collection:xyz')).toBe('status.playlistLabel');
  });

  it('reads the context the speaker reports, not only the app cache', () => {
    // shorty310 streams to the speaker's OWN Spotify receiver, so the app has no
    // context of its own and the URI has to come out of the box location.
    expect(main).toContain('state.nowSpotifyContext || spotifyURIFromContainer(loc)');
  });

  it('has the words in every language', () => {
    for (const l of langs) {
      const b = bundle(l);
      for (const k of ['status.albumLabel', 'status.artistLabel', 'status.podcastLabel']) {
        expect(b[k], `${l} is missing ${k}`).toBeTruthy();
      }
    }
  });
});

describe('the balance control has a label of its own', () => {
  it('shows a heading over the slider', () => {
    expect(settings).toContain("id=\"boxBalanceHead\"");
    expect(settings).toContain("t('controls.balanceHead')");
  });

  it('hides the heading exactly when the row is hidden', () => {
    // A heading over a hidden slider is a label for nothing.
    expect(settings).toContain('const show = (on) => { row.hidden = !on; if (head) head.hidden = !on; };');
    // Inside the balance function only: the start-volume row below it has its own.
    const fn = settings.slice(settings.indexOf('export async function refreshBoxBalanceRow'),
      settings.indexOf('export async function refreshStartVolume'));
    expect(fn).not.toMatch(/row\.hidden = (true|false);/);
  });

  it('reads the value without repeating the word', () => {
    expect(settings).toContain('balanceStateLabel(b.actual)');
    expect(bundle('en')['controls.balanceCentreShort']).toBe('centred');
    expect(bundle('en')['controls.balanceCentre']).toContain('Balance');
  });

  it('has the short readings in every language', () => {
    for (const l of langs) {
      const b = bundle(l);
      for (const k of ['controls.balanceHead', 'controls.balanceCentreShort', 'controls.balanceLeftShort', 'controls.balanceRightShort']) {
        expect(b[k], `${l} is missing ${k}`).toBeTruthy();
      }
      expect(b['controls.balanceLeftShort'], l).toContain('{{n}}');
      expect(b['controls.balanceRightShort'], l).toContain('{{n}}');
    }
  });
});

// phone-preset-art: the phone remote draws a key's picture from the same
// source and in the same order as the desktop app (#1467: the two showed
// different covers on the same keys). A station whose stored favicon is http
// (which the app cannot draw) wears its host's icon first, fetched through the
// agent's /art proxy; a library folder key wears its first track's cover; an
// https logo and a Spotify cover stay what they are.
//
//   E2E_PORT=4191 npx playwright test specs/phone-preset-art.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

const ICON_SVG = PHONE.coverSVG('I', '#333333');

const PRESETS = [
  { slot: 1, name: 'Jazz Lounge', type: 'radio', stream_url: 'http://stream.jazzlounge.example/live',
    homepage: 'https://www.jazzlounge.example/', art: 'http://www.jazzlounge.example/fav.png' },
  { slot: 2, name: 'Road Trip', type: 'queue', stream_url: '',
    items: [{ url: 'http://192.0.2.60:8200/a.mp3', title: 'A', art: `${PHONE.ART_HOST}/kexp.svg` }] },
  { slot: 3, name: 'Jazz24', type: 'radio', stream_url: 'http://example.com/jazz24', art: `${PHONE.ART_HOST}/jazz.svg` },
  { slot: 4, name: 'Mix', type: 'spotify', uri: 'spotify:playlist:demo', art: `${PHONE.ART_HOST}/rock.svg` },
];

const viaAgent = (u) => '/art?u=' + Buffer.from(u).toString('base64')
  .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') + '&to=browser';

test.use({
  phoneOverrides: {
    '/api/presets': { status: 200, body: PRESETS },
    '/art': { status: 200, contentType: 'image/svg+xml', body: ICON_SVG },
  },
});

const keyArt = (page, slot) => page.locator('#presets .preset').nth(slot - 1).locator('img.art');

test('a key draws the picture the desktop app draws', async ({ phone }) => {
  const { page } = phone;
  await expect(page.locator('#presets .preset')).toHaveCount(6);

  // http favicon on the internet: the app skips it and asks DuckDuckGo for
  // the homepage host first. So does the phone, through the agent.
  await expect(keyArt(page, 1)).toHaveAttribute('src',
    viaAgent('https://icons.duckduckgo.com/ip3/www.jazzlounge.example.ico'));
  // A folder key: its first track's cover.
  await expect(keyArt(page, 2)).toHaveAttribute('src', `${PHONE.ART_HOST}/kexp.svg`);
  // An https logo and a Spotify cover are drawn as stored.
  await expect(keyArt(page, 3)).toHaveAttribute('src', `${PHONE.ART_HOST}/jazz.svg`);
  await expect(keyArt(page, 4)).toHaveAttribute('src', `${PHONE.ART_HOST}/rock.svg`);
  for (const slot of [1, 2, 3, 4]) {
    await expect(page.locator('#presets .preset').nth(slot - 1)).toHaveClass(/\bhas-art\b/);
  }
  expect(phone.errors).toEqual([]);
});

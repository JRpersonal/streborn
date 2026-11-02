// The phone remote's shuffle and repeat buttons follow the speaker (#1233):
// a mode set in the desktop app or another remote while a library folder
// plays shows here on the next status tick, read from the /api/queue answer
// that tick already asks for. A tap here is not undone by a tick that read
// the queue before the speaker had taken it.
//
//   E2E_PORT=4185 npx playwright test specs/phone-play-mode-follow.spec.mjs

import { test, expect } from '../fixtures.mjs';

const TRACK = 'http://192.0.2.60:8200/31.flac';
const STATUS_QUEUE =
  '<?xml version="1.0" encoding="UTF-8"?>' +
  '<nowPlaying deviceID="A1B2C3D4E5F6" source="UPNP">' +
  `<ContentItem source="UPNP" location="${TRACK}" isPresetable="true">` +
  '<itemName>Late Train</itemName></ContentItem>' +
  '<track>Late Train</track>' +
  '<playStatus>PLAY_STATE</playStatus></nowPlaying>';

const queue = (shuffle, repeat) => ({
  body: {
    active: true, pos: 0, shuffle, repeat,
    items: [{ title: 'Late Train', artist: 'Ada Demo Trio', album: 'Night Lines' }],
    preset_rev: 1,
  },
});

const OVERRIDES = {
  '/api/status': { body: STATUS_QUEUE, contentType: 'application/xml' },
  '/api/queue': queue(false, 'off'),
  '/api/queue/mode': { body: { shuffle: false, repeat: 'off', stored: true } },
};

test.use({ phoneOverrides: OVERRIDES });

const count = (handler, path) => handler.calls.filter((c) => new URL(c.url).pathname === path).length;

// Moves the page clock to the next 5 s status tick and waits until that tick
// has read the queue.
async function queueTick(page, handler) {
  const before = count(handler, '/api/queue');
  await page.clock.fastForward(5000);
  await expect.poll(() => count(handler, '/api/queue')).toBeGreaterThan(before);
}

async function boot(page, handler) {
  await page.clock.install();
  await page.reload();
  await expect(page.locator('#status .now')).toHaveText('Late Train');
  await expect.poll(() => count(handler, '/api/queue')).toBeGreaterThan(0);
}

const shuffleBtn = (page) => page.locator('#btnShuffle');
const repeatBtn = (page) => page.locator('#btnRepeat');
const repeatOne = (page) => page.locator('#btnRepeatOne');

test('a mode set elsewhere shows on the next status tick', async ({ phone }) => {
  const { page, handler } = phone;
  await boot(page, handler);
  await expect(shuffleBtn(page)).toHaveAttribute('aria-pressed', 'false');
  await expect(repeatBtn(page)).toHaveAttribute('aria-pressed', 'false');
  await expect(repeatOne(page)).toBeHidden();

  // The desktop app turns on shuffle and repeat-one.
  handler.options.overrides = { ...OVERRIDES, '/api/queue': queue(true, 'one') };
  await queueTick(page, handler);
  await expect(shuffleBtn(page)).toHaveAttribute('aria-pressed', 'true');
  await expect(repeatBtn(page)).toHaveAttribute('aria-pressed', 'true');
  await expect(repeatOne(page)).toBeVisible();

  // Then shuffle off and repeat all.
  handler.options.overrides = { ...OVERRIDES, '/api/queue': queue(false, 'all') };
  await queueTick(page, handler);
  await expect(shuffleBtn(page)).toHaveAttribute('aria-pressed', 'false');
  await expect(repeatBtn(page)).toHaveAttribute('aria-pressed', 'true');
  await expect(repeatOne(page)).toBeHidden();
});

test('a tap here is not undone by a tick before the speaker has answered it', async ({ phone }) => {
  const { page, handler } = phone;
  await boot(page, handler);

  // The speaker takes a while to answer the tap.
  let release;
  const held = new Promise((r) => { release = r; });
  const sent = [];
  await page.route('**/api/queue/shuffle', async (route) => {
    sent.push(JSON.parse(route.request().postData() || '{}'));
    await held;
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(queue(true, 'off').body) });
  });

  await shuffleBtn(page).click();
  await expect(shuffleBtn(page)).toHaveAttribute('aria-pressed', 'true');
  await expect.poll(() => sent).toEqual([{ on: true }]);

  // The queue still reports the old mode: the button stays as tapped.
  await queueTick(page, handler);
  await expect(shuffleBtn(page)).toHaveAttribute('aria-pressed', 'true');

  // The speaker answers and reports the new mode from then on.
  release();
  handler.options.overrides = { ...OVERRIDES, '/api/queue': queue(true, 'off') };
  await queueTick(page, handler);
  await expect(shuffleBtn(page)).toHaveAttribute('aria-pressed', 'true');
});

test.describe('on the radio', () => {
  test.use({ phoneOverrides: { '/api/queue/mode': { body: { shuffle: true, repeat: 'all', stored: true } } } });

  test('the buttons show the stored mode and the queue is not asked', async ({ phone }) => {
    const { page, handler } = phone;
    await expect(shuffleBtn(page)).toHaveAttribute('aria-pressed', 'true');
    await expect(repeatBtn(page)).toHaveAttribute('aria-pressed', 'true');
    await expect(repeatOne(page)).toBeHidden();
    expect(count(handler, '/api/queue')).toBe(0);
  });
});

// phone-selfupdate-long-silence: a single-speaker update from the phone
// remote (internal/webui/assets/index.html) while the router is off for well
// over an hour, so the phone loses the network together with the speaker
// (discussion #1339, issue 4). The speaker gives up its download after about
// eleven minutes and keeps its version. The card used to turn red ("not
// reachable") at fifteen minutes, flip back to "waiting" every time the phone
// came back to the page, and the Play screen said "not reachable" on top.
// Now the card waits quietly, says after twelve minutes that the speaker has
// stopped the update by now, never turns red during the silence, keeps
// asking, and reads how the update ended once the network is back.
//
//   SHOOT_DIR=<dir> also saves screenshots of the card's states there.

import { test, expect } from '../fixtures.mjs';

const SU = '/api/agent/self-update';
const MIN = 60 * 1000;

function suStatus(extra) {
  return { body: { state: 'downloading', phase: '', done: 10, total: 100, current: '1.0.21', latest: '1.1.1', available: true, engine: 'ok', ...extra } };
}

async function bootDone(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
}

async function shot(page, name) {
  if (!process.env.SHOOT_DIR) return;
  await page.locator('#suCard').scrollIntoViewIfNeeded();
  await page.locator('#suCard').screenshot({ path: `${process.env.SHOOT_DIR}/${name}.png` });
}

// The phone goes into a pocket and comes back out.
async function setHidden(page, hidden) {
  await page.evaluate((h) => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (h ? 'hidden' : 'visible') });
    document.dispatchEvent(new Event('visibilitychange'));
  }, hidden);
}

function calls(handler, path) { return handler.calls.filter((c) => new URL(c.url).pathname === path).length; }

// Moves the page clock five seconds, then waits for the status poll that tick
// fires to have asked the speaker.
async function statusTick(page, handler) {
  const before = calls(handler, '/api/status');
  await page.clock.fastForward(5000);
  await expect.poll(() => calls(handler, '/api/status')).toBeGreaterThan(before);
}

test.describe('single-speaker update, router off for over an hour', () => {
  test.use({ phoneOverrides: { [SU]: suStatus() } });

  test('waits quietly, then says the speaker has stopped, and reads the end once the network is back', async ({ phone }) => {
    const { page, handler } = phone;
    await page.clock.install();
    await page.reload();
    await bootDone(page);
    const T = await page.evaluate(() => ({
      noAnswer: window.T.sfNoAnswer, offline: window.T.offline, stopped: window.T.suNetStopped,
      dl: window.T.suDl, fail: window.T.suFail, netErr: window.T.sfNetErr,
    }));
    expect(T.stopped).toContain('{v}');
    const stopped = T.stopped.replace('{v}', '1.0.21');
    const msg = page.locator('#suMsg');
    await page.locator('#tabMore').click();
    await expect(msg).toHaveText(T.dl.replace('{p}', '10'));

    // The router goes off: the phone and the speaker lose the network
    // together, so the speaker's last word stays a plain "downloading".
    handler.options.overrides = { '/api/status': { abort: true }, [SU]: { abort: true } };
    await expect(msg).toHaveText(T.noAnswer);
    await expect(msg).toHaveClass(/\bbusy\b/);
    await expect(msg).not.toHaveClass(/\bbad\b/);
    await shot(page, 'phone-update-silence-1-waiting');

    // Ten minutes in: still waiting, nothing red, the Play screen quiet.
    await page.clock.fastForward(10 * MIN);
    await statusTick(page, handler);
    await expect(msg).toHaveText(T.noAnswer);
    await expect(msg).not.toHaveClass(/\bbad\b/);
    await expect(page.locator('#statusCard')).not.toHaveClass(/\boffline\b/);

    // Past twelve minutes, longer than the speaker's own retries: the card
    // says it has stopped the update and keeps its version. A plain line,
    // no red, no turning ring.
    await page.clock.fastForward(3 * MIN);
    await expect(msg).toHaveText(stopped);
    await expect(msg).not.toHaveClass(/\bbad\b/);
    await expect(msg).not.toHaveClass(/\bbusy\b/);
    await shot(page, 'phone-update-silence-2-stopped');

    // Thirty-three minutes, where the card used to turn red: the phone was in
    // a pocket and comes back. The line stays, nothing flips to "waiting".
    await setHidden(page, true);
    await page.clock.fastForward(20 * MIN);
    await setHidden(page, false);
    await expect(msg).toHaveText(stopped);
    await expect(msg).not.toHaveClass(/\bbad\b/);
    expect(await page.evaluate(() => window.suUpdateInFlight())).toBe(true);
    await statusTick(page, handler);
    await expect(page.locator('#statusCard')).not.toHaveClass(/\boffline\b/);
    await expect(page.locator('#status')).not.toContainText(T.offline);

    // Seventy-five minutes: still the same line, and the card still asks,
    // at the outage pace of every fifteen seconds.
    await page.clock.fastForward(42 * MIN);
    const asked = calls(handler, SU);
    await page.clock.fastForward(15_000);
    await expect.poll(() => calls(handler, SU)).toBeGreaterThan(asked);
    await expect(msg).toHaveText(stopped);
    await expect(msg).not.toHaveClass(/\bbad\b/);

    // Eighty-five minutes: the network is back. The speaker gave up long ago
    // and says so; that answer replaces the line.
    handler.options.overrides = { [SU]: { body: {
      state: 'failed', error: 'the speaker could not reach the internet: dial tcp: lookup github.com: no such host',
      current: '1.0.21', latest: '1.1.1', available: true, engine: 'ok',
    } } };
    await page.clock.fastForward(15_000);
    await expect(msg).toHaveText(T.fail.replace('{e}', T.netErr), { timeout: 10_000 });
    await expect(msg).toHaveClass(/\bbad\b/);
    await shot(page, 'phone-update-silence-3-failed-after');
  });

  test('a speaker that finished meanwhile is read as done once the network is back', async ({ phone }) => {
    const { page, handler } = phone;
    await page.clock.install();
    await page.reload();
    await bootDone(page);
    const T = await page.evaluate(() => ({ stopped: window.T.suNetStopped }));
    const msg = page.locator('#suMsg');
    await page.locator('#tabMore').click();
    handler.options.overrides = { '/api/status': { abort: true }, [SU]: { abort: true } };
    await page.clock.fastForward(30 * MIN);
    await expect(msg).toHaveText(T.stopped.replace('{v}', '1.0.21'));

    handler.options.overrides = { [SU]: { body: { state: 'done', current: '1.1.1', latest: '1.1.1', available: false, engine: 'ok' } } };
    await page.clock.fastForward(15_000);
    await expect(msg).not.toHaveText(T.stopped.replace('{v}', '1.0.21'), { timeout: 10_000 });
    await expect(page.locator('#suStatus')).toContainText('1.1.1');
  });
});

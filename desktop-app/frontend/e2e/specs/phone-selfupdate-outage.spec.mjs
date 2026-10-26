// phone-selfupdate-outage: a single-speaker update started from the phone
// remote (internal/webui/assets/index.html) while the network drops out
// (discussion #1339). The update card keeps the word while it waits, the
// page's general "not reachable" state stands down meanwhile, the card asks
// again on its own once the speaker answers after the card gave up, and the
// speaker's own "no internet" phase does not promise a resume the speaker
// alone does not make.

import { test, expect } from '../fixtures.mjs';

const SU = '/api/agent/self-update';

function suStatus(extra) {
  return { body: { state: 'downloading', phase: '', done: 10, total: 100, current: '1.0.21', latest: '1.1.1', available: true, engine: 'ok', ...extra } };
}

async function bootDone(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
}

// The page's own string table, read in the page.
function strings(page) {
  return page.evaluate(() => ({
    noAnswer: window.T.sfNoAnswer, offline: window.T.offline, dl: window.T.suDl,
    netWait: window.T.sfNetWait, netWaitOne: window.T.sfNetWaitOne,
  }));
}

function pct(s, p) { return String(s).replace(/\{\w\}/g, p); }

// Moves the page clock five seconds, then waits for the status poll that tick
// fires to have asked the speaker.
async function statusTick(page, handler) {
  const before = handler.calls.filter((c) => new URL(c.url).pathname === '/api/status').length;
  await page.clock.fastForward(5000);
  await expect.poll(() => handler.calls.filter((c) => new URL(c.url).pathname === '/api/status').length).toBeGreaterThan(before);
}

test.describe('single-speaker update, speaker without internet', () => {
  test.use({ phoneOverrides: { [SU]: suStatus({ phase: 'network' }) } });

  test('says the speaker keeps trying, not that it carries on by itself', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    const T = await strings(page);
    expect(T.netWaitOne).toBeTruthy();
    expect(T.netWaitOne).not.toEqual(T.netWait);
    await page.locator('#tabMore').click();
    await expect(page.locator('#suMsg')).toHaveText(T.netWaitOne);
  });
});

test.describe('single-speaker update through a router outage', () => {
  test.use({ phoneOverrides: { [SU]: suStatus() } });

  test('the card keeps the word, then asks again once the speaker is back', async ({ phone }) => {
    const { page, handler } = phone;
    await page.clock.install();
    await page.reload();
    await bootDone(page);
    const T = await strings(page);
    await page.locator('#tabMore').click();
    await expect(page.locator('#suMsg')).toHaveText(pct(T.dl, '10'));

    // The router goes off: neither the status nor the update answers.
    handler.options.overrides = { '/api/status': { abort: true }, [SU]: { abort: true } };
    await expect(page.locator('#suMsg')).toHaveText(T.noAnswer);
    for (let i = 0; i < 3; i++) await statusTick(page, handler);
    expect(await page.evaluate(() => window.suUpdateInFlight())).toBe(true);
    await expect(page.locator('#statusCard')).not.toHaveClass(/\boffline\b/);

    // Fifteen silent minutes: the card stops asking and says so, and the
    // general state may say it too.
    await page.clock.fastForward(16 * 60 * 1000);
    await expect(page.locator('#suMsg')).toHaveText(T.offline);
    await statusTick(page, handler);
    await expect(page.locator('#statusCard')).toHaveClass(/\boffline\b/);

    // The router is back, the phone stayed on the page: the card asks again.
    handler.options.overrides = { [SU]: suStatus({ done: 50 }) };
    await statusTick(page, handler);
    await expect(page.locator('#suMsg')).toHaveText(pct(T.dl, '50'), { timeout: 10_000 });
    await expect(page.locator('#statusCard')).not.toHaveClass(/\boffline\b/);
  });
});

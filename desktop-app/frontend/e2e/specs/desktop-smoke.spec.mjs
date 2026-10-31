// desktop-smoke: the classic (non UI 2.0) desktop layout boots on the mock,
// every tab opens its view, the Music tab shows the presets and what plays,
// a preset click and a volume drag reach the right binding, and the Music
// tab degrades readably when the presets cannot be read or no speaker exists.
// Also the strict drift guard: every binding the tabs call is modelled, or
// listed on purpose in baselines/unmocked-desktop.json.
//
// Runs in the desktop project, and in webkit-smoke with E2E_WEBKIT=1.

import { test, expect } from '../fixtures.mjs';
import { expectDriftList, onHost } from '../helpers/mockclient.mjs';
import { settle } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { PRESETS } from '../../testkit/demo-data.mjs';

// Every classic tab, with an element only that view renders once it loaded.
const TABS = [
  ['box', '#statusMain'],
  ['library', '#libServerSelect'],
  ['recent', '#recentList'],
  ['spotify', '#spotifySyncBtn'],
  ['settings', '#settingsBody'],
  ['multiroom', '#zoneCreate'],
  ['setup', '#setupTargetBody'],
  ['wishes', '#wishesSuggestBtn'],
];

test.use({ ui2: false });

async function visit(page, view, marker) {
  await page.locator(`.tab-btn[data-view="${view}"]`).click();
  await expect(page.locator(`.tab-btn[data-view="${view}"]`)).toHaveClass(/\bactive\b/);
  const v = page.locator(`#view-${view}`);
  await expect(v).toBeVisible();
  await expect(v.locator(marker).first()).toBeAttached();
  // Every other view is hidden. "Not visible" alone passes for a view that is
  // still an empty, zero-size div (Wishes until first opened), so the class
  // the app actually toggles is checked too. All seven views are read in one
  // page round trip: fourteen separate assertions per visit made each of the
  // two tab tours ~11 s long, and on a busy machine they ran into the 30 s
  // test timeout.
  const others = TABS.map(([v]) => v).filter((v) => v !== view);
  await expect.poll(() => page.evaluate((ids) => ids.filter((id) => {
    const el = document.getElementById(`view-${id}`);
    return !el || !el.classList.contains('hidden') || el.checkVisibility();
  }), others), { message: `views not hidden while ${view} is open` }).toEqual([]);
}

test.describe('classic layout, default scenario', () => {
  test('boots without page errors, classic layout, lang en', async ({ app, mock }) => {
    const { page } = app;
    await expect(page.locator('html')).not.toHaveClass(/\bui2\b/);
    await expect(page.locator('html')).toHaveAttribute('lang', 'en');
    await expect(page.locator('#ui2Shell')).toHaveCount(0);
    await expect(page.locator('.tab-btn[data-view="box"]')).toHaveClass(/\bactive\b/);
    expect(await mock.pageErrors()).toEqual([]);
  });

  test('every tab shows its view with content', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('.tab-btn[data-view]')).toHaveCount(TABS.length);
    for (const [view, marker] of TABS) await visit(page, view, marker);
    // Back to the first tab, so the last view gets checked once it has content.
    await visit(page, TABS[0][0], TABS[0][1]);
  });

  test('six preset tiles in order and the playing station in the status bar', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#presets .preset .preset-body .name')).toHaveText(PRESETS.map((p) => p.name));
    await expect(page.locator('#statusMain')).toContainText('Best Of Rock.FM');
  });

  test('a preset click plays that slot on the current speaker, once', async ({ app, mock }) => {
    const { page } = app;
    await expect(page.locator('#presets .preset-body')).toHaveCount(6);
    await mock.resetCalls();
    await page.locator('#presets .preset-body').first().click();
    await mock.expectCalled('PlaySlot', { times: 1 });
    // Nothing else trails in after the first call settles.
    await expect(page.locator('#statusMain')).toContainText(PRESETS[0].name);
    expect(await mock.count('PlaySlot')).toBe(1);
    expect((await mock.last('PlaySlot')).args).toEqual(['demobox.local', 8888, 1]);
  });
});

// #1526: a speaker woken for a pair or group is on, on its last station, and
// stopped. It read as "paused on that key": pause bars, green text, no word,
// the key lit. It reads "stopped" now, and no key is lit.
test.describe('a stopped speaker on a selected station', () => {
  test.use({ scenario: { playing: { livingRoom: 'STOP_STATE' } } });

  test('says stopped with a stop glyph and lights no key', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#presets .preset-body')).toHaveCount(6);
    const line = page.locator('#statusMain');
    await expect(line).toContainText('Best Of Rock.FM');
    await expect(line.locator('small').first()).toHaveText('stopped');
    await expect(line.locator('.now')).toContainText('⏹');
    await expect(line.locator('.now')).not.toContainText('⏸');
    await expect(page.locator('#presets .preset.playing')).toHaveCount(0);
  });
});

test.describe('a playing speaker on the same station', () => {
  test('still lights its key', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#presets .preset.playing')).toHaveCount(1);
    await expect(page.locator('#statusMain small').first()).toHaveText('playing');
  });
});

// #1644: the power button at the end of the input row was a STANDBY input, lit
// green while the speaker slept and unable to wake it. It is the phone remote's
// power toggle now: lit while the speaker is on, a click switches it.
test.describe('the power toggle on a sleeping speaker', () => {
  test.use({ scenario: { playing: { livingRoom: 'STANDBY' } } });

  test('is plain, reads Power, and a click wakes the speaker', async ({ app, mock }) => {
    const { page } = app;
    const power = page.locator('#powerBtn');
    await expect(power).toBeVisible();
    await expect(power).toHaveAttribute('title', en('controls.powerTitle'));
    await expect(power).toHaveAttribute('aria-pressed', 'false');
    await expect(power).not.toHaveClass(/\bon\b/);
    await expect(power).not.toHaveClass(/\bactive\b/);
    await mock.resetCalls();
    await power.click();
    await mock.expectCalled('SetBoxPower', { times: 1 });
    const call = await mock.last('SetBoxPower');
    expect(call.args).toEqual(['demobox.local', 8888, true]);
    await expect(power).toHaveClass(/\bon\b/);
    await expect(power).toHaveAttribute('aria-pressed', 'true');
    await mock.expectCalled('SelectBoxSource', { times: 0 });
  });
});

test.describe('the power toggle on a playing speaker', () => {
  test('is lit, and a click sends the speaker to standby', async ({ app, mock }) => {
    const { page } = app;
    const power = page.locator('#powerBtn');
    await expect(power).toHaveClass(/\bon\b/);
    await mock.resetCalls();
    await power.click();
    await mock.expectCalled('SetBoxPower', { times: 1 });
    expect((await mock.last('SetBoxPower')).args[2]).toBe(false);
    await expect(power).not.toHaveClass(/\bon\b/);
  });
});

test.describe('volume, fake clock', () => {
  test.use({ fakeClock: true, scenario: { bindings: { SetBoxVolume: { value: null, delayMs: 50 } } } });

  test('a slider move sends the level to the current speaker, throttled', async ({ app, mock }) => {
    const { page } = app;
    await expect(page.locator('#musicVolume')).toBeVisible();
    await settle(page, 9_000);
    await mock.resetCalls();
    await page.locator('#musicVolume').fill('40');
    await page.clock.runFor(1_000);
    const calls = await mock.calls('SetBoxVolume');
    expect(calls.length).toBeGreaterThan(0);
    expect(calls.length).toBeLessThanOrEqual(2);
    const last = calls[calls.length - 1];
    expect(last.args[0]).toBe('demobox.local');
    expect(last.args[2]).toBe(40);
    expect(calls.every(onHost('demobox.local'))).toBe(true);
    expect(await mock.maxOpen('SetBoxVolume')).toBeLessThanOrEqual(1);
    await expect(page.locator('#musicVolumeVal')).toHaveText('40');
  });
});

test.describe('strict drift guard', () => {
  test.use({ scenario: { strict: true } });

  test('every binding the tabs call is modelled or listed', async ({ app, mock }) => {
    const { page } = app;
    // Leave Music first so its own markers count as a fresh visit at the end.
    for (const [view, marker] of [...TABS.slice(1), TABS[0]]) await visit(page, view, marker);
    // The Music view's start-up reads have landed again.
    await expect(page.locator('#presets .preset-body')).toHaveCount(6);
    expectDriftList(await mock.unmocked(), 'baselines/unmocked-desktop.json');
  });
});

test.describe('presets cannot be read', () => {
  test.use({ ready: '#view-box', scenario: { bindings: { GetPresets: { error: 'boom' } } } });

  test('the preset grid says the speaker is not reachable', async ({ app, mock }) => {
    const { page } = app;
    // The first failure retries once after 1.5 s, then shows the hint.
    await expect(page.locator('#presets')).toContainText(en('preset.speakerUnreachable'));
    await expect(page.locator('#presets .preset-body')).toHaveCount(0);
    expect(await mock.count('GetPresets')).toBeGreaterThanOrEqual(2);
  });
});

test.describe('no speaker on the network', () => {
  test.use({ ready: '#view-box', scenario: { boxes: [], seed: { cachedBoxes: false } } });

  test('the Music tab shows the empty state and searched', async ({ app, mock }) => {
    const { page } = app;
    const empty = page.locator('#boxSelect .empty-state');
    await expect(empty).toBeVisible();
    await expect(empty.locator('.empty-state-title')).toHaveText(en('speaker.emptyTitle'));
    await expect(page.locator('#emptyRetry')).toHaveText(en('speaker.retry'));
    await expect(page.locator('#boxControls')).toBeHidden();
    await mock.expectCalled('DiscoverBoxes');
  });
});

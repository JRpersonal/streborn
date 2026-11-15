// The UI 2.0 join review (src/shell2/speakers.js, playclaim.js joinReview):
// "+" in the sidebar on a speaker that plays something else first says, in
// that speaker's own row, what it stops and what it plays instead ("Bedroom
// stops Jazz24 and plays Best Of Rock.FM."), with Add and Cancel. Joining a
// group ends the speaker's own music, and before this the "+" was simply not
// offered on a speaker that played. An idle or standby speaker still joins at
// once. Inline in the row, never a toast or a dialog across the window.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-ui2-join-review.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { ROSTER, statusXml } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const DR = ROSTER.dining;

// Bedroom's own /now_playing while it plays a station of its own (the
// mock's model names every playing speaker "Best Of Rock.FM", the station
// Living Room plays, and the same station would need no review).
const BR_JAZZ = '<?xml version="1.0" encoding="UTF-8"?>'
  + `<nowPlaying deviceID="${BR.deviceID}" source="INTERNET_RADIO">`
  + '<ContentItem source="INTERNET_RADIO" location="http://radio.example/jazz24" isPresetable="true">'
  + '<itemName>Jazz24</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>';

const SENTENCE = 'Bedroom stops Jazz24 and plays Best Of Rock.FM.';

// The every-speaker poll runs every 15 s (main.js refreshEverySpeaker), so
// one poll round is a little more than that on the page clock.
const POLL_MS = 16_000;

const row = (page, host) => page.locator('#ui2SpkLists .ui2-spk-row')
  .filter({ has: page.locator(`.ui2-spk-pick[data-host="${host}"]`) });
const plus = (page, host) => row(page, host).locator('.ui2-spk-act[data-act="add"]');
const review = (page, host) => row(page, host).locator('.ui2-spk-join');

test.describe('UI 2.0 join review', () => {
  test.use({ ui2: true, fakeClock: true });

  test.describe('a speaker that plays something else', () => {
    test.use({
      scenario: {
        playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' },
        bindings: { Status: { byHost: { [BR.host]: { value: BR_JAZZ } } } },
      },
    });

    test('1. "+" opens the review in its row, and only Add joins it', async ({ app, mock }) => {
      const { page } = app;
      // "+" needs the first every-speaker poll (play state, names and zones).
      await settle(page, 10_000);
      const add = plus(page, BR.host);
      await expect(add).toBeVisible();
      await expect(add).toHaveAccessibleName('Play on Bedroom too');
      await expect(add).toHaveAttribute('aria-expanded', 'false');
      await mock.resetCalls();
      await add.click();
      const box = review(page, BR.host);
      await expect(box).toBeVisible();
      await expect(box).toHaveAttribute('role', 'group');
      await expect(box).toHaveAccessibleName(SENTENCE);
      await expect(box.locator('.ui2-spk-join-text')).toHaveText(SENTENCE);
      await expect(add).toHaveAttribute('aria-expanded', 'true');
      await expect(add).toHaveAttribute('aria-controls', await box.getAttribute('id'));
      // The keyboard lands on Add, which names the speaker.
      const confirm = box.locator('.ui2-spk-join-btn[data-join="add"]');
      await expect(confirm).toBeFocused();
      await expect(confirm).toHaveText('Add');
      await expect(confirm).toHaveAccessibleName('Add Bedroom');
      await expect(box.locator('.ui2-spk-join-btn[data-join="cancel"]')).toHaveAccessibleName('Cancel, Bedroom keeps playing');
      // Nothing was sent yet, and nothing spoke across the window.
      await runFor(page, 1_000);
      expect(await mock.count('FormZone')).toBe(0);
      await expect(page.locator('#toast')).not.toHaveClass(/\bshow\b/);
      // Only this row has a review.
      await expect(page.locator('#ui2SpkLists .ui2-spk-join')).toHaveCount(1);

      await confirm.click();
      await expect(review(page, BR.host)).toHaveCount(0);
      // Group edits coalesce for 600 ms (views/music/group.js).
      await runFor(page, 1_000);
      await mock.expectCalled('FormZone', { times: 1 });
      const call = await mock.last('FormZone');
      expect(call.args[0]).toBe(LR.host);
      expect(JSON.stringify(call.args)).toContain(BR.host);
      // The keyboard went back to the row, not to the top of the window.
      await expect(page.locator(`#ui2SpkLists [data-host="${BR.host}"]:focus`)).toHaveCount(1);
    });

    test('2. Cancel and Escape close the review and leave the speaker playing', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 10_000);
      await mock.resetCalls();
      const add = plus(page, BR.host);
      await add.click();
      await review(page, BR.host).locator('.ui2-spk-join-btn[data-join="cancel"]').click();
      await expect(review(page, BR.host)).toHaveCount(0);
      await expect(add).toBeFocused();
      await expect(add).toHaveAttribute('aria-expanded', 'false');
      // By keyboard: Enter opens it, Escape is Cancel.
      await page.keyboard.press('Enter');
      await expect(review(page, BR.host)).toBeVisible();
      await expect(review(page, BR.host).locator('.ui2-spk-join-btn[data-join="add"]')).toBeFocused();
      await page.keyboard.press('Tab');
      await expect(review(page, BR.host).locator('.ui2-spk-join-btn[data-join="cancel"]')).toBeFocused();
      await page.keyboard.press('Escape');
      await expect(review(page, BR.host)).toHaveCount(0);
      await expect(add).toBeFocused();
      // A second press on "+" closes an open review too.
      await add.click();
      await expect(review(page, BR.host)).toBeVisible();
      await add.click();
      await expect(review(page, BR.host)).toHaveCount(0);
      await runFor(page, 1_000);
      expect(await mock.count('FormZone')).toBe(0);
    });

    test('3. the whole review works from the keyboard alone', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 10_000);
      await mock.resetCalls();
      // Into the list, down to Bedroom, over its own Play/Pause (it plays,
      // so the row carries one between the name and "+") to its "+".
      await row(page, LR.host).locator('.ui2-spk-pick').focus();
      await page.keyboard.press('ArrowDown');
      await expect(row(page, BR.host).locator('.ui2-spk-pick')).toBeFocused();
      await page.keyboard.press('ArrowRight');
      await expect(row(page, BR.host).locator('.ui2-spk-tp')).toBeFocused();
      await page.keyboard.press('ArrowRight');
      await expect(plus(page, BR.host)).toBeFocused();
      await page.keyboard.press('Enter');
      await expect(review(page, BR.host).locator('.ui2-spk-join-btn[data-join="add"]')).toBeFocused();
      await page.keyboard.press('Enter');
      await runFor(page, 1_000);
      await mock.expectCalled('FormZone', { times: 1 });
    });

    test('5. the review closes by itself once the speaker stops, and "+" then joins at once', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 10_000);
      const add = plus(page, BR.host);
      await add.click();
      await expect(review(page, BR.host).locator('.ui2-spk-join-btn[data-join="add"]')).toBeFocused();
      // Bedroom goes to standby while its review is open.
      const standby = { byHost: { [BR.host]: { value: statusXml({ deviceID: BR.deviceID, playState: 'STANDBY' }) } } };
      await mock.set('Status', standby);
      await runFor(page, POLL_MS);
      await expect(review(page, BR.host)).toHaveCount(0);
      await expect(page.locator('#ui2SpkLists .ui2-spk-join')).toHaveCount(0);
      // The keyboard falls back to the row's "+", which is a plain button again.
      await expect(add).toBeFocused();
      await expect(add).not.toHaveAttribute('aria-expanded', /.*/);
      // The review was closed, not hidden: when Bedroom plays again it stays
      // shut until "+" is pressed.
      await mock.set('Status', { byHost: { [BR.host]: { value: BR_JAZZ } } });
      await runFor(page, POLL_MS);
      await expect(add).toHaveAttribute('aria-expanded', 'false');
      await expect(page.locator('#ui2SpkLists .ui2-spk-join')).toHaveCount(0);
      await mock.set('Status', standby);
      await runFor(page, POLL_MS);
      await expect(add).not.toHaveAttribute('aria-expanded', /.*/);
      await mock.resetCalls();
      await add.click();
      await expect(page.locator('#ui2SpkLists .ui2-spk-join')).toHaveCount(0);
      await runFor(page, 1_000);
      await mock.expectCalled('FormZone', { times: 1 });
      expect(JSON.stringify((await mock.last('FormZone')).args)).toContain(BR.host);
    });

    test('6. a repaint while the review is open keeps the keyboard on Cancel', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 10_000);
      await mock.resetCalls();
      await plus(page, BR.host).click();
      const cancel = review(page, BR.host).locator('.ui2-spk-join-btn[data-join="cancel"]');
      await cancel.focus();
      // Living Room moves on to another station: the review's sentence is
      // drawn again from the next poll, with the review still open.
      await mock.set('Status', {
        byHost: {
          [BR.host]: { value: BR_JAZZ },
          [LR.host]: { value: statusXml({ deviceID: LR.deviceID, itemName: 'Radio Paradise', location: 'http://radio.example/paradise' }) },
        },
      });
      await runFor(page, POLL_MS);
      await expect(review(page, BR.host).locator('.ui2-spk-join-text')).toHaveText('Bedroom stops Jazz24 and plays Radio Paradise.');
      await expect(review(page, BR.host).locator('.ui2-spk-join-btn[data-join="cancel"]')).toBeFocused();
      expect(await mock.count('FormZone')).toBe(0);
    });
  });

  test.describe('two speakers that play something else', () => {
    // Dining room plays a station of its own too.
    const DR_SOUL = '<?xml version="1.0" encoding="UTF-8"?>'
      + `<nowPlaying deviceID="${DR.deviceID}" source="INTERNET_RADIO">`
      + '<ContentItem source="INTERNET_RADIO" location="http://radio.example/soul" isPresetable="true">'
      + '<itemName>Soul Radio</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>';
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'dining', 'office', 'kitchen'],
        playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE', dining: 'PLAY_STATE' },
        bindings: { Status: { byHost: { [BR.host]: { value: BR_JAZZ }, [DR.host]: { value: DR_SOUL } } } },
      },
    });

    test('7. opening a second review closes the first, one review at a time', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 10_000);
      await mock.resetCalls();
      await plus(page, BR.host).click();
      await expect(review(page, BR.host)).toBeVisible();
      await plus(page, DR.host).click();
      await expect(page.locator('#ui2SpkLists .ui2-spk-join')).toHaveCount(1);
      await expect(review(page, BR.host)).toHaveCount(0);
      await expect(review(page, DR.host).locator('.ui2-spk-join-text')).toHaveText('Dining room stops Soul Radio and plays Best Of Rock.FM.');
      await expect(review(page, DR.host).locator('.ui2-spk-join-btn[data-join="add"]')).toBeFocused();
      await expect(plus(page, BR.host)).toHaveAttribute('aria-expanded', 'false');
      await expect(plus(page, DR.host)).toHaveAttribute('aria-expanded', 'true');
      await runFor(page, 1_000);
      expect(await mock.count('FormZone')).toBe(0);
    });
  });

  test('4. a speaker in standby joins at once, as before', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 10_000);
    const add = plus(page, BR.host);
    await expect(add).toBeVisible();
    await expect(add).not.toHaveAttribute('aria-expanded', /.*/);
    await mock.resetCalls();
    await add.click();
    await expect(page.locator('#ui2SpkLists .ui2-spk-join')).toHaveCount(0);
    await runFor(page, 1_000);
    await mock.expectCalled('FormZone', { times: 1 });
  });
});

// The UI 2.0 preview's Library (#1286, phase 2 of docs/UI-2.0.md): one level
// per step (music servers, a server's root, each folder down to the tracks),
// each level replacing the one before it under a trail of crumbs and Back; the
// speaker that plays named on every play and key action; feedback that says
// "playing" only once the speaker does; errors, empty and loading states in
// the place they belong. The classic Library is checked to be untouched.
//
// The mock walks Music > Evening Sessions (three tracks) and Music > Empty
// Shelf (testkit/demo-data.mjs LIBRARY_TREE).
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-library.spec.mjs

import AxeBuilder from '@axe-core/playwright';
import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { KILL_ANIM, WCAG_TAGS } from '../helpers/a11y.mjs';
import { minSize, noHorizontalOverflow } from '../helpers/layout.mjs';
import { en } from '../helpers/i18n.mjs';
import { LIBRARY_TREE, MEDIA_SERVERS, ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const NAS = MEDIA_SERVERS[0].friendlyName;
const [OPENING, BLUE, LATE] = LIBRARY_TREE.c1a.items;

const lib = (page) => page.locator('#ui2MainInner > #view-library');
const heading = (page) => lib(page).locator('#lib2LevelTitle');
const crumbs = (page) => lib(page).locator('.lib2-crumbs > li');
const folder = (page, name) => lib(page).locator('.lib2-folder', { hasText: name });
const track = (page, it) => lib(page).locator('.lib2-track', { hasText: it.title });
const rowStatus = (page, it) => track(page, it).locator('.lib2-status');

async function openLibrary(page) {
  await page.locator('#ui2Sidebar .ui2-nav-item[data-view="library"]').click();
  await expect(heading(page)).toHaveText(NAS);
}

async function walkToTracks(page) {
  await openLibrary(page);
  await folder(page, 'Music').click();
  await expect(heading(page)).toHaveText('Music');
  await folder(page, 'Evening Sessions').click();
  await expect(heading(page)).toHaveText('Evening Sessions');
}

test.describe('UI 2.0 Library', () => {
  test.use({ ui2: true, fakeClock: true });

  test.beforeEach(async ({ app }) => {
    await settle(app.page, 2_000);
  });

  test('1. one level per step: each level replaces the one before, under the trail', async ({ app, mock }) => {
    const { page } = app;
    await openLibrary(page);
    await expect(crumbs(page)).toHaveText([en('ui2.library.servers'), NAS]);
    await expect(lib(page).locator('.lib2-back')).toHaveAccessibleName(en('ui2.library.backTo', { name: en('ui2.library.servers') }));
    await expect(lib(page).locator('.lib2-folder')).toHaveCount(6);

    await folder(page, 'Music').click();
    await expect(heading(page)).toHaveText('Music');
    await expect(heading(page)).toBeFocused();
    await expect(crumbs(page)).toHaveText([en('ui2.library.servers'), NAS, 'Music']);
    // The root's folders are gone, not stacked above.
    await expect(folder(page, 'Videos')).toHaveCount(0);
    await expect(lib(page).locator('.lib2-folder')).toHaveCount(2);

    await folder(page, 'Evening Sessions').click();
    await expect(lib(page).locator('.lib2-track')).toHaveCount(3);
    await expect(lib(page).locator('.lib2-level-sub')).toHaveText(en('ui2.library.countTracks', { n: 3 }));
    expect((await mock.last('BrowseLibrary')).args[1]).toBe('c1a');

    // Back is one step up, and the keyboard lands on the folder it came from.
    await lib(page).locator('.lib2-back').click();
    await expect(heading(page)).toHaveText('Music');
    await expect(folder(page, 'Evening Sessions')).toBeFocused();

    // The first crumb is the list of music servers.
    await lib(page).locator('.lib2-crumb', { hasText: en('ui2.library.servers') }).click();
    await expect(heading(page)).toHaveText(en('ui2.library.serversTitle'));
    await expect(lib(page).locator('.lib2-back')).toHaveCount(0);
    const server = lib(page).locator('.lib2-server');
    await expect(server).toHaveCount(1);
    await expect(server).toBeFocused();
    // Choosing it again is a step down to its root.
    await server.click();
    await expect(heading(page)).toHaveText(NAS);
    expect((await mock.last('BrowseLibrary')).args[1]).toBe('0');
  });

  test('2. every crumb is reachable by keyboard and Enter goes there', async ({ app }) => {
    const { page } = app;
    await walkToTracks(page);
    await lib(page).locator('.lib2-back').focus();
    await page.keyboard.press('Tab');
    await expect(lib(page).locator('.lib2-crumb').nth(0)).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(lib(page).locator('.lib2-crumb').nth(1)).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(lib(page).locator('.lib2-crumb').nth(2)).toHaveText('Music');
    await expect(lib(page).locator('.lib2-crumb').nth(2)).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(heading(page)).toHaveText('Music');
    await expect(folder(page, 'Evening Sessions')).toBeFocused();
    // The current level is not a link.
    await expect(lib(page).locator('.lib2-crumb--current')).toHaveAttribute('aria-current', 'location');
  });

  test('3. a track names the speaker and says playing only once the speaker plays', async ({ app, mock }) => {
    const { page } = app;
    await walkToTracks(page);
    await expect(lib(page).locator('.lib2-target')).toContainText(LR.friendlyName);
    const play = track(page, BLUE).locator('.lib2-play');
    await expect(play).toHaveAccessibleName(en('ui2.library.playTrackOn', { title: BLUE.title, name: LR.friendlyName }));
    // The preview's one "Put on key" control: its words, then what and where.
    await expect(track(page, BLUE).locator('.lib2-key')).toHaveAccessibleName(en('ui2.radio.putOnKeyName', { name: BLUE.title, speaker: LR.friendlyName }));
    await expect(track(page, BLUE).locator('.lib2-key')).toContainText(en('ui2.radio.putOnKey'));
    // The count after the folder filter says what it counts.
    const n = await lib(page).locator('.lib2-track').count();
    await expect(lib(page).locator('#libCount')).toHaveText(`${en('ui2.library.entries')} ${n}`);
    // Shuffle and repeat wrap together, never one of them alone.
    await expect(lib(page).locator('.lib2-folder-modes .lib2-shuffle')).toHaveCount(1);
    await expect(lib(page).locator('.lib2-folder-modes .lib2-repeat')).toHaveCount(1);

    await mock.resetCalls();
    await play.click();
    await mock.expectCalled('PlayURL', { times: 1, pred: (c) => c.args[0] === LR.host && c.args[2] === BLUE.streamURL });
    await expect(rowStatus(page, BLUE)).toHaveText(en('ui2.library.starting', { name: LR.friendlyName }));
    // The speaker is asked two seconds later and reports PLAY_STATE.
    await runFor(page, 2_500);
    await expect(rowStatus(page, BLUE)).toHaveText(en('ui2.library.playingOn', { name: LR.friendlyName }));
    await expect(rowStatus(page, BLUE)).toHaveClass(/lib2-status--ok/);
    // Only this row says so.
    await expect(rowStatus(page, LATE)).toHaveText('');
  });

  test('4. a speaker chosen in the sidebar is named at once and gets the folder', async ({ app, mock }) => {
    const { page } = app;
    await walkToTracks(page);
    await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BR.host}"]`).click();
    const playFolder = lib(page).locator('.lib2-play-folder');
    await expect(playFolder).toHaveText(en('ui2.library.playFolderOn', { name: BR.friendlyName }));
    await expect(lib(page).locator('.lib2-key-folder')).toHaveText(en('ui2.library.keyFolderOn', { name: BR.friendlyName }));
    await expect(heading(page)).toHaveText('Evening Sessions');

    await mock.resetCalls();
    await playFolder.click();
    await mock.expectCalled('StartQueue', { times: 1, pred: onHost(BR.host) });
    const payload = JSON.parse((await mock.last('StartQueue')).args[2]);
    expect(payload.items.map((i) => i.url)).toEqual([OPENING.streamURL, BLUE.streamURL, LATE.streamURL]);
    const status = lib(page).locator('.lib2-folder-acts .lib2-status');
    await expect(status).toHaveText(en('ui2.library.folderStarting', { name: BR.friendlyName }));
    await runFor(page, 2_500);
    await expect(status).toHaveText(en('ui2.library.folderPlaying', { name: BR.friendlyName }));
  });

  test.describe('a play the speaker refuses', () => {
    test.use({ scenario: { bindings: { PlayURL: { error: 'connection refused' } } } });

    test('5. reports in its own row, not in a window', async ({ app }) => {
      const { page } = app;
      await walkToTracks(page);
      await track(page, OPENING).locator('.lib2-play').click();
      await expect(rowStatus(page, OPENING)).toHaveText(en('ui2.library.playFailed', { name: LR.friendlyName, detail: 'connection refused' }));
      await expect(rowStatus(page, OPENING)).toHaveClass(/lib2-status--error/);
      await expect(page.locator('#errorModal')).toBeHidden();
    });
  });

  test.describe('a server that does not answer', () => {
    test.use({ scenario: { bindings: { BrowseLibrary: { error: 'BrowseLibrary: context deadline exceeded', times: 1 } } } });

    test('6. says so in place, with Try again and the way back', async ({ app, mock }) => {
      const { page } = app;
      await page.locator('#ui2Sidebar .ui2-nav-item[data-view="library"]').click();
      const state = lib(page).locator('.lib2-state--error');
      await expect(state).toHaveAttribute('role', 'alert');
      await expect(state.locator('.lib2-state-title')).toHaveText(en('ui2.library.asleepTitle', { name: NAS }));
      await expect(state).toContainText(en('ui2.library.asleepBody'));
      await expect(page.locator('#errorModal')).toBeHidden();
      await expect(lib(page).locator('#lib2Up')).toHaveText(en('ui2.library.backTo', { name: en('ui2.library.servers') }));
      await mock.resetCalls();
      await lib(page).locator('#lib2Retry').click();
      await expect(heading(page)).toHaveText(NAS);
      await expect(lib(page).locator('.lib2-folder')).toHaveCount(6);
      await mock.expectCalled('BrowseLibrary', { times: 1 });
    });
  });

  test.describe('no music server on the network', () => {
    test.use({ scenario: { bindings: { ListMediaServers: { value: [] } } } });

    test('7. explains what to do and offers the search and the address', async ({ app, mock }) => {
      const { page } = app;
      await page.locator('#ui2Sidebar .ui2-nav-item[data-view="library"]').click();
      await expect(lib(page).locator('.lib2-state-title')).toHaveText(en('ui2.library.noServersTitle'));
      await expect(lib(page).locator('.lib2-state')).toContainText(en('ui2.library.noServersBody'));
      await expect(lib(page).locator('.lib2-manual')).toHaveAttribute('open', '');
      await expect(lib(page).locator('#libManualUrl')).toHaveAccessibleName(en('ui2.library.addServerLabel'));
      await mock.resetCalls();
      await lib(page).locator('#libRefreshBtn').click();
      await mock.expectCalled('ListMediaServers', { times: 1 });
    });
  });

  test('8. a track and a folder go on a key of the named speaker', async ({ app, mock }) => {
    const { page } = app;
    await walkToTracks(page);
    await track(page, OPENING).locator('.lib2-key').click();
    await expect(page.locator('#pickModal')).toBeVisible();
    await expect(page.locator('#pickTitle')).toContainText(LR.friendlyName);
    await page.locator('#pickGrid .pick-slot').nth(0).click();
    // The track goes on the key with its object on the server (#919), as one
    // preset object.
    await mock.expectCalled('SaveFolderPreset', {
      times: 1,
      pred: (c) => c.args[0] === LR.host && c.args[2] === 1 && JSON.parse(c.args[3]).stream_url === OPENING.streamURL && !!JSON.parse(c.args[3]).track,
    });
    await expect(rowStatus(page, OPENING)).toHaveText(en('ui2.library.onKey', { n: 1, name: LR.friendlyName }));

    await lib(page).locator('.lib2-key-folder').click();
    await expect(page.locator('#pickTitle')).toHaveText(en('ui2.library.keyFolderTitle', { name: LR.friendlyName }));
    await page.locator('#pickGrid .pick-slot').nth(1).click();
    await mock.expectCalled('SaveFolderPreset', { times: 1, pred: (c) => c.args[0] === LR.host && c.args[2] === 2 });
    await expect(lib(page).locator('.lib2-folder-acts .lib2-status')).toHaveText(en('ui2.library.onKey', { n: 2, name: LR.friendlyName }));
  });

  test('9. the search across every server is one more step, and Back returns without a new browse', async ({ app, mock }) => {
    const { page } = app;
    await openLibrary(page);
    await mock.resetCalls();
    const q = lib(page).locator('#libWholeQ');
    await expect(q).toHaveAccessibleName(en('ui2.library.searchLabel'));
    await q.fill('blue');
    await q.press('Enter');
    await expect(heading(page)).toHaveText(en('ui2.library.results', { q: 'blue' }));
    await expect(crumbs(page)).toHaveText([en('ui2.library.servers'), NAS, en('ui2.library.results', { q: 'blue' })]);
    await expect(lib(page).locator('.lib2-track')).toHaveCount(2);
    await expect(track(page, BLUE).locator('.lib2-sub')).toContainText(NAS);
    await expect(track(page, BLUE).locator('.lib2-play')).toHaveAccessibleName(en('ui2.library.playTrackOn', { title: BLUE.title, name: LR.friendlyName }));
    await lib(page).locator('.lib2-back').click();
    await expect(heading(page)).toHaveText(NAS);
    await expect(lib(page).locator('.lib2-folder')).toHaveCount(6);
    expect(await mock.count('BrowseLibrary')).toBe(0);
  });

  test('10. the folder filter narrows the rows and keeps the field focused', async ({ app }) => {
    const { page } = app;
    await walkToTracks(page);
    const f = lib(page).locator('#libSearch');
    await expect(f).toHaveAccessibleName(en('ui2.library.filterLabel'));
    await f.fill('late');
    await expect(lib(page).locator('.lib2-track')).toHaveCount(1);
    await expect(track(page, LATE)).toBeVisible();
    await expect(f).toBeFocused();
    await f.fill('zzz');
    await expect(lib(page).locator('.lib2-none')).toHaveText(en('ui2.library.filterNone', { q: 'zzz' }));
  });

  test('11. shuffle says whether it is on, repeat says its mode, both go to the speaker', async ({ app }) => {
    const { page, api } = app;
    await walkToTracks(page);
    const shuffle = lib(page).locator('.lib2-shuffle');
    const before = await shuffle.getAttribute('aria-pressed');
    await shuffle.click();
    await expect(shuffle).toHaveAttribute('aria-pressed', before === 'true' ? 'false' : 'true');
    const repeat = lib(page).locator('.lib2-repeat');
    await expect(repeat).toHaveText(en('ui2.library.repeatOff'));
    await repeat.click();
    await expect(repeat).toHaveText(en('ui2.library.repeatAll'));
    await repeat.click();
    await expect(repeat).toHaveText(en('ui2.library.repeatOne'));
    await expect.poll(() => api.calls.filter((c) => c.method === 'POST' && /\/api\/queue\/(shuffle|repeat)$/.test(new URL(c.url).pathname)).length).toBe(3);
  });

  test('12. an empty folder says so and leads back up', async ({ app }) => {
    const { page } = app;
    await openLibrary(page);
    await folder(page, 'Music').click();
    await folder(page, 'Empty Shelf').click();
    await expect(lib(page).locator('.lib2-state-title')).toHaveText(en('library.emptyFolder'));
    await lib(page).locator('#lib2Up').click();
    await expect(heading(page)).toHaveText('Music');
    await expect(folder(page, 'Empty Shelf')).toBeFocused();
  });

  test('16. Search again on the server list refreshes it and stays there', async ({ app, mock }) => {
    const { page } = app;
    // Deep in a folder, so the server and the path are remembered.
    await walkToTracks(page);
    await lib(page).locator('.lib2-crumb', { hasText: en('ui2.library.servers') }).click();
    await expect(heading(page)).toHaveText(en('ui2.library.serversTitle'));
    await mock.resetCalls();
    await lib(page).locator('#libRefreshBtn').click();
    await mock.expectCalled('ListMediaServers', { times: 1 });
    await expect(heading(page)).toHaveText(en('ui2.library.serversTitle'));
    await expect(lib(page).locator('.lib2-server')).toHaveCount(1);
    expect(await mock.count('BrowseLibrary')).toBe(0);
  });

  test('17. a folder that fails to open says so in its own place, with the way back to its parent', async ({ app, mock }) => {
    const { page } = app;
    await openLibrary(page);
    await folder(page, 'Music').click();
    await expect(heading(page)).toHaveText('Music');
    await mock.set('BrowseLibrary', { error: 'UPnP error 701: no such object', times: 1 });
    await folder(page, 'Evening Sessions').click();
    const state = lib(page).locator('.lib2-state--error');
    await expect(state.locator('.lib2-state-title')).toHaveText(en('ui2.library.folderErrorTitle'));
    await expect(state).toContainText('no such object');
    // The trail stays on the folder that was asked for, not the server root.
    await expect(crumbs(page)).toHaveText([en('ui2.library.servers'), NAS, 'Music', 'Evening Sessions']);
    await expect(lib(page).locator('#lib2Up')).toHaveText(en('ui2.library.backTo', { name: 'Music' }));
    await expect(page.locator('#errorModal')).toBeHidden();
    await lib(page).locator('#lib2Retry').click();
    await expect(lib(page).locator('.lib2-track')).toHaveCount(3);
  });

  test('18. a folder play\'s answer is not written under another folder', async ({ app, mock }) => {
    const { page } = app;
    await walkToTracks(page);
    await lib(page).locator('.lib2-play-folder').click();
    const status = lib(page).locator('.lib2-folder-acts .lib2-status');
    await expect(status).toHaveText(en('ui2.library.folderStarting', { name: LR.friendlyName }));
    // Before the speaker answers, open another folder with tracks.
    await lib(page).locator('.lib2-back').click();
    await expect(heading(page)).toHaveText('Music');
    await mock.set('BrowseLibrary', { value: LIBRARY_TREE.c1a, times: 1 });
    await folder(page, 'Empty Shelf').click();
    await expect(heading(page)).toHaveText('Empty Shelf');
    await expect(lib(page).locator('.lib2-track')).toHaveCount(3);
    // The speaker reports PLAY_STATE two seconds after the start.
    await runFor(page, 2_500);
    await expect(status).toHaveText('');
  });

  for (const theme of ['dark', 'light', 'contrast']) {
    test.describe(`axe, ${theme}`, () => {
      test.use({ theme });

      test(`13. the tracks level with its feedback and the server list have no WCAG findings (${theme})`, async ({ app }) => {
        const { page } = app;
        await walkToTracks(page);
        await track(page, OPENING).locator('.lib2-play').click();
        await runFor(page, 2_500);
        await expect(rowStatus(page, OPENING)).toHaveText(en('ui2.library.playingOn', { name: LR.friendlyName }));
        await page.addStyleTag({ content: KILL_ANIM });
        // axe batches its rules with setTimeout, which the paused clock never
        // fires; nothing of this view is still waiting on a timer here.
        await page.clock.resume();
        const r1 = await new AxeBuilder({ page }).withTags(WCAG_TAGS).include('#view-library').analyze();
        expect(r1.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([]);
        await lib(page).locator('.lib2-crumb', { hasText: en('ui2.library.servers') }).click();
        await expect(lib(page).locator('.lib2-server')).toHaveCount(1);
        const r2 = await new AxeBuilder({ page }).withTags(WCAG_TAGS).include('#view-library').analyze();
        expect(r2.violations.map((v) => v.id)).toEqual([]);
      });
    });
  }
});

test.describe('UI 2.0 Library in the smallest window at Extra large', () => {
  test.use({ ui2: true, scale: '3', viewport: { width: 1000, height: 600 } });

  test('14. nothing scrolls sideways and every control keeps its 40px', async ({ app }) => {
    const { page } = app;
    await walkToTracks(page);
    await page.addStyleTag({ content: KILL_ANIM });
    await noHorizontalOverflow(page);
    const over = await page.locator('#ui2Main').evaluate((m) => m.scrollWidth - m.clientWidth);
    expect(over).toBeLessThanOrEqual(1);
    await minSize(lib(page).locator('button:visible, input:visible, select:visible'));
  });
});

test.describe('the classic Library', () => {
  test.use({ ui2: false });

  test('15. is untouched: its pickers, rows and crumbs, and none of the preview', async ({ app }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="library"]').click();
    const v = page.locator('#view-library');
    await expect(v.locator('#libServerSelect')).toBeVisible();
    await expect(v.locator('#libraryBoxSelect')).toBeVisible();
    await expect(v.locator('[class*="lib2-"]')).toHaveCount(0);
    await v.locator('.library-row-folder', { hasText: 'Music' }).click();
    await expect(v.locator('.library-crumb-active')).toHaveText('Music');
    await v.locator('.library-row-folder', { hasText: 'Evening Sessions' }).click();
    await expect(v.locator('.lib-play-folder-btn')).toBeVisible();
    await expect(v.locator('.library-row-track')).toHaveCount(3);
  });
});

// The key assignment editor on a speaker's Keys and alarms tab in the UI 2.0
// preview (shell2/speakers/keyeditor.js): the speaker's keys next to a source
// browser (Radio, Library, Spotify), so a station, a folder or a playlist goes
// onto a key in one place, and "Put what's playing on key N" for every source
// the agent keeps (POST /api/box/save-playing). Every outcome is said in the
// editor's own line, done only after the speaker's read-back; the agent's 409
// already-on-slot offers the move right there and a 422 reads in plain words,
// never in the error dialog. A hold on a Radio key opens this editor on that
// key (test 16). The last test checks the classic layout.
//
//   E2E_PORT=4185 npx playwright test specs/desktop-ui2-assignment-editor.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { runFor, settle } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { PRESETS, PRESETS_ROW2, ROSTER } from '../../testkit/demo-data.mjs';
import { mergeScenario, speakersScenario } from '../../testkit/scenario.mjs';

const LR = ROSTER.livingRoom;
const NAME = LR.friendlyName;
// Keys 5 and 6 empty, so the editor chooses key 5 on its own; the second row
// as the demo speaker keeps it.
const FOUR = [...PRESETS.filter((p) => p.slot <= 4), ...PRESETS_ROW2];
// The logo of the station the demo speaker plays: a key stored without one
// would be given one by the app's logo repair, a write of its own.
const ROCK_ART = PRESETS.find((p) => p.slot === 2).art;

const scenario = (patch = {}) => mergeScenario(speakersScenario(), mergeScenario({ presets: { livingRoom: FOUR } }, patch));
const nav = (page, view) => page.locator(`#ui2Tabs .ui2-nav-item[data-view="${view}"]`);
const editor = (page) => page.locator('#ui2KeyEditor');
const key = (page, slot) => editor(page).locator(`.kae-key[data-slot="${slot}"]`);
const result = (page) => page.locator('#kaeResult');
const L = (k, v) => en(`ui2.speakers.keyEditor.${k}`, v);

async function openKeys(page) {
  await nav(page, 'speakers').click();
  await page.locator(`#view-speakers .spl-open[data-host="${LR.host}"]`).click();
  await expect(page.locator('#ui2SpkPageName')).toHaveText(NAME);
  await page.locator('#ui2SpkTab-keys').click();
  await expect(editor(page).getByRole('heading', { name: L('title', { name: NAME }) })).toBeVisible();
}

// putOnSpeaker adds what the agent stored to the in-page model's store: the
// mock's own save-playing has no store of its own.
async function putOnSpeaker(page, slot, name) {
  await page.evaluate(([host, s, n, art]) => {
    const list = window.__strMock.state.presets[host];
    list.push({ slot: s, name: n, stream_url: 'http://example.com/rock', type: 'radio', art });
    list.sort((a, b) => a.slot - b.slot);
  }, [LR.host, slot, name, ROCK_ART]);
}

// playOnSpeaker makes the mock speaker play item, the way its now-playing
// reports it; "Put what's playing" reads it fresh before it saves.
async function playOnSpeaker(page, item) {
  await page.evaluate(([h, it]) => {
    window.__strMock.state.playing[h] = 'PLAY_STATE';
    window.__strMock.state.item[h] = it;
  }, [LR.host, item]);
}

const PLAYLIST = 'spotify:playlist:37i9dQZF1DXcBWIGoYBM5M';
// The speaker's own Spotify entry: source SPOTIFY, a container location that
// carries the playlist's URI.
const SPOTIFY_ITEM = { source: 'SPOTIFY', location: `/playback/container/${Buffer.from(PLAYLIST).toString('base64')}`, name: 'Discover Weekly', art: '' };
const CONFLICT_2 = 'status 409: {"code":"already-on-slot","slot":2,"name":"Best Of Rock.FM","error":"already on key 2"}';

test.describe('UI 2.0 key assignment editor', () => {
  test.use({ ui2: true, scenario: scenario() });

  test('1. lists the keys beside the sources, chooses an empty key, and is worked by keyboard', async ({ app }) => {
    const { page } = app;
    await openKeys(page);
    const keys = editor(page).getByRole('group', { name: L('keysLabel', { name: NAME }) });
    await expect(keys.locator('.kae-key')).toHaveCount(12);
    await expect(key(page, 2)).toContainText('Best Of Rock.FM');
    await expect(key(page, 5)).toContainText(L('keyEmpty'));
    // The first empty key is chosen, so nothing is replaced by accident.
    await expect(key(page, 5)).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('#kaeHint')).toHaveText(L('hintEmpty', { n: 5 }));
    // The second row, pressed twice.
    await expect(key(page, 7)).toContainText('Radio Swiss Jazz');
    await key(page, 3).focus();
    await page.keyboard.press('Enter');
    await expect(key(page, 3)).toHaveAttribute('aria-pressed', 'true');
    await expect(key(page, 5)).toHaveAttribute('aria-pressed', 'false');
    await expect(page.locator('#kaeHint')).toHaveText(L('hintReplace', { n: 3, current: 'NPR News' }));
    await expect(page.locator('#kaeSaveNow')).toHaveText(L('nowPut', { n: 3 }));
    // The source tabs: arrows move and open them.
    const radioTab = editor(page).getByRole('tab', { name: L('srcRadio') });
    await expect(radioTab).toHaveAttribute('aria-selected', 'true');
    await radioTab.focus();
    await page.keyboard.press('ArrowRight');
    const libTab = editor(page).getByRole('tab', { name: L('srcLibrary') });
    await expect(libTab).toBeFocused();
    await expect(libTab).toHaveAttribute('aria-selected', 'true');
    await expect(editor(page).getByRole('tabpanel')).toBeVisible();
  });

  test('2. a station from the search goes onto the chosen key, done after the read-back', async ({ app, mock }) => {
    const { page } = app;
    await openKeys(page);
    await expect(editor(page).locator('.kae-state')).toHaveText(L('radioNoFavs'));
    await editor(page).getByLabel(L('radioSearchLabel')).fill('kexp');
    await editor(page).getByRole('button', { name: L('search'), exact: true }).click();
    await expect(editor(page).getByRole('heading', { name: L('radioHits', { q: 'kexp' }) })).toBeVisible();
    await mock.resetCalls();
    await editor(page).getByRole('button', { name: L('putName', { what: 'KEXP', n: 5, name: NAME }) }).click();
    await mock.expectCalled('SetPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 5 && c.args[3] === 'KEXP' });
    await expect(result(page)).toHaveText(en('ui2.radio.saved', { n: 5, speaker: NAME }));
    await expect(result(page)).toHaveClass(/kae-result--ok/);
    await expect(key(page, 5)).toContainText('KEXP');
    await expect(page.locator('#errorModal')).toBeHidden();
  });

  test('3. a library folder goes onto a key, walked folder by folder', async ({ app, mock }) => {
    const { page } = app;
    await openKeys(page);
    await editor(page).getByRole('tab', { name: L('srcLibrary') }).click();
    await editor(page).getByRole('button', { name: 'Living Room NAS', exact: true }).click();
    await editor(page).getByRole('button', { name: 'Music', exact: true }).click();
    await editor(page).getByRole('button', { name: 'Evening Sessions', exact: true }).click();
    await expect(editor(page).getByRole('heading', { name: 'Evening Sessions' })).toBeVisible();
    await mock.resetCalls();
    await editor(page).getByRole('button', { name: L('putName', { what: 'Evening Sessions', n: 5, name: NAME }) }).click();
    await mock.expectCalled('SaveFolderPreset', {
      pred: (c) => onHost(LR.host)(c) && c.args[2] === 5 && JSON.parse(c.args[3]).items.length === 3
        && JSON.parse(c.args[3]).folder.container_id === 'c1a',
    });
    await expect(result(page)).toHaveText(en('ui2.radio.saved', { n: 5, speaker: NAME }));
    await expect(key(page, 5)).toContainText('Evening Sessions');
    // Back leads up again, and names where.
    await editor(page).getByRole('button', { name: en('ui2.library.backTo', { name: 'Music' }) }).click();
    await expect(editor(page).getByRole('button', { name: 'Evening Sessions', exact: true })).toBeVisible();
  });

  test('9. reads the keys again when the tab is opened again', async ({ app }) => {
    const { page } = app;
    await openKeys(page);
    await expect(key(page, 6)).toContainText(L('keyEmpty'));
    // Stored meanwhile somewhere else (the speaker's own hold, a phone).
    await putOnSpeaker(page, 6, 'Classic FM');
    await page.locator('#ui2SpkTab-sound').click();
    await page.locator('#ui2SpkTab-keys').click();
    await expect(key(page, 6)).toContainText('Classic FM');
  });

  // The hold on a Radio key (views/music/ui2keyedit.js): its "Find a station"
  // and "Choose a favorite" open this editor, on that key, instead of a
  // picker of their own.
  test('16. a hold on a Radio key opens the editor on that key, in the mode it asked for', async ({ app }) => {
    const { page } = app;
    const radioKey = (slot) => page.locator(`#presets .preset[data-slot="${slot}"]`);
    const holdChoice = (id) => page.locator(`#ui2KeyEdit [data-ke="${id}"]`);
    await openKeys(page);
    // On its own the editor chooses the first empty key.
    await expect(key(page, 5)).toHaveAttribute('aria-pressed', 'true');
    // Key 2's "Choose a favorite", from Radio.
    await nav(page, 'box').click();
    await radioKey(2).locator('.preset-body').focus();
    await page.keyboard.press('Shift+Enter');
    await holdChoice('favorites').click();
    await expect(page.locator('#ui2SpkTab-keys')).toHaveAttribute('aria-selected', 'true');
    await expect(key(page, 2)).toHaveAttribute('aria-pressed', 'true');
    await expect(key(page, 5)).toHaveAttribute('aria-pressed', 'false');
    await expect(page.locator('#kaeHint')).toHaveText(L('hintReplace', { n: 2, current: 'Best Of Rock.FM' }));
    // No favourites yet: the keyboard is on the Radio source, which says so.
    await expect(editor(page).locator('.kae-state')).toHaveText(L('radioNoFavs'));
    await expect(page.locator('#kaePanel')).toBeFocused();
    // Key 6's "Find a station", with the Library source chosen here meanwhile.
    await editor(page).getByRole('tab', { name: L('srcLibrary') }).click();
    await nav(page, 'box').click();
    await radioKey(6).click();
    await holdChoice('find').click();
    await expect(editor(page).getByRole('tab', { name: L('srcRadio') })).toHaveAttribute('aria-selected', 'true');
    await expect(key(page, 6)).toHaveAttribute('aria-pressed', 'true');
    await expect(editor(page).getByLabel(L('radioSearchLabel'))).toBeFocused();
    await expect(page.locator('#pickModal')).toBeHidden();
  });

  test.describe('Spotify', () => {
    test.use({
      scenario: scenario({
        bindings: {
          RecentPlayed: {
            value: [
              { ts: '2026-10-23T18:00:00Z', source: 'spotify', cardKey: 'spotify:playlist:demo1', cardName: 'Morning Mix', cardURL: 'spotify:playlist:demo1', account: 'acct' },
              { ts: '2026-10-23T17:00:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Best Of Rock.FM', cardURL: 'http://example.com/rock' },
            ],
          },
        },
      }),
    });

    test('4. a playlist the speaker played lately goes onto a key', async ({ app, mock }) => {
      const { page } = app;
      await openKeys(page);
      await editor(page).getByRole('tab', { name: L('srcSpotify') }).click();
      await expect(editor(page).locator('.kae-item')).toHaveCount(1);
      await mock.resetCalls();
      await editor(page).getByRole('button', { name: L('putName', { what: 'Morning Mix', n: 5, name: NAME }) }).click();
      await mock.expectCalled('SaveSpotifyPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 5 && c.args[4] === 'spotify:playlist:demo1' });
      await expect(result(page)).toHaveText(en('ui2.radio.saved', { n: 5, speaker: NAME }));
    });
  });

  test.describe("what's playing", () => {
    test.use({ scenario: scenario({ bindings: { SavePlayingPreset: { value: { kind: 'stream', name: 'Best Of Rock.FM' } } } }) });

    test("5. goes onto the key through the agent's save-playing", async ({ app, mock }) => {
      const { page } = app;
      await openKeys(page);
      await expect(editor(page).locator('.kae-now-text')).toHaveText(L('nowPlays', { name: NAME, what: 'Best Of Rock.FM' }));
      await putOnSpeaker(page, 5, 'Best Of Rock.FM');
      await mock.resetCalls();
      await page.locator('#kaeSaveNow').click();
      await mock.expectCalled('SavePlayingPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 5 });
      await expect(result(page)).toHaveText(en('ui2.radio.saved', { n: 5, speaker: NAME }));
      await expect(key(page, 5)).toContainText('Best Of Rock.FM');
      // The agent stored it; the app wrote nothing of its own.
      expect(await mock.count('SetPreset')).toBe(0);
    });
  });

  test.describe('already on another key', () => {
    test.use({
      scenario: scenario({
        bindings: { SavePlayingPreset: { error: 'status 409: {"code":"already-on-slot","slot":2,"name":"Best Of Rock.FM","error":"already on key 2"}' } },
      }),
    });

    test('6. says where it is and moves it here on request, without a dialog', async ({ app, mock }) => {
      const { page } = app;
      await openKeys(page);
      await page.locator('#kaeSaveNow').click();
      await expect(result(page)).toContainText(L('conflict', { what: 'Best Of Rock.FM', n: 2, name: NAME }));
      await expect(result(page)).toHaveClass(/kae-result--warn/);
      await expect(page.locator('#warnModal')).toBeHidden();
      await expect(page.locator('#errorModal')).toBeHidden();
      await mock.resetCalls();
      const move = result(page).getByRole('button', { name: en('preset.moveButton', { n: 5 }) });
      await move.focus();
      await page.keyboard.press('Enter');
      await mock.expectCalled('MovePreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 2 && c.args[3] === 5 });
      await expect(result(page)).toHaveText(en('ui2.radio.saved', { n: 5, speaker: NAME }));
      await expect(key(page, 5)).toContainText('Best Of Rock.FM');
      await expect(key(page, 2)).toContainText(L('keyEmpty'));
      // The Move button went with the move: the keyboard is on the key now.
      await expect(key(page, 5)).toBeFocused();
    });
  });

  test.describe('nothing to keep', () => {
    test.use({
      scenario: scenario({
        bindings: { SavePlayingPreset: { error: 'status 422: {"code":"nothing-saveable","error":"This source cannot be saved."}' } },
      }),
    });

    test('7. a 422 reads in plain words in the editor', async ({ app }) => {
      const { page } = app;
      await openKeys(page);
      await page.locator('#kaeSaveNow').click();
      await expect(result(page)).toHaveText(L('nothingSaveable', { name: NAME }));
      await expect(result(page)).toHaveClass(/kae-result--warn/);
      await expect(page.locator('#errorModal')).toBeHidden();
      await expect(key(page, 5)).toContainText(L('keyEmpty'));
    });
  });

  test.describe('a station refused for a reason of its own', () => {
    const WEBPAGE = 'This address is a web page, not a stream.';
    test.use({
      scenario: scenario({
        bindings: { SetPreset: { error: `status 422: {"code":"stream-url-is-webpage","error":"${WEBPAGE}"}` } },
      }),
    });

    test("10. says the agent's reason, not that what plays cannot be kept", async ({ app }) => {
      const { page } = app;
      await openKeys(page);
      await editor(page).getByLabel(L('radioSearchLabel')).fill('kexp');
      await editor(page).getByRole('button', { name: L('search'), exact: true }).click();
      await editor(page).getByRole('button', { name: L('putName', { what: 'KEXP', n: 5, name: NAME }) }).click();
      await expect(result(page)).toHaveText(en('ui2.radio.saveFailed', { n: 5, speaker: NAME, error: WEBPAGE }));
      await expect(result(page)).not.toContainText(L('nothingSaveable', { name: NAME }));
      await expect(result(page)).toHaveClass(/kae-result--error/);
      await expect(page.locator('#errorModal')).toBeHidden();
    });
  });

  test.describe("what's playing on Spotify", () => {
    test.use({
      scenario: scenario({
        // The speaker was never picked in Spotify: the key stores, and the
        // notice joins the verdict.
        bindings: { SpotifyNowPlaying: { value: { bitrate: 0, track: '', artist: '', cover: '', context: '', account: 'acct', canRecall: false, premiumRequired: false, audioKeyRefused: false, product: 'premium' } } },
      }),
    });

    test("11. goes onto the key through the Spotify save, with its notice in the editor's line", async ({ app, mock }) => {
      const { page } = app;
      await openKeys(page);
      await playOnSpeaker(page, SPOTIFY_ITEM);
      await mock.resetCalls();
      await page.locator('#kaeSaveNow').click();
      await mock.expectCalled('SaveSpotifyPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 5 && c.args[4] === PLAYLIST });
      await expect(result(page)).toHaveText(`${en('ui2.radio.saved', { n: 5, speaker: NAME })} ${en('preset.spotifyKeyNeedsLogin')}`);
      await expect(result(page)).toHaveClass(/kae-result--warn/);
      await expect(key(page, 5)).toContainText('Discover Weekly');
      // The agent's save-playing leaves Spotify to the app: not asked.
      expect(await mock.count('SavePlayingPreset')).toBe(0);
    });
  });

  test.describe("what's playing on Spotify, already on another key", () => {
    test.use({ scenario: scenario({ bindings: { SaveSpotifyPreset: { error: CONFLICT_2 } } }) });

    test('12. offers the move in the editor, not the dialog', async ({ app }) => {
      const { page } = app;
      await openKeys(page);
      await playOnSpeaker(page, SPOTIFY_ITEM);
      await page.locator('#kaeSaveNow').click();
      await expect(result(page)).toContainText(L('conflict', { what: 'Best Of Rock.FM', n: 2, name: NAME }));
      await expect(result(page).getByRole('button', { name: en('preset.moveButton', { n: 5 }) })).toBeVisible();
      await expect(page.locator('#warnModal')).toBeHidden();
      await expect(page.locator('#errorModal')).toBeHidden();
    });
  });

  test("13. a Pandora station that plays goes onto the key as the speaker's own item", async ({ app, mock }) => {
    const { page } = app;
    await openKeys(page);
    await playOnSpeaker(page, { source: 'PANDORA', location: '111', name: 'Jazz Radio', art: '' });
    await mock.resetCalls();
    await page.locator('#kaeSaveNow').click();
    await mock.expectCalled('SaveNativePreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 5 && c.args[3] === 'Jazz Radio' });
    await expect(result(page)).toHaveText(en('ui2.radio.saved', { n: 5, speaker: NAME }));
    await expect(key(page, 5)).toContainText('Jazz Radio');
    expect(await mock.count('SavePlayingPreset')).toBe(0);
  });

  test.describe('an agent without save-playing', () => {
    test.use({
      scenario: scenario({
        bindings: {
          SavePlayingPreset: { error: 'status 404: 404 page not found' },
          // The app's own save copies key 2, which the agent refuses to keep twice.
          SetPreset: { error: CONFLICT_2 },
        },
      }),
    });

    test("14. falls back to the app's own save, and its 409 still offers the move in the editor", async ({ app, mock }) => {
      const { page } = app;
      await openKeys(page);
      await mock.resetCalls();
      await page.locator('#kaeSaveNow').click();
      await mock.expectCalled('SetPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 5 && c.args[3] === 'Best Of Rock.FM' });
      await expect(result(page)).toContainText(L('conflict', { what: 'Best Of Rock.FM', n: 2, name: NAME }));
      await expect(result(page)).toHaveClass(/kae-result--warn/);
      // No dialog, and no "not confirmed" under it.
      await expect(page.locator('#warnModal')).toBeHidden();
      await expect(result(page)).not.toContainText(en('ui2.radio.notConfirmed', { n: 5, speaker: NAME }));
      await result(page).getByRole('button', { name: en('preset.moveButton', { n: 5 }) }).click();
      await mock.expectCalled('MovePreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 2 && c.args[3] === 5 });
      await expect(result(page)).toHaveText(en('ui2.radio.saved', { n: 5, speaker: NAME }));
    });
  });

  test.describe('while the station search is typed in', () => {
    test.use({ fakeClock: true });

    test('15. a repaint of the page keeps the field, its words, the caret and the outcome line', async ({ app }) => {
      const { page } = app;
      await settle(page, 9_000);
      await openKeys(page);
      const q = editor(page).getByLabel(L('radioSearchLabel'));
      await q.click();
      await page.keyboard.type('kexp radio');
      // Marks on the very nodes: a rebuilt field or line would come back without.
      await q.evaluate((el) => {
        el.setSelectionRange(2, 4);
        el.dataset.e2eMark = 'field';
        document.getElementById('kaeResult').dataset.e2eMark = 'line';
      });
      // The song changes, and the every-speaker poll repaints the page.
      await playOnSpeaker(page, { source: 'INTERNET_RADIO', location: 'http://demobox.local:8888/stream/2', name: 'Next Song', art: '' });
      await runFor(page, 16_000);
      await expect(editor(page).locator('.kae-now-text')).toHaveText(L('nowPlays', { name: NAME, what: 'Next Song' }));
      const now = await q.evaluate((el) => ({
        mark: el.dataset.e2eMark, value: el.value, start: el.selectionStart, end: el.selectionEnd,
        focused: document.activeElement === el, line: document.getElementById('kaeResult').dataset.e2eMark,
      }));
      expect(now).toEqual({ mark: 'field', value: 'kexp radio', start: 2, end: 4, focused: true, line: 'line' });
    });
  });
});

test.describe('the classic layout', () => {
  test.use({ ui2: false });

  test('8. has no key assignment editor', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#ui2KeyEditor')).toHaveCount(0);
    await expect(page.locator('.kae')).toHaveCount(0);
  });
});

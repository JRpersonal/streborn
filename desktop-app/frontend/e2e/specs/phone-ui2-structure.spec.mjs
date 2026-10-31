// phone-ui2-structure: the phone remote in the UI 2.0 preview (Aa > Layout,
// localStorage str.ui2Preview = '1'): the speaker card on top with the
// speaker list under it on every tab, then Listen, Find, Speakers and More as
// Help and info. The second half pins the current layout without the key, so
// the preview's markup and hooks provably change nothing there.

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

const NAME = PHONE.SETTINGS.info.name; // 'Living Room'

async function bootDone(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
}

// The phone fixture boots before the test body, so the preview is switched on
// the way a phone has it: stored, then the page (re)loaded.
async function preview(page) {
  await page.addInitScript(() => { try { localStorage.setItem('str.ui2Preview', '1'); } catch (e) { /* none */ } });
  await page.reload();
  await expect(page.locator('html')).toHaveClass(/(^|\s)ui2(\s|$)/);
  await bootDone(page);
}

async function openTab(page, name) {
  await page.locator('#tab' + name).click();
  await expect(page.locator('#panel' + name)).toBeVisible();
}

// a precedes b in document order.
const before = (page, a, b) => page.evaluate(([x, y]) => {
  const ex = document.querySelector(x), ey = document.querySelector(y);
  return !!(ex && ey && (ex.compareDocumentPosition(ey) & Node.DOCUMENT_POSITION_FOLLOWING));
}, [a, b]);

test.describe('UI 2.0 preview', () => {
  test('the speaker card names the speaker with one status line; no logo, power and Aa stay', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await expect(page.locator('header #devName')).toHaveText(NAME);
    await expect(page.locator('#u2Status')).toHaveText('Playing');
    await expect(page.locator('header img')).toBeHidden();
    await expect(page.locator('header .brand')).toBeHidden();
    await expect(page.locator('#powerBtn')).toBeVisible();
    await expect(page.locator('#a11yTrigger')).toBeVisible();
    await expect(page.locator('#dev')).toHaveAttribute('aria-expanded', 'false');
    expect(phone.errors).toEqual([]);
  });

  test('the chevron discloses one speaker list, this speaker first, on every tab', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await expect(page.locator('#u2List')).toBeHidden();
    await page.locator('#dev').click();
    await expect(page.locator('#u2List')).toBeVisible();
    await expect(page.locator('#dev')).toHaveAttribute('aria-expanded', 'true');
    // The existing card, moved: one list, never a copy.
    await expect(page.locator('#peersCard')).toHaveCount(1);
    await expect(page.locator('#u2List #peersCard')).toBeVisible();
    await expect(page.locator('#panelSpeakers #peersCard')).toHaveCount(0);
    const first = page.locator('#peers > :first-child');
    await expect(first).toHaveClass(/u2-self/);
    await expect(first).toContainText(NAME);
    const links = page.locator('#peers a.peer');
    await expect(links).toHaveText(PHONE.PEERS.map((p) => p.name));
    // A tap opens that speaker in the same layout.
    for (let i = 0; i < PHONE.PEERS.length; i++) {
      await expect(links.nth(i)).toHaveAttribute('href', PHONE.PEERS[i].url + '#ui2');
    }
    await page.locator('#dev').click();
    await expect(page.locator('#u2List')).toBeHidden();
    // The same list from the Find tab.
    await openTab(page, 'Browse');
    await page.locator('#dev').click();
    await expect(page.locator('#u2List #peersCard')).toBeVisible();
    await expect(page.locator('#panelBrowse')).toBeVisible();
  });

  test('the tabs read Listen, Find, Speakers, More, with the Speakers tab always there', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await expect(page.locator('#tlPlay')).toHaveText('Listen');
    await expect(page.locator('#tabPlay')).toHaveAttribute('aria-label', 'Listen');
    await expect(page.locator('#tlBrowse')).toHaveText('Find');
    await expect(page.locator('#tlSpeakers')).toHaveText('Speakers');
    await expect(page.locator('#tlMore')).toHaveText('More');
    await expect(page.locator('#tabSpeakers')).toBeVisible();
    // Line icons instead of the emoji.
    await expect(page.locator('#tabPlay .ti2 svg')).toBeVisible();
    await expect(page.locator('#tabPlay .ti')).toBeHidden();
  });

  test('this speaker\'s own settings sit on Speakers under its name; updates stay on More', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await openTab(page, 'Speakers');
    await expect(page.locator('#u2SpkHead')).toHaveText(NAME);
    for (const id of ['bassCard', 'identCard', 'dispCard']) {
      await expect(page.locator(`#panelSpeakers #u2SpkSettings #${id}`)).toHaveCount(1);
      await expect(page.locator(`#panelMore #${id}`)).toHaveCount(0);
    }
    await expect(page.locator('#identCard')).toBeVisible();
    await openTab(page, 'More');
    await expect(page.locator('#panelMore #suCard')).toBeVisible();
    await expect(page.locator('#u2lUpdates')).toHaveText('Updates');
    expect(await before(page, '#u2lUpdates', '#suCard')).toBe(true);
  });

  test('More is Help and info, in order, with support links only there', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await openTab(page, 'More');
    await expect(page.locator('#u2lHelp')).toHaveText('Help and info');
    await expect(page.locator('#btnDiag')).toBeVisible();
    await expect(page.locator('#u2Issues')).toHaveAttribute('href', 'https://github.com/JRpersonal/streborn/issues');
    await expect(page.locator('#u2Issues')).toHaveText('Report on GitHub');
    await expect(page.locator('#lnkVoteIdeas')).toBeVisible();
    await expect(page.locator('#u2Source')).toHaveAttribute('href', 'https://github.com/JRpersonal/streborn');
    await expect(page.locator('#u2WorldMap')).toHaveAttribute('href', 'https://st-reborn.de/?share&src=app#community');
    const order = ['#u2lHelp', '#btnDiag', '#u2Issues', '#lnkVoteIdeas', '#u2Source', '#u2WorldMap', '.sponsors', '#shareCard', '#u2About', 'footer'];
    for (let i = 1; i < order.length; i++) expect(await before(page, order[i - 1], order[i]), `${order[i - 1]} before ${order[i]}`).toBe(true);
    // The donate card states a fact instead of asking, and lives on More only.
    await expect(page.locator('#lblSupport')).toHaveText('ST Reborn is free and open source. It is funded by donations.');
    await expect(page.locator('.sponsors')).toHaveCount(1);
    await expect(page.locator('#panelMore .sponsors')).toHaveCount(1);
    await expect(page.locator('a[href*="ko-fi.com"], a[href*="paypal.me"], a[href*="github.com/sponsors"]')).toHaveCount(3);
    await expect(page.locator('#panelMore a[href*="ko-fi.com"], #panelMore a[href*="paypal.me"], #panelMore a[href*="github.com/sponsors"]')).toHaveCount(3);
    // About: version, website, the legal pages.
    await expect(page.locator('#u2Ver')).toHaveText(`ST Reborn ${PHONE.VERSION.version}`);
    await expect(page.locator('#u2Privacy')).toHaveAttribute('href', 'https://st-reborn.de/privacy/');
    await expect(page.locator('#u2Imprint')).toHaveAttribute('href', 'https://st-reborn.de/imprint/');
    await expect(page.locator('#u2About a[href="https://st-reborn.de"]')).toBeVisible();
    // The language picker and the home-screen tip stay in the footer.
    await expect(page.locator('footer #langPick')).toBeVisible();
    await expect(page.locator('footer #lblTip')).toBeVisible();
  });

  test('the keys and the Find summary name the speaker', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await expect(page.locator('#lblPresets')).toHaveText(`Keys on ${NAME}`);
    await openTab(page, 'Browse');
    await expect(page.locator('#lblFindSum')).toHaveText(`Search the worldwide station directory and play it on ${NAME}.`);
  });

  test('no raw keys or placeholders and no sideways scroll on any tab', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await page.locator('#dev').click();
    for (const name of ['Play', 'Browse', 'Speakers', 'More']) {
      await openTab(page, name);
      const bad = await page.evaluate(() => {
        const out = [];
        const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
        for (let n = walker.nextNode(); n; n = walker.nextNode()) {
          const p = n.parentElement;
          if (!p || p.closest('script,style')) continue;
          if (/\{\{|\}\}|\{name\}/.test(n.nodeValue)) out.push(n.nodeValue.trim().slice(0, 60));
        }
        return out;
      });
      expect(bad, `tab ${name}`).toEqual([]);
      const w = await page.evaluate(() => document.documentElement.scrollWidth);
      expect(w, `tab ${name} scrolls sideways`).toBeLessThanOrEqual(390);
    }
  });
});

test.describe('UI 2.0 preview, grouped', () => {
  test.use({ phoneGrouped: true });

  test('the card says who plays along and the list keeps the minus', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await expect(page.locator('#u2Status')).toHaveText(/with Kitchen/);
    await page.locator('#dev').click();
    const kitchen = page.locator('#peers .peerrow', { has: page.locator('a.peer', { hasText: 'Kitchen' }) });
    await expect(kitchen.locator('.leavebtn')).toHaveText('−');
    await expect(kitchen.locator('.leavebtn')).toHaveAttribute('aria-label', /Kitchen/);
    // The others can still join.
    const bedroom = page.locator('#peers .peerrow', { has: page.locator('a.peer', { hasText: 'Bedroom' }) });
    await expect(bedroom.locator('.joinbtn')).toHaveText('+');
  });
});

// The status line on the speaker card, from the state the speaker reports.
const statusXML = (source, playState) => '<?xml version="1.0" encoding="UTF-8"?>'
  + `<nowPlaying deviceID="A1B2C3D4E5F6" source="${source}">`
  + `<ContentItem source="${source}" location="http://example.com/rock" isPresetable="true">`
  + '<itemName>Best Of Rock.FM</itemName></ContentItem>'
  + (playState ? `<playStatus>${playState}</playStatus>` : '') + '</nowPlaying>';

test.describe('UI 2.0 preview, a speaker in standby', () => {
  test.use({ phoneOverrides: { '/api/status': { body: statusXML('STANDBY', ''), contentType: 'application/xml' } } });

  test('says Standby', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await expect(page.locator('#u2Status')).toHaveText('Standby');
  });
});

test.describe('UI 2.0 preview, a paused speaker', () => {
  test.use({ phoneOverrides: { '/api/status': { body: statusXML('LOCAL_INTERNET_RADIO', 'PAUSE_STATE'), contentType: 'application/xml' } } });

  test('says Paused', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await expect(page.locator('#u2Status')).toHaveText('Paused');
  });
});

test.describe('UI 2.0 preview, half of a stereo pair', () => {
  test.use({
    phoneOverrides: {
      '/api/box/zone/volume': { body: { grouped: true, stereo: true, stereoName: 'Living Room pair', members: [
        { name: 'Left', ip: '192.0.2.50', deviceID: 'A1B2C3D4E5F6', isSelf: true, isMaster: true, volume: 30 },
        { name: 'Kitchen', ip: '192.0.2.51', deviceID: 'B1B2C3D4E5F6', volume: 30 },
      ], loudest: 30, average: 30 } },
    },
  });

  test('says Playing (stereo) and names the pair in the list, not the half', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await expect(page.locator('#u2Status')).toHaveText('Playing (stereo)');
    await page.locator('#dev').click();
    const self = page.locator('#peers .u2-self');
    await expect(self).toContainText('Living Room pair');
    // The partner is part of this row, not a row of its own.
    await expect(page.locator('#peers a.peer', { hasText: 'Kitchen' })).toHaveCount(0);
  });
});

// A "+" in the list under the speaker card that changed nothing (the speaker
// refused) gets its button back, not a busy mark for good.
test.describe('UI 2.0 preview, a join the speaker refuses', () => {
  test.use({ phoneOverrides: { '/api/box/zone': { body: { ok: false } } } });

  test('the + comes back', async ({ phone }) => {
    const { page, handler } = phone;
    await preview(page);
    await page.locator('#dev').click();
    const bedroom = page.locator('#peers .peerrow', { has: page.locator('a.peer', { hasText: 'Bedroom' }) });
    const join = bedroom.locator('.joinbtn');
    await expect(join).toHaveText('+');
    await join.click();
    await expect.poll(() => handler.calls.some((c) => c.method === 'POST' && new URL(c.url).pathname === '/api/box/zone')).toBe(true);
    const again = page.locator('#peers .peerrow', { has: page.locator('a.peer', { hasText: 'Bedroom' }) }).locator('.joinbtn');
    await expect(again).toHaveText('+');
    await expect(again).toBeEnabled();
  });
});

test.describe('UI 2.0 preview in German', () => {
  test.use({ phoneLang: 'de' });

  test('the tabs and Help and info read German, the world map has the locale', async ({ phone }) => {
    const { page } = phone;
    await preview(page);
    await expect(page.locator('#tlPlay')).toHaveText('Hören');
    await expect(page.locator('#tlSpeakers')).toHaveText('Lautsprecher');
    await expect(page.locator('#lblPresets')).toHaveText(`Tasten auf ${NAME}`);
    await openTab(page, 'More');
    await expect(page.locator('#u2lHelp')).toHaveText('Hilfe und Infos');
    await expect(page.locator('#u2WorldMap')).toHaveAttribute('href', 'https://st-reborn.de/de/?share&src=app#community');
    await expect(page.locator('#u2Privacy')).toHaveAttribute('href', 'https://st-reborn.de/de/datenschutz/');
    await expect(page.locator('#u2Imprint')).toHaveAttribute('href', 'https://st-reborn.de/de/impressum/');
  });
});

test.describe('current layout (no key)', () => {
  test('nothing of the preview renders', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(page.locator('html')).not.toHaveClass(/(^|\s)ui2(\s|$)/);
    const shown = await page.evaluate(() => [...document.querySelectorAll('.ui2-only')]
      .filter((e) => e.getClientRects().length > 0).map((e) => e.id || e.className));
    expect(shown).toEqual([]);
    await expect(page.locator('header img')).toBeVisible();
    await expect(page.locator('#tabPlay .ti')).toBeVisible();
  });

  test('the name jumps to Speakers, where the peer list still lives', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(page.locator('#dev')).toHaveClass(/haspeers/);
    await page.locator('#dev').click();
    await expect(page.locator('#panelSpeakers')).toBeVisible();
    await expect(page.locator('#panelSpeakers #peersCard')).toBeVisible();
    await expect(page.locator('#u2List')).toBeHidden();
    const links = page.locator('#peers a.peer');
    await expect(links).toHaveCount(PHONE.PEERS.length);
    for (let i = 0; i < PHONE.PEERS.length; i++) {
      await expect(links.nth(i)).toHaveAttribute('href', PHONE.PEERS[i].url);
    }
    await expect(page.locator('#peers .u2-self')).toHaveCount(0);
  });

  test('the tabs, the cards on More and the keys heading are as before', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(page.locator('#tlPlay')).toHaveText('Play');
    await expect(page.locator('#tlSpeakers')).toHaveText('Speakers');
    await expect(page.locator('#lblPresets')).toHaveText('Presets');
    for (const id of ['bassCard', 'identCard', 'dispCard', 'suCard']) {
      await expect(page.locator(`#panelMore #${id}`)).toHaveCount(1);
    }
    await expect(page.locator('#lblSupport')).toHaveText('You like ST Reborn?');
    await openTab(page, 'Browse');
    await expect(page.locator('#lblFindSum')).toHaveText('Search the worldwide station directory and play it here.');
    await expect(page.locator('#dev')).toHaveAttribute('aria-label', 'Switch to another speaker');
  });
});

test.describe('current layout in German', () => {
  test.use({ phoneLang: 'de' });

  test('the first tab still reads Spielen', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(page.locator('#tlPlay')).toHaveText('Spielen');
    await expect(page.locator('#tlSpeakers')).toHaveText('Boxen');
  });
});

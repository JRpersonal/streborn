// phone-update-group-note: the phone remote's single-speaker update says
// beforehand that a speaker in a live group leaves that group while it
// restarts for the update (discussion #1339). A standalone speaker and half of
// a stereo pair (which survives a restart) get no such note.

import { test, expect } from '../fixtures.mjs';

const SU = '/api/agent/self-update';
const IDLE_UPDATE = { body: { state: 'idle', current: '1.0.21', latest: '1.1.1', available: true, engine: 'ok' } };

async function bootDone(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
}

function groupNote(page) {
  return page.evaluate(() => window.T.suGrpNote);
}

test.describe('grouped speaker with an update waiting', () => {
  test.use({ phoneGrouped: true, phoneOverrides: { [SU]: IDLE_UPDATE } });

  test('says the speaker leaves its group before the update starts', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    const note = await groupNote(page);
    expect(note).toBeTruthy();
    await page.locator('#tabMore').click();
    await expect(page.locator('#btnSu')).toBeVisible();
    await expect(page.locator('#suMsg')).toHaveText(note);
  });
});

test.describe('standalone speaker with an update waiting', () => {
  test.use({ phoneGrouped: false, phoneOverrides: { [SU]: IDLE_UPDATE } });

  test('has no group note', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await page.locator('#tabMore').click();
    await expect(page.locator('#btnSu')).toBeVisible();
    await expect(page.locator('#suMsg')).toHaveText('');
  });
});

test.describe('stereo pair with an update waiting', () => {
  test.use({ phoneOverrides: {
    [SU]: IDLE_UPDATE,
    '/api/box/zone/volume': { body: { grouped: true, stereo: true, stereoName: 'Living Room pair', members: [
      { name: 'Left', ip: '192.0.2.50', deviceID: 'A1B2C3D4E5F6', isSelf: true, isMaster: true, volume: 30 },
      { name: 'Right', ip: '192.0.2.51', deviceID: 'B1B2C3D4E5F6', volume: 30 },
    ], loudest: 30, average: 30 } },
  } });

  test('has no group note: a pair survives the restart', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await page.locator('#tabMore').click();
    await expect(page.locator('#btnSu')).toBeVisible();
    await expect(page.locator('#suMsg')).toHaveText('');
  });
});

// phone-update-group-note: the phone remote's update says beforehand that a
// speaker in a live group leaves that group while it restarts for the update
// (discussion #1339), for one speaker and for "Update all speakers". A
// standalone speaker and half of a stereo pair (which survives a restart) get
// no such note.

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

// "Update all speakers" breaks a group just the same: a member that restarts
// leaves it. The fleet plan says so before the tap when the run will restart
// this speaker or a member of its live group, and not for a stereo pair.
function fleetPlan(speakers) {
  return { body: { state: 'idle', current: '1.0.21', latest: '1.1.1', available: true, engine: 'ok',
    fleet: { mode: 'plan', target: '1.1.1', at: '2026-10-26T12:00:00Z', speakers } } };
}
const SELF_NEEDS = { name: 'Living Room', self: true, version: '1.0.21', target: '1.1.1', needs: true, state: 'waiting' };
const SELF_CURRENT = { name: 'Living Room', self: true, version: '1.1.1', target: '1.1.1', needs: false, state: 'skipped', reason: 'current' };
const KITCHEN = { name: 'Kitchen', ip: '192.0.2.51', port: 8888, version: '1.0.21', target: '1.1.1', needs: true, state: 'waiting' };
const BEDROOM = { name: 'Bedroom', ip: '192.0.2.52', port: 8888, version: '1.0.21', target: '1.1.1', needs: true, state: 'waiting' };

async function openFleetCard(page) {
  await bootDone(page);
  await page.locator('#tabMore').click();
  await expect(page.locator('#btnSuAll')).toBeVisible();
}

function fleetNote(page) {
  return page.evaluate(() => window.T.suGrpAllNote);
}

test.describe('update all, this speaker grouped and in the run', () => {
  test.use({ phoneGrouped: true, phoneOverrides: { [SU]: fleetPlan([SELF_NEEDS, KITCHEN, BEDROOM]) } });

  test('says beforehand that grouped speakers leave their group', async ({ phone }) => {
    const { page } = phone;
    await openFleetCard(page);
    const note = await fleetNote(page);
    expect(note).toBeTruthy();
    expect(note).not.toEqual(await groupNote(page));
    await expect(page.locator('#suMsg')).toHaveText(note);
    if (process.env.SHOOT_DIR) await page.locator('#suCard').screenshot({ path: `${process.env.SHOOT_DIR}/phone-update-all-group-note.png` });
  });
});

test.describe('update all, only a group member in the run', () => {
  test.use({ phoneGrouped: true, phoneOverrides: { [SU]: fleetPlan([SELF_CURRENT, KITCHEN]) } });

  test('says it too: the member leaves this speaker\'s group', async ({ phone }) => {
    const { page } = phone;
    await openFleetCard(page);
    await expect(page.locator('#suMsg')).toHaveText(await fleetNote(page));
  });
});

test.describe('update all, the group untouched by the run', () => {
  test.use({ phoneGrouped: true, phoneOverrides: { [SU]: fleetPlan([SELF_CURRENT, BEDROOM]) } });

  test('has no group note', async ({ phone }) => {
    const { page } = phone;
    await openFleetCard(page);
    await expect(page.locator('#suMsg')).toHaveText('');
  });
});

test.describe('update all, no group', () => {
  test.use({ phoneGrouped: false, phoneOverrides: { [SU]: fleetPlan([SELF_NEEDS, KITCHEN]) } });

  test('has no group note', async ({ phone }) => {
    const { page } = phone;
    await openFleetCard(page);
    await expect(page.locator('#suMsg')).toHaveText('');
  });
});

test.describe('update all, a stereo pair', () => {
  test.use({ phoneOverrides: {
    [SU]: fleetPlan([SELF_NEEDS, KITCHEN]),
    '/api/box/zone/volume': { body: { grouped: true, stereo: true, stereoName: 'Living Room pair', members: [
      { name: 'Left', ip: '192.0.2.50', deviceID: 'A1B2C3D4E5F6', isSelf: true, isMaster: true, volume: 30 },
      { name: 'Right', ip: '192.0.2.51', deviceID: 'B1B2C3D4E5F6', volume: 30 },
    ], loudest: 30, average: 30 } },
  } });

  test('has no group note: a pair survives the restart', async ({ phone }) => {
    const { page } = phone;
    await openFleetCard(page);
    await expect(page.locator('#suMsg')).toHaveText('');
  });
});

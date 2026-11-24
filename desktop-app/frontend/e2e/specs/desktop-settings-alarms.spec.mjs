// The alarm clock in Speaker settings (discussion #2090): until now alarms
// could only be set on the speaker's phone page. The app edits the same
// document behind GET/PUT /api/alarms, with the revision the phone page sends
// too (#1918), so neither editor overwrites the other.

import { test, expect } from '../fixtures.mjs';

const PRESETS = [
  { slot: 1, name: 'NDR 1 Niedersachsen' },
  { slot: 2, name: 'Deutschlandfunk' },
];

function alarmDoc() {
  return {
    zone: 'Europe/Berlin',
    alarms: [
      { id: 'a1', enabled: true, hour: 6, minute: 45, days: [1, 2, 3, 4, 5], slot: 2, volume: 20, autoOff: 60, snooze: 9 },
      { id: 'a2', enabled: false, hour: 9, minute: 0, days: [0, 6], slot: 1, autoOff: 120, snooze: 9 },
    ],
    skipDates: ['2026-12-24'],
    rev: 4,
    status: { clockTrusted: true, zoneResolved: true, nextFire: '2026-11-25T05:45:00Z', maxSkipDates: 60 },
  };
}

// A speaker that keeps the document and answers like the agent does: the
// stored revision on GET, 409 with the current document on a stale PUT.
async function speakerWithAlarms(page) {
  let doc = alarmDoc();
  const puts = [];
  await page.route('**/api/alarms', async (route) => {
    const req = route.request();
    if (req.method() === 'PUT') {
      const body = JSON.parse(req.postData() || '{}');
      puts.push(body);
      if (typeof body.rev === 'number' && body.rev !== doc.rev) {
        return route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify(doc) });
      }
      doc = { ...doc, zone: body.zone, alarms: body.alarms.map((a, i) => ({ ...a, id: a.id || `n${i}` })), skipDates: body.skipDates, rev: doc.rev + 1 };
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(doc) });
  });
  return { puts, get doc() { return doc; }, set doc(d) { doc = d; } };
}

async function openAlarms(page) {
  await page.locator('.tab-btn[data-view="settings"]').click();
  const group = page.locator('#settingsBody details.settings-group', { has: page.locator('#alarmsSection') });
  await group.locator('summary').click();
  const sec = page.locator('#alarmsSection');
  await expect(sec.locator('.alarms-row')).toHaveCount(2);
  return sec;
}

test.describe('classic layout: alarm clock in Speaker settings', () => {
  test.use({ ui2: false, appLocale: 'en', scenario: { bindings: { GetPresets: { value: PRESETS } } } });

  test('lists the speaker\'s alarms with their days and keys', async ({ app }) => {
    const { page } = app;
    await speakerWithAlarms(page);
    const sec = await openAlarms(page);
    const first = sec.locator('.alarms-row').first();
    await expect(first.locator('.alarms-what')).toHaveText('Mo Tu We Th Fr · Key 2 · Deutschlandfunk');
    await expect(first.locator('input[type="checkbox"]')).toBeChecked();
    await expect(sec.locator('.alarms-row').nth(1).locator('.alarms-what')).toHaveText('Sa Su · Key 1 · NDR 1 Niedersachsen');
    await expect(sec.locator('#alarmsSum')).toContainText('Next:');
    await expect(sec.locator('#alarmsZone')).toHaveText('Times are in Europe/Berlin');
    await expect(sec.locator('.alarms-chip')).toHaveCount(1);
  });

  test('edits an alarm and saves it with the revision it was read at', async ({ app }) => {
    const { page } = app;
    const spk = await speakerWithAlarms(page);
    const sec = await openAlarms(page);
    await sec.locator('[data-alarm-edit="0"]').click();
    await sec.locator('#alarmsTime').fill('07:15');
    await sec.locator('[data-alarm-day="6"]').click(); // add Saturday
    await sec.locator('#alarmsSlot').selectOption('1');
    await sec.locator('#alarmsOff').selectOption('at');
    await sec.locator('#alarmsOffAt').fill('08:30');
    await sec.locator('#alarmsSave').click();
    await expect(sec.locator('.alarms-row')).toHaveCount(2);
    expect(spk.puts).toHaveLength(1);
    const sent = spk.puts[0];
    expect(sent.rev).toBe(4);
    expect(sent.skipDates).toEqual(['2026-12-24']);
    expect(sent.alarms[0]).toMatchObject({ id: 'a1', hour: 7, minute: 15, slot: 1, offAt: '08:30', autoOff: 0, enabled: true });
    expect(sent.alarms[0].days.sort()).toEqual([1, 2, 3, 4, 5, 6]);
    await expect(sec.locator('.alarms-row').first().locator('.alarms-what')).toHaveText('Mo Tu We Th Fr Sa · Key 1 · NDR 1 Niedersachsen');
  });

  test('switching an alarm on survives a save from the phone in between', async ({ app }) => {
    const { page } = app;
    const spk = await speakerWithAlarms(page);
    const sec = await openAlarms(page);
    // The phone page deletes the first alarm meanwhile.
    spk.doc = { ...spk.doc, alarms: [spk.doc.alarms[1]], rev: 5 };
    await sec.locator('.alarms-row').nth(1).locator('input[type="checkbox"]').check();
    await expect(sec.locator('.alarms-row')).toHaveCount(1);
    expect(spk.puts).toHaveLength(2);
    expect(spk.puts[1].rev).toBe(5);
    expect(spk.puts[1].alarms).toEqual([expect.objectContaining({ id: 'a2', enabled: true })]);
    await expect(sec.locator('.alarms-row input[type="checkbox"]')).toBeChecked();
  });

  test('adds a new alarm and refuses one without days', async ({ app }) => {
    const { page } = app;
    const spk = await speakerWithAlarms(page);
    const sec = await openAlarms(page);
    await sec.locator('#alarmsAdd').click();
    for (const d of [1, 2, 3, 4, 5]) await sec.locator(`[data-alarm-day="${d}"]`).click();
    await sec.locator('#alarmsSave').click();
    expect(spk.puts).toHaveLength(0);
    // Said in the card, not in a dialog, and the editor stays open for the fix.
    await expect(sec.locator('#alarmsMsg')).toHaveText('The alarm could not be saved.');
    await expect(page.locator('#errorModal')).toBeHidden();
    await expect(sec.locator('#alarmsSave')).toBeVisible();
    await sec.locator('[data-alarm-day="0"]').click();
    await sec.locator('#alarmsSave').click();
    await expect(sec.locator('.alarms-row')).toHaveCount(3);
    expect(spk.puts[0].alarms[2]).toMatchObject({ id: '', hour: 7, minute: 0, days: [0], slot: 1 });
  });

  test('adds and removes a day without an alarm', async ({ app }) => {
    const { page } = app;
    const spk = await speakerWithAlarms(page);
    const sec = await openAlarms(page);
    await sec.locator('#alarmsSkipDate').fill('2026-12-31');
    await sec.locator('#alarmsSkipAdd').click();
    await expect(sec.locator('.alarms-chip')).toHaveCount(2);
    expect(spk.puts[0].skipDates).toEqual(['2026-12-24', '2026-12-31']);
    await sec.locator('.alarms-chip-x').first().click();
    await expect(sec.locator('.alarms-chip')).toHaveCount(1);
    expect(spk.puts[1].skipDates).toEqual(['2026-12-31']);
  });

  test('says so when the speaker\'s STR has no alarm clock yet', async ({ app }) => {
    const { page } = app;
    await page.route('**/api/alarms', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>STR</title>' }));
    await page.locator('.tab-btn[data-view="settings"]').click();
    const group = page.locator('#settingsBody details.settings-group', { has: page.locator('#alarmsSection') });
    await group.locator('summary').click();
    await expect(page.locator('#alarmsSum')).toHaveText('The STR version on this speaker has no alarm clock yet. Update the speaker first.');
    await expect(page.locator('#alarmsAddRow')).toBeHidden();
  });

  test('screenshot', async ({ app }) => {
    test.skip(!process.env.SHOOT_DIR, 'only when shooting');
    const { page } = app;
    await speakerWithAlarms(page);
    const sec = await openAlarms(page);
    await sec.screenshot({ path: `${process.env.SHOOT_DIR}/alarms-list-en.png`, animations: 'disabled' });
    await sec.locator('[data-alarm-edit="0"]').click();
    await sec.screenshot({ path: `${process.env.SHOOT_DIR}/alarms-editor-en.png`, animations: 'disabled' });
  });
});

test.describe('classic layout: alarm clock, German', () => {
  test.use({ ui2: false, appLocale: 'de', scenario: { bindings: { GetPresets: { value: PRESETS } } } });

  test('screenshot', async ({ app }) => {
    test.skip(!process.env.SHOOT_DIR, 'only when shooting');
    const { page } = app;
    await speakerWithAlarms(page);
    const sec = await openAlarms(page);
    await expect(sec.locator('.alarms-row').first().locator('.alarms-what')).toHaveText('Mo Di Mi Do Fr · Taste 2 · Deutschlandfunk');
    await sec.screenshot({ path: `${process.env.SHOOT_DIR}/alarms-list-de.png`, animations: 'disabled' });
    await sec.locator('[data-alarm-edit="0"]').click();
    await sec.screenshot({ path: `${process.env.SHOOT_DIR}/alarms-editor-de.png`, animations: 'disabled' });
  });
});

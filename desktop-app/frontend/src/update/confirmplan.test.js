// #1954: the speaker update card is the owner's yes for the speakers it names.
// Update all asks a second time only about what the card could not know.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { updateAllConfirmPlan } from './confirmplan.js';

const here = dirname(fileURLToPath(import.meta.url));
const src = (p) => readFileSync(join(here, '..', p), 'utf8');
const A = '192.0.2.1';
const B = '192.0.2.2';
const C = '192.0.2.3';

describe('updateAllConfirmPlan', () => {
  const base = { named: [A, B], updateHosts: [B, A], engineCount: 0, warnings: 0, groupNoted: false, groupNoteShown: false };

  it('does not ask again when the card named exactly these speakers and nothing new was found', () => {
    expect(updateAllConfirmPlan(base)).toBe('none');
  });

  it('asks with the whole list when the caller named nothing (the Settings button)', () => {
    expect(updateAllConfirmPlan({ ...base, named: undefined })).toBe('full');
    expect(updateAllConfirmPlan({})).toBe('full');
    expect(updateAllConfirmPlan()).toBe('full');
  });

  it('asks with the whole list when the run would update other speakers than the card named', () => {
    expect(updateAllConfirmPlan({ ...base, updateHosts: [A, B, C] })).toBe('full');
    expect(updateAllConfirmPlan({ ...base, updateHosts: [A] })).toBe('full');
    expect(updateAllConfirmPlan({ ...base, updateHosts: [A, C] })).toBe('full');
    expect(updateAllConfirmPlan({ ...base, updateHosts: [] })).toBe('full');
  });

  it('stops for a pre-scan note: stick, weak Wi-Fi, tight storage, may stay dark', () => {
    expect(updateAllConfirmPlan({ ...base, warnings: 1 })).toBe('notes');
  });

  it('stops for speakers the card never named (engine-only repairs)', () => {
    expect(updateAllConfirmPlan({ ...base, engineCount: 1 })).toBe('notes');
  });

  it('stops for the group note only when the card did not show it', () => {
    expect(updateAllConfirmPlan({ ...base, groupNoted: true, groupNoteShown: true })).toBe('none');
    expect(updateAllConfirmPlan({ ...base, groupNoted: true, groupNoteShown: false })).toBe('notes');
  });
});

describe('the wiring', () => {
  const card = src('banners/speakerupdate.js');
  const all = src('update/all.js');
  const status = src('views/settings/status.js');
  const main = src('main.js');

  it('the card hands over the speakers it named and whether it showed the group note', () => {
    expect(card).toContain('named: [...wanted],');
    expect(card).toContain("groupNoteShown: !!$('sucGroupNote'),");
  });

  it('the Settings "Update all" button names nothing, so it keeps the full confirmation', () => {
    expect(status).toContain('uaAll.onclick = () => deps.updateAllBoxes();');
  });

  it('the run still offers Cancel whenever it asks, and asks unless the plan says none', () => {
    expect(all).toContain("if (ask !== 'none') {");
    expect(all).toContain('if (!confirmed) return false;');
  });

  it('the progress window says what happens to the speakers while it runs', () => {
    expect(main).toContain('id="uaLead"');
    expect(all).toContain("t('updateAll.confirmLead')");
  });
});

describe('the app update notice', () => {
  const app = src('appupdate.js');
  const help = src('shell2/help/help.js');

  it('"What\'s new" opens the releases list, the same page Help and info opens', () => {
    expect(help).toContain('releaseNotes: `$' + '{repo}/releases`,');
    expect(app).toContain('const releasesUrl = `$' + '{repo}/releases`;');
    expect(app).toContain('BrowserOpenURL(releasesUrl)');
    expect(app).not.toContain('BrowserOpenURL(notesUrl)');
  });
});

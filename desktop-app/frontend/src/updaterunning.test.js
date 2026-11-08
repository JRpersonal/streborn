// A running speaker update is shown clearly on the speaker it concerns
// (discussion #1764): bold, the accent colour, an activity indicator, and the
// words that it restarts and is not to be used meanwhile. In the classic
// speaker picker, the UI 2.0 sidebar, speakers list and speaker page, and the
// update card in Speaker Settings.
import { describe, it, expect, beforeAll, afterEach } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { setLocale } from './i18n/index.js';
import {
  clearUpdateRunning, isUpdateRunning, onUpdateRunningChange, setUpdateRunning,
  updateRunningChipHTML, updateRunningNoticeHTML,
} from './updaterunning.js';
import { speakerTileState } from './speakertile.js';
import { speakerChipKinds, speakerPillKind, statusPillHTML } from './shell2/pill.js';
import { roomItems } from './shell2/rooms.js';
import { speakerListHTML, speakerListModel } from './shell2/speakers/list.js';
import { speakerStateHTML } from './shell2/speakers/speaker.js';
import { srcOf, fileSource } from './test/source.js';

const dir = new URL('./i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));

const STR = { host: '192.0.2.20', port: 8888, deviceID: 'device-id-here', kind: 'STR', version: 'v1.0.10', friendlyName: 'Kitchen' };
const OTHER = { host: '192.0.2.21', port: 8888, deviceID: 'device-id-two', kind: 'STR', version: 'v1.0.10', friendlyName: 'Hall' };

beforeAll(async () => { await setLocale('en'); });
afterEach(() => clearUpdateRunning());

describe('the speakers an update runs on', () => {
  it('marks and unmarks a speaker, and tells the lists only on a change', () => {
    let n = 0;
    const off = onUpdateRunningChange(() => { n++; });
    setUpdateRunning(STR.host, true);
    setUpdateRunning(STR.host, true);
    expect(isUpdateRunning(STR.host)).toBe(true);
    expect(isUpdateRunning(OTHER.host)).toBe(false);
    setUpdateRunning(OTHER.host, true);
    setUpdateRunning(STR.host, false);
    expect(isUpdateRunning(STR.host)).toBe(false);
    clearUpdateRunning();
    clearUpdateRunning();
    expect(isUpdateRunning(OTHER.host)).toBe(false);
    expect(n).toBe(4);
    setUpdateRunning('', true);
    expect(isUpdateRunning('')).toBe(false);
    off();
  });
});

describe('the speaker tile', () => {
  it('is marked while its update runs, offline or not, and offers no second update', () => {
    setUpdateRunning(STR.host, true);
    const tile = speakerTileState({ ...STR, offline: true }, { needsUpdate: () => true });
    expect(tile.updateRunning).toBe(true);
    // Restarting drops it off the network: "update running" wins over "not reachable".
    expect(speakerPillKind(tile)).toBe('updateRunning');
    expect(speakerChipKinds(tile)).toEqual([]);
    expect(speakerTileState(OTHER, {}).updateRunning).toBe(false);
  });

  it('draws a busy pill in the accent colour, for a running and a finishing update', () => {
    for (const kind of ['updateRunning', 'updateFinishing']) {
      const html = statusPillHTML(kind);
      expect(html, kind).toContain('u-pill--accent');
      expect(html, kind).toContain('u-pill--busy');
    }
    expect(statusPillHTML('updateRunning')).toContain('Update running');
    expect(statusPillHTML('playing')).not.toContain('u-pill--busy');
  });

  it('gives the UI 2.0 sidebar row the hint as its tooltip, and a pair the mark of either half', () => {
    setUpdateRunning(STR.host, true);
    const [row] = roomItems([STR], {});
    expect(row.pill).toBe('updateRunning');
    expect(row.title).toBe(bundles.en['speaker.updateRunningHint']);
  });

  it('is what the classic speaker picker draws, chip and tooltip', () => {
    const chip = updateRunningChipHTML();
    expect(chip).toContain('class="box-updating"');
    expect(chip).toContain('class="update-running-spin" aria-hidden="true"');
    expect(chip).toContain('>Update running</span>');
    expect(chip).toContain(`title="${bundles.en['speaker.updateRunningHint'].replace(/'/g, '&#39;')}"`);
    const select = srcOf('renderBoxSelect');
    expect(select).toContain('tile.updateRunning ? updateRunningChipHTML()');
    expect(select).toContain('const showUpd = tile.needsUpdate && !tile.updateRunning;');
    expect(select).toContain("tile.updateRunning ? t('speaker.updateRunningHint') : offlineTitle(b)");
    // The finishing badge turns prominent too, keeping its own words.
    expect(select).toContain('box-stock-badge box-updating');
    expect(select).toContain("finishing ? 'speaker.updateFinishingBadge'");
  });
});

describe('the UI 2.0 speakers list and speaker page', () => {
  it('says it on the card, with nothing to press, even while the speaker is offline', () => {
    setUpdateRunning(STR.host, true);
    const model = speakerListModel([STR, OTHER], { needsUpdate: () => true, appVersion: 'v1.0.19' });
    const row = model.rows.find((r) => r.host === STR.host);
    expect(row.pill).toBe('updateRunning');
    expect(row.action).toBe('updating');
    expect(model.rows.find((r) => r.host === OTHER.host).action).toBe('update');
    const html = speakerListHTML(model);
    expect(html).toContain('class="spl-busy spl-updating" role="status"');
    expect(html).toContain('update-running-spin');
    expect(html).toContain(bundles.en['speaker.updateRunningHint'].replace(/'/g, '&#39;'));
  });

  it('puts the hint next to the pill on the speaker page', () => {
    setUpdateRunning(STR.host, true);
    const tile = speakerTileState(STR, {});
    const html = speakerStateHTML(STR, tile, '');
    expect(html).toContain('data-kind="updateRunning"');
    expect(html).toContain('<span class="sps-updating">');
    clearUpdateRunning();
    expect(speakerStateHTML(STR, speakerTileState(STR, {}), '')).not.toContain('sps-updating');
  });
});

describe('the update card in Speaker Settings', () => {
  it('draws the notice over the progress line, and the update keeps it there', () => {
    const notice = updateRunningNoticeHTML();
    expect(notice).toContain('class="update-running" role="status"');
    expect(notice).toContain('update-running-spin');
    expect(notice).toContain('Update running, the speaker restarts shortly.');
    const status = fileSource('views/settings/status.js');
    expect(status).toContain("runsHere ? updateRunningNoticeHTML() : ''}<div class=\"op-status\" id=\"stickInfoUpdateStatus\">");
    expect(status).toContain('const runsHere = otaHere || isUpdateRunning(box.host);');
    const update = srcOf('runSingleBoxUpdate');
    expect(update).toContain('setUpdateRunning(targetBox.host, true);');
    expect(update).toContain('setUpdateRunning(state.otaTargetHost, false);');
    expect(update).toContain("el.insertAdjacentHTML('beforebegin', updateRunningNoticeHTML())");
    const all = srcOf('runUpdateAllBoxes');
    expect(all).toContain('setUpdateRunning(p.host, true);');
    expect(all).toContain('setUpdateRunning(b.host, false);');
    expect(all).toContain('clearUpdateRunning();');
  });

  it('is bold, in the accent colour, and its ring stands still without motion', () => {
    const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');
    const rule = (sel) => { const at = css.indexOf(`${sel} {`); return css.slice(at, css.indexOf('}', at)); };
    expect(rule('.update-running')).toMatch(/font-weight: 700;/);
    expect(rule('.update-running')).toMatch(/color: var\(--brand-text\);/);
    expect(rule('.box-updating')).toMatch(/font-weight: 700;/);
    expect(rule('.update-running-spin')).toMatch(/animation: spin/);
    expect(css).toMatch(/prefers-reduced-motion: reduce\) \{\s*\.update-running-spin \{ animation: none;/);
  });
});

describe('the words', () => {
  for (const [code, b] of Object.entries(bundles)) {
    it(`${code} has both, translated, without an em dash`, () => {
      for (const k of ['speaker.updateRunningBadge', 'speaker.updateRunningHint']) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(b[k], `${code}: ${k}`).not.toContain('—');
        if (code !== 'en') expect(b[k], `${code}: ${k}`).not.toBe(bundles.en[k]);
      }
      expect(b['ui2.speakers.list.updating'], code).toBeUndefined();
    });
  }

  it('says it in German the way the request asked', () => {
    expect(bundles.de['speaker.updateRunningBadge']).toBe('Update läuft');
    expect(bundles.de['speaker.updateRunningHint']).toBe('Update läuft, der Lautsprecher startet gleich neu. Bitte so lange nicht bedienen.');
  });
});

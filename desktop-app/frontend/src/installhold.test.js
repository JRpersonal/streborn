// Field case 2026-10-30 (SoundTouch 20 Series I): an update from this app was
// accepted, the speaker was slow to come back, discovery degraded it to "STR
// not running", and the setup picker showed it as "Bose stock firmware" with a
// "first install" badge and "Install STR now" ready. The install guard
// (install_guard.go) refused the click, but the picker had offered precisely
// the action the guard exists for. These pin the part in front of the button:
// a held speaker's card, the network-install card, Speaker settings and the
// speaker list all say an update is finishing and offer no install.
import { describe, it, expect, beforeAll, vi } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { srcOf } from './test/source.js';

vi.mock('./api.js', () => ({}));

const { holdsByHost, holdFor, sameHeldHosts, holdMinutes, stockCardText, heldInstallNote } = await import('./installhold.js');
const { setLocale } = await import('./i18n/index.js');
const { speakerTileState } = await import('./speakertile.js');
const { speakerPillKind, statusPillHTML } = await import('./shell2/pill.js');
const { roomItems } = await import('./shell2/rooms.js');

const here = dirname(fileURLToPath(import.meta.url));
const bundleDir = join(here, 'i18n', 'bundles');
const bundles = Object.fromEntries(readdirSync(bundleDir)
  .filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(join(bundleDir, f), 'utf8'))]));

// What InstallHolds answers for the field case: the update started 10 minutes
// ago and the hold has about 26 minutes left.
const held = { host: '192.0.2.10', sinceSec: 600, remainingSec: 26 * 60 - 5 };

describe('reading InstallHolds', () => {
  it('keys the answer by host and drops anything odd', () => {
    const m = holdsByHost([held, null, { sinceSec: 1 }, { host: '', remainingSec: 9 }]);
    expect(Object.keys(m)).toEqual(['192.0.2.10']);
    expect(holdFor(m, '192.0.2.10')).toBe(held);
    expect(holdFor(m, '192.0.2.11')).toBeNull();
    expect(holdFor(m, '')).toBeNull();
    expect(holdFor(null, '192.0.2.10')).toBeNull();
  });

  it('reads a failed or empty answer as nothing held', () => {
    expect(holdsByHost(null)).toEqual({});
    expect(holdsByHost(undefined)).toEqual({});
    expect(holdsByHost({ host: 'x' })).toEqual({});
    expect(holdFor(holdsByHost(null), 'constructor')).toBeNull();
  });

  it('repaints only when a speaker enters or leaves the hold', () => {
    const a = holdsByHost([held]);
    const later = holdsByHost([{ ...held, remainingSec: 60 }]);
    expect(sameHeldHosts(a, later)).toBe(true);
    expect(sameHeldHosts(a, {})).toBe(false);
    expect(sameHeldHosts({}, undefined)).toBe(true);
  });

  it('words the remaining time in whole minutes, never below two', () => {
    expect(holdMinutes(held)).toBe(26);
    expect(holdMinutes({ remainingSec: 30 })).toBe(2);
    expect(holdMinutes({ remainingSec: 0 })).toBe(2);
    expect(holdMinutes(null)).toBe(2);
  });
});

describe('the setup picker card of a held speaker', () => {
  beforeAll(async () => { await setLocale('en'); });

  it('says an update is finishing, not stock firmware or first install', () => {
    const card = stockCardText(held);
    expect(card.kind).toBe('ST Reborn is finishing an update');
    expect(card.badge).toBe('update finishing');
    expect(card.kind).not.toMatch(/stock firmware/i);
    expect(card.badge).not.toMatch(/first install/i);
  });

  it('keeps the first-install card for every speaker nothing holds', () => {
    const card = stockCardText(null);
    expect(card.kind).toBe('Bose stock firmware');
    expect(card.badge).toBe('first install');
  });

  it('explains the disabled install button up front', () => {
    const note = heldInstallNote(held);
    expect(note).toContain('still finishing');
    expect(note).toContain('nothing to install');
    expect(note).toContain('about 26 minutes');
    expect(note).not.toMatch(/USB stick/i);
    expect(heldInstallNote(null)).toBe('');
  });

  it('is what renderSetupTargetPicker draws for a stock speaker', () => {
    const picker = srcOf('renderSetupTargetPicker');
    const stockLoop = picker.slice(picker.indexOf('for (const b of stockBoxes)'));
    expect(stockLoop).toContain('stockCardText(holdFor(state.installHolds, b.host))');
    expect(stockLoop).toContain('boxIdentLine(b, card.kind)');
    expect(stockLoop).toContain('card.badge');
    // The old hard-wired stock wording is gone from the card: it comes only
    // from stockCardText now, which knows about the hold.
    expect(stockLoop.slice(0, stockLoop.indexOf('limitedNote'))).not.toContain("t('setup.targetCardKindStock')");
    // And the picker asks for the holds of the speakers it lists.
    expect(picker).toContain('refreshInstallHolds(stockBoxes.map(b => b.host))');
  });

  it('asks the backend and repaints only on a change', () => {
    const refresh = srcOf('refreshInstallHolds');
    expect(refresh).toContain('await InstallHolds(hosts)');
    expect(refresh).toContain('sameHeldHosts(state.installHolds, next)');
    expect(refresh).toMatch(/if \(changed\) \{\s*renderSetupTargetPicker\(\);/);
  });
});

describe('the network install card of a held speaker', () => {
  it('shows the note above the pitch and disables Install STR now', () => {
    const hero = srcOf('renderPrimaryAction');
    const hold = hero.indexOf('id="setupHeroHold"');
    const pitch = hero.indexOf("t('setup.cardPitch'");
    expect(hold).toBeGreaterThan(0);
    expect(hold).toBeLessThan(pitch);
    // On the first build and on every refresh of an existing card, in place.
    expect(hero.split('syncHeroInstallHold();').length - 1).toBe(2);

    const sync = srcOf('syncHeroInstallHold');
    expect(sync).toContain('heldInstallNote(hold)');
    expect(sync).toContain('note.hidden = !hold');
    expect(sync).toContain('btn.disabled = networkInstallRunning || installWaitPanelLive || !!hold');
  });

  it('is not re-enabled under the hold when an install run ends', () => {
    const start = srcOf('startNetworkInstall');
    expect(start).toContain('b2.disabled = !!currentHeroHold()');
    expect(start).not.toContain('b2.disabled = false');
    const wait = srcOf('renderInstallWaiting');
    expect(wait).toContain('hero.disabled = !!currentHeroHold()');
    expect(wait).not.toContain('hero.disabled = false');
  });
});

describe('Speaker settings and the speaker list during the hold', () => {
  it('replace the reinstall offer with the update-finishing panel', () => {
    const load = srcOf('loadBoxSettings');
    const ask = load.indexOf('await updateHoldOf(asked)');
    const notRunning = load.indexOf('renderSTRNotRunningPanel(body, state.settingsBox, false)');
    const stock = load.indexOf("t('settingsView.stockBoxTitle')");
    expect(ask).toBeGreaterThan(0);
    expect(ask).toBeLessThan(notRunning);
    expect(ask).toBeLessThan(stock);
    // The reconnect loop's give-up asks too.
    expect(load).toContain('if (hold) renderUpdateFinishingPanel(body, state.settingsBox, hold);');

    const panel = srcOf('renderUpdateFinishingPanel');
    expect(panel).toContain("t('settingsView.updateFinishingTitle')");
    expect(panel).not.toContain('openInstallFor');
    expect(panel).not.toContain('settingsInstallAgain');
    expect(panel).not.toContain("switchView('setup')");
  });

  it('do not invite the install from the speaker list', () => {
    const pick = srcOf('pickBox');
    const ask = pick.indexOf('await InstallHolds([box.host])');
    const invite = pick.indexOf("t('speaker.stockConfirmTitle')");
    expect(ask).toBeGreaterThan(0);
    expect(ask).toBeLessThan(invite);
    const branch = pick.slice(ask, invite);
    expect(branch).toContain("showToast(t('speaker.updateFinishingToast'");
    expect(branch).toContain('return;');
  });
});

describe('the speaker list badge of a held speaker', () => {
  beforeAll(async () => { await setLocale('en'); });
  const degraded = { host: '192.0.2.10', port: 8090, deviceID: 'device-id-here', kind: 'stock', strNotRunning: true, friendlyName: 'Kitchen' };
  const holds = holdsByHost([held]);

  it('reads "update finishing", not "STR not running"', () => {
    const tile = speakerTileState(degraded, { installHolds: holds });
    expect(tile.updateFinishing).toBe(true);
    expect(tile.strNotRunning).toBe(false);
    expect(speakerPillKind(tile)).toBe('updateFinishing');
    const pill = statusPillHTML('updateFinishing');
    expect(pill).toContain('update finishing');
    expect(pill).not.toContain('STR not running');
  });

  it('goes back to "STR not running" once the hold is over', () => {
    const tile = speakerTileState(degraded, { installHolds: {} });
    expect(tile.updateFinishing).toBe(false);
    expect(tile.strNotRunning).toBe(true);
    expect(speakerPillKind(tile)).toBe('strNotRunning');
    expect(speakerTileState(degraded).updateFinishing).toBe(false);
  });

  it('says "not reachable" for an offline speaker, whatever the hold', () => {
    const tile = speakerTileState({ ...degraded, offline: true }, { installHolds: holds });
    expect(tile.updateFinishing).toBe(false);
    expect(speakerPillKind(tile)).toBe('notReachable');
  });

  it('never marks a speaker that runs STR', () => {
    const tile = speakerTileState({ ...degraded, kind: 'str', strNotRunning: false }, { installHolds: holds });
    expect(tile.updateFinishing).toBe(false);
  });

  it('gives the sidebar row a tooltip that offers no install', () => {
    const [row] = roomItems([degraded], { installHolds: holds });
    expect(row.pill).toBe('updateFinishing');
    expect(row.title).toContain('still finishing an update');
    expect(row.title).not.toMatch(/click to install/i);
    const [plain] = roomItems([degraded], {});
    expect(plain.pill).toBe('strNotRunning');
    expect(plain.title).toBe('');
  });

  it('is what the Music tab speaker picker draws, from the same holds', () => {
    const select = srcOf('renderBoxSelect');
    expect(select).toContain('installHolds: state.installHolds');
    expect(select).toContain("finishing ? 'speaker.updateFinishingBadge'");
    expect(select).toContain("t(finishing ? 'speaker.updateFinishingTooltip'");
    expect(srcOf('paintSpeakers')).toContain('installHolds: state.installHolds');
    // A speaker entering or leaving the hold repaints both lists.
    expect(srcOf('refreshInstallHolds')).toContain('deps.installHoldsChanged()');
  });

  it('matches the setup card badge and never says click to install', () => {
    for (const [lang, b] of Object.entries(bundles)) {
      expect(b['speaker.updateFinishingBadge'], lang).toBe(b['setup.targetCardBadgeUpdating']);
    }
    expect(bundles.en['speaker.updateFinishingTooltip']).not.toMatch(/click|install STR/i);
  });
});

describe('the hold strings', () => {
  const keys = [
    'setup.targetCardKindUpdating', 'setup.targetCardBadgeUpdating', 'setup.installHeldNote',
    'settingsView.updateFinishingTitle', 'settingsView.updateFinishingHelp', 'speaker.updateFinishingToast',
    'speaker.updateFinishingBadge', 'speaker.updateFinishingTooltip',
  ];

  it('exist in every bundle with their placeholders', () => {
    expect(Object.keys(bundles).length).toBe(13);
    for (const [lang, b] of Object.entries(bundles)) {
      for (const k of keys) expect(b[k], `${lang} ${k}`).toBeTruthy();
      expect(b['setup.installHeldNote'], lang).toContain('{{min}}');
      expect(b['settingsView.updateFinishingHelp'], lang).toContain('{{min}}');
      expect(b['settingsView.updateFinishingHelp'], lang).toContain('{{name}}');
      expect(b['speaker.updateFinishingToast'], lang).toContain('{{label}}');
    }
  });

  it('use no dashes as punctuation', () => {
    for (const [lang, b] of Object.entries(bundles)) {
      for (const k of keys) expect(b[k], `${lang} ${k}`).not.toMatch(/ [-–—] |—|–/);
    }
  });

  it('never send the owner to the stick while the update finishes', () => {
    for (const k of keys) expect(bundles.en[k]).not.toMatch(/USB stick/i);
  });

  it('address German readers informally', () => {
    const de = bundles.de;
    expect(de['setup.installHeldNote']).toContain('Lass ihn');
    for (const k of keys) expect(de[k]).not.toMatch(/\bSie\b|\bIhr(en)?\b/);
  });
});

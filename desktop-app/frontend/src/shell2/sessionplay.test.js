// Play/Pause on every playing row of the UI 2.0 sidebar: which rows get it,
// what it acts on, what it says, and when a press counts as done.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { rowTransport, rowTransportLabel, transportVerdict, TRANSPORT_SEND_MS, TRANSPORT_WITHIN_MS } from './sessionplay.js';
import { sidebarHTML, sidebarModel } from './speakers.js';
import { stereoPairsOf } from '../groups.js';
import { setLocale } from '../i18n/index.js';

setLocale('en');

const css = readFileSync(new URL('./shell2.css', import.meta.url), 'utf8');

const box = (n, id, name, extra = {}) => ({ host: `192.0.2.${n}`, port: 8888, deviceID: id, kind: 'STR', friendlyName: name, ...extra });
const M = box(20, 'MMM', 'Living room');
const F = box(21, 'FFF', 'Kitchen');
const P1 = box(22, 'P1P', 'Terrace left');
const P2 = box(23, 'P2P', 'Terrace right');
const O = box(24, 'OOO', 'Garage', { offline: true });
const S = box(25, 'SSS', 'Bedroom', { kind: 'stock', port: 8090 });
const I1 = box(26, 'II1', 'Office');
const I2 = box(27, 'II2', 'Hall');
const NET = [M, F, P1, P2, O, S, I1, I2];

// Living room leads Kitchen; Terrace is a stereo pair; Office and Hall alone.
const ZONES = {
  MMM: { master: 'MMM', members: [{ deviceID: 'MMM', ip: M.host }, { deviceID: 'FFF', ip: F.host }] },
  FFF: { master: 'MMM' },
  P1P: { stereo: { masterDeviceID: 'P1P', name: 'Terrace', members: [{ deviceID: 'P1P', role: 'LEFT' }, { deviceID: 'P2P', role: 'RIGHT' }] } },
  P2P: {},
  II1: {},
  II2: {},
};
const PLAYING = { MMM: true, FFF: true, P1P: true, P2P: true, II1: true, II2: false };

function model(over = {}) {
  const zoneLive = over.zoneLive || ZONES;
  return sidebarModel(over.boxes || NET, {
    currentBox: I2,
    zoneLive,
    stereoPairs: stereoPairsOf(zoneLive),
    boxPlaying: PLAYING,
    boxPaused: {},
    pairName: (p) => p.name || '',
    ...over,
  });
}
const rows = (m) => [...m.pinned, ...m.others];
const byHost = (m, b) => rows(m).find((r) => r.hosts.includes(b.host));

describe('which sidebar rows get Play/Pause', () => {
  it('every playing session: a single speaker, the group master, a stereo pair', () => {
    const m = model();
    expect(byHost(m, I1)).toMatchObject({ transport: 'pause', transportGroup: false });
    expect(byHost(m, M)).toMatchObject({ transport: 'pause', transportGroup: true });
    const pair = byHost(m, P1);
    expect(pair.kind).toBe('pair');
    // The pair's other half does not make it a group: the pair is one row.
    expect(pair).toMatchObject({ transport: 'pause', transportGroup: false, host: P1.host });
  });

  it('never on a follower: a group is paused through its master (#70)', () => {
    const m = model();
    expect(byHost(m, F).playing).toBe(true);
    expect(byHost(m, F).transport).toBeUndefined();
    expect(sidebarHTML(m)).not.toMatch(/ui2-spk-tp[^>]*data-host="192\.0\.2\.21"/);
  });

  it('none on an idle, offline or Bose original row', () => {
    const m = model({ boxPlaying: { ...PLAYING, SSS: true, OOO: true } });
    for (const b of [I2, O, S]) expect(byHost(m, b).transport, b.friendlyName).toBeUndefined();
  });

  it('a paused speaker offers Play, so its row keeps the way back to the music', () => {
    const m = model({ boxPlaying: { ...PLAYING, II1: false }, boxPaused: { II1: true } });
    expect(byHost(m, I1)).toMatchObject({ transport: 'play' });
    // A paused group master: Play for the whole group.
    const g = model({ boxPlaying: { ...PLAYING, MMM: false, FFF: false }, boxPaused: { MMM: true } });
    expect(byHost(g, M)).toMatchObject({ transport: 'play', transportGroup: true });
  });

  it('reads the speaker you control from the status poll, not the slower every-speaker poll', () => {
    const cur = model({ currentBox: I1, boxPlaying: { ...PLAYING, II1: false }, boxPaused: {}, nowPlayState: 'PAUSE_STATE', nowSource: 'UPNP' });
    expect(byHost(cur, I1).transport).toBe('play');
    // Paused in standby is asleep: nothing to resume from the row.
    const asleep = model({ currentBox: I1, boxPlaying: { ...PLAYING, II1: false }, boxPaused: { II1: true }, nowPlayState: 'PAUSE_STATE', nowSource: 'STANDBY' });
    expect(byHost(asleep, I1).transport).toBeUndefined();
  });

  it('leaves the speaker you control alone: the model still marks the same row current', () => {
    const m = model({ currentBox: I2 });
    expect(rows(m).filter((r) => r.isCurrent).map((r) => r.host)).toEqual([I2.host]);
  });

  it('rowTransport is safe on missing input', () => {
    expect(rowTransport(null)).toEqual({ kind: '', group: false });
    expect(rowTransport({ host: 'x', hosts: ['x'], playing: true }, {})).toEqual({ kind: '', group: false });
  });
});

describe('the Play/Pause button', () => {
  it('names the speaker, and on a master the speakers playing along', () => {
    const m = model();
    expect(rowTransportLabel(byHost(m, I1))).toBe('Pause Office');
    expect(rowTransportLabel(byHost(m, M))).toBe('Pause Living room and the speakers playing along');
    expect(rowTransportLabel(byHost(m, P1))).toBe('Pause Terrace (stereo)');
    const p = model({ boxPlaying: { ...PLAYING, II1: false, MMM: false, FFF: false }, boxPaused: { II1: true, MMM: true } });
    expect(rowTransportLabel(byHost(p, I1))).toBe('Play Office');
    expect(rowTransportLabel(byHost(p, M))).toBe('Play Living room and the speakers playing along');
    expect(rowTransportLabel({ name: 'X' })).toBe('');
  });

  it('says it waits while a press waits for the speaker', () => {
    const m = model({ transportPending: new Map([[I1.host, 'pause']]) });
    const r = byHost(m, I1);
    expect(r.transportPending).toBe('pause');
    expect(rowTransportLabel(r)).toBe('Pausing Office...');
    const html = sidebarHTML(m);
    expect(html).toMatch(/class="[^"]*ui2-spk-tp pending"[^>]*data-tp="pause"[^>]*data-host="192\.0\.2\.26"[^>]*aria-busy="true"/);
    const res = model({ boxPlaying: { ...PLAYING, II1: false }, boxPaused: { II1: true }, transportPending: { [I1.host]: 'resume' } });
    expect(rowTransportLabel(byHost(res, I1))).toBe('Resuming Office...');
  });

  it('is its own button next to the name, with the row host and port, in the roving tab order', () => {
    const m = model();
    const html = sidebarHTML(m, { focusHost: I1.host });
    const li = html.split('<li ').find((s) => s.includes(`data-host="${I1.host}"`));
    // Pick first, then Play/Pause: a sibling, never inside the pick button.
    expect(li.indexOf('ui2-spk-pick')).toBeLessThan(li.indexOf('ui2-spk-tp'));
    expect(li).toMatch(/<\/button><button type="button" class="u-btn u-btn--icon ui2-spk-tp"/);
    expect(li).toMatch(/ui2-spk-tp"[^>]*data-port="8888" tabindex="0" aria-label="Pause Office" title="Pause Office"/);
    // Another row's button stays out of Tab, as its pick does.
    const other = html.split('<li ').find((s) => s.includes(`data-host="${M.host}"`));
    expect(other).toMatch(/ui2-spk-tp"[^>]*tabindex="-1"/);
    // The glyph is decoration; the name is the words.
    expect(li).toMatch(/<span class="ui2-spk-tp-glyph" aria-hidden="true"><\/span>/);
  });

  it('shows on hover and keyboard focus, always on a touch screen, and stays while it waits', () => {
    expect(css).toMatch(/@media not \(pointer: coarse\) \{\s*html\.ui2 \.ui2-spk-row:not\(:hover, :focus-within\) > \.ui2-spk-tp:not\(\.pending\) \{ opacity: 0; \}/);
    // Hidden by opacity only: display or visibility would take it from the keyboard.
    const rules = css.match(/[^}]*\.ui2-spk-tp[^{]*\{[^}]*\}/g) || [];
    expect(rules.length).toBeGreaterThan(3);
    for (const r of rules) expect(r).not.toMatch(/display:\s*none|visibility:\s*hidden/);
    expect(css).toMatch(/\.ui2-spk-tp\[data-tp="play"\] \.ui2-spk-tp-glyph/);
  });
});

describe('when a press counts as done', () => {
  const now = 1_000_000;
  const row = (transport, readAt) => ({ host: 'h', kind: 'str', transport, readAt });
  const sent = (want, sentAt = now + 300) => ({ want, at: now, sent: true, sentAt });
  it('a pause once the row offers Play, a resume once it plays again, only after the binding answered', () => {
    expect(transportVerdict(sent('pause'), row('play'), now + 900)).toBe('confirmed');
    expect(transportVerdict(sent('pause'), row('pause'), now + 900)).toBe('wait');
    expect(transportVerdict({ want: 'pause', at: now, sent: false }, row('play'), now + 900)).toBe('wait');
    expect(transportVerdict(sent('resume'), row('pause'), now + 900)).toBe('confirmed');
    expect(transportVerdict(sent('resume'), row('play'), now + 900)).toBe('wait');
  });

  it('a row without its Pause confirms a pause only through a read that worked and started after the press was sent', () => {
    // The Status read failed (no read stamp) or is older than the answer:
    // "not playing" says nothing, the speaker may well play on.
    expect(transportVerdict(sent('pause'), row(undefined), now + 900)).toBe('wait');
    expect(transportVerdict(sent('pause'), row(undefined, now + 100), now + 900)).toBe('wait');
    // A good read after the answer: stopped is stopped (radio has no pause).
    expect(transportVerdict(sent('pause'), row(undefined, now + 300), now + 900)).toBe('confirmed');
    expect(transportVerdict(sent('pause'), row(undefined, now + 1100), now + 1500)).toBe('confirmed');
    // Still playing on a good read: no confirmation.
    expect(transportVerdict(sent('pause'), row('pause', now + 1100), now + 1500)).toBe('wait');
  });

  it('a slow binding is waited for, and the confirm window counts from its answer', () => {
    // Resume may take 15 s on its own (app_boxops.go resumeCallTimeout).
    const slow = { want: 'resume', at: now, sent: false };
    expect(transportVerdict(slow, row('play'), now + TRANSPORT_WITHIN_MS + 100)).toBe('wait');
    expect(transportVerdict(slow, row('play'), now + TRANSPORT_SEND_MS + 1)).toBe('timeout');
    expect(TRANSPORT_SEND_MS).toBeGreaterThan(15000);
    // Sent late, at +14.5 s: past the old deadline it still waits, and the
    // speaker's report then confirms it.
    const late = sent('resume', now + 14_500);
    expect(transportVerdict(late, row('play'), now + TRANSPORT_WITHIN_MS + 100)).toBe('wait');
    expect(transportVerdict(late, row('pause'), now + 15_300)).toBe('confirmed');
    expect(transportVerdict(late, row('play'), now + 14_500 + TRANSPORT_WITHIN_MS + 1)).toBe('timeout');
  });

  it('a press refused after the old 15 s deadline is still waiting, so its refusal lands in the row', () => {
    // The binding has not answered at +15.1 s: the press is not settled as
    // "not confirmed", and the refusal that follows belongs to it.
    expect(transportVerdict({ want: 'resume', at: now, sent: false }, row('play'), now + 15_100)).toBe('wait');
  });

  it('gives up after the deadline, and drops a row that left or went offline', () => {
    expect(transportVerdict({ want: 'pause', at: now, sent: true }, row('pause'), now + TRANSPORT_WITHIN_MS + 1)).toBe('timeout');
    expect(transportVerdict({ want: 'pause', at: now, sent: true }, null, now)).toBe('gone');
    expect(transportVerdict({ want: 'pause', at: now, sent: true }, { ...row('pause'), offline: true }, now)).toBe('gone');
    expect(transportVerdict(null, row('pause'), now)).toBe('gone');
  });
});

describe('a stereo pair whose master is offline', () => {
  it('gets no Play/Pause: the half that answers does not own the pair\'s transport', () => {
    const boxes = NET.map((b) => (b === P1 ? { ...P1, offline: true } : b));
    const m = model({ boxes });
    const pair = byHost(m, P2);
    expect(pair.kind).toBe('pair');
    // The row picks the half that is there, as before.
    expect(pair.host).toBe(P2.host);
    expect(pair.offline).toBe(false);
    expect(pair.transport).toBeUndefined();
    expect(sidebarHTML(m)).not.toMatch(/ui2-spk-tp[^>]*data-host="192\.0\.2\.23"/);
  });

  it('rowTransport refuses a pair row aimed at anything but its master', () => {
    const ctx = { boxes: NET, zoneLive: ZONES };
    const r = { host: P2.host, hosts: [P1.host, P2.host], kind: 'pair', playing: true };
    expect(rowTransport({ ...r, pairMaster: '' }, ctx).kind).toBe('');
    expect(rowTransport({ ...r, pairMaster: P1.host }, ctx).kind).toBe('');
    expect(rowTransport({ ...r, host: P1.host, pairMaster: P1.host }, ctx).kind).toBe('pause');
  });
});

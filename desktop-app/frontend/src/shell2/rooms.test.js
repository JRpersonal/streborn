import { describe, it, expect } from 'vitest';
import { roomButtonLabel, roomItems, roomSwitcherHTML } from './rooms.js';
import en from '../i18n/bundles/en.json';
import { setLocale } from '../i18n/index.js';
import { shellMarkup } from './markup.js';

// The assertions compare with the English bundle, whatever the machine's language.
setLocale('en');

const A = { host: '192.0.2.10', port: 8888, deviceID: 'AAA', kind: 'STR', friendlyName: 'Kitchen' };
const B = { host: '192.0.2.11', port: 8888, deviceID: 'BBB', kind: 'STR', friendlyName: 'Office' };
const C = { host: '192.0.2.12', port: 8888, deviceID: 'CCC', kind: 'STR', friendlyName: 'Hall' };

const html = (boxes, ctx = {}) => roomSwitcherHTML({ items: roomItems(boxes, ctx) });

describe('room switcher', () => {
  it('presses the current speaker and only that one', () => {
    const out = html([A, B], { currentBox: B });
    expect(out).toMatch(/data-host="192\.0\.2\.11"[^>]*aria-pressed="true"/);
    expect(out).toMatch(/data-host="192\.0\.2\.10"[^>]*aria-pressed="false"/);
    expect(out.match(/aria-pressed="true"/g)).toHaveLength(1);
  });

  it('shows offline, stock and STR-not-running speakers with their pills', () => {
    const items = roomItems([
      { ...A, offline: true },
      { ...B, kind: 'stock' },
      { ...C, kind: 'stock', strNotRunning: true },
    ], { offlineTitle: () => 'Offline, last seen 5 min ago' });
    expect(items.map((i) => i.pill)).toEqual(['notReachable', 'boseOriginal', 'strNotRunning']);
    expect(items[0].title).toBe('Offline, last seen 5 min ago');
    const out = roomSwitcherHTML({ items });
    expect(out).toContain(en['ui2.pill.notReachable']);
    expect(out).toContain(en['speaker.needsInstallBadge']);
    expect(out).toContain(en['speaker.strNotRunningBadge']);
  });

  it('escapes speaker names', () => {
    const out = html([{ ...A, friendlyName: '<img src=x onerror=alert(1)>' }]);
    expect(out).not.toContain('<img');
    expect(out).toContain('&lt;img');
  });

  it('explains an empty list, and offers to add a speaker outside the repainted list', () => {
    const searching = roomSwitcherHTML({ items: [], searching: true });
    expect(searching).toContain(en['speaker.searching']);
    const none = roomSwitcherHTML({ items: [] });
    expect(none).toContain(en['ui2.rooms.none']);
    // The Add button is fixed markup below #ui2RoomList, so a live repaint of
    // the list never replaces it while it has keyboard focus.
    expect(none).not.toContain('ui2RoomAdd');
    const shell = shellMarkup();
    const list = shell.indexOf('id="ui2RoomList"');
    const add = shell.indexOf('id="ui2RoomAdd"');
    expect(list).toBeGreaterThan(0);
    expect(add).toBeGreaterThan(list);
    expect(shell.slice(add)).toContain(en['nav.setupStick']);
  });

  it('collapses a live stereo pair into one row led by its master', () => {
    const pair = { id: 'p1', master: 'BBB', members: [{ deviceID: 'AAA', role: 'LEFT' }, { deviceID: 'BBB', role: 'RIGHT' }] };
    const items = roomItems([A, B, C], { stereoPairs: [pair], currentBox: A });
    expect(items).toHaveLength(2);
    expect(items[0].host).toBe(B.host);
    expect(items[0].name).toBe('Kitchen + Office');
    expect(items[0].pill).toBe('stereoPair');
    // Selecting either half marks the pair row as current.
    expect(items[0].isCurrent).toBe(true);
    const named = roomItems([A, B], { stereoPairs: [pair], pairName: () => 'Living pair' });
    expect(named[0].name).toBe('Living pair');
  });

  it('names the controlled speaker and its state in the button label', () => {
    const [item] = roomItems([A], { currentBox: A, nowPlayState: 'PLAY_STATE' });
    expect(roomButtonLabel(item)).toBe(`Controlling Kitchen, ${en['speaker.playingNow']}. Change speaker`);
    expect(roomButtonLabel(null)).toBe(en['ui2.rooms.none']);
  });

  it('names every chip the button shows, not only the pill', () => {
    const [item] = roomItems([A], {
      currentBox: A,
      nowPlayState: 'PLAY_STATE',
      needsUpdate: () => true,
      cannotReachInternet: () => true,
    });
    expect(item.chips).toEqual(['update', 'noInternet']);
    expect(roomButtonLabel(item)).toBe(
      `Controlling Kitchen, ${en['speaker.playingNow']}, ${en['speaker.updateChip']}, ${en['speaker.noInternetChip']}. Change speaker`,
    );
  });

  it('reads an idle speaker without a state slot (no stray dash)', () => {
    const [item] = roomItems([A], { currentBox: A });
    expect(item.pill).toBe('');
    expect(item.chips).toEqual([]);
    const name = roomButtonLabel(item);
    expect(name).toBe('Controlling Kitchen. Change speaker');
    expect(name).not.toContain('-');
  });
});

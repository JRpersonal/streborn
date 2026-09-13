// The line inputs the music tab offers, built from the speaker's own source
// list.
//
// Four owners could not reach their sockets from the app because the row held
// two fixed buttons, AUX and Bluetooth: a SoundTouch 300 with TV and HDMI
// (discussion #385 item 1), an SA-5 whose three line inputs all arrive as
// source AUX (#274), a CineMate 130 with TV, CBL-Sat, BD-DVD, Game and Aux
// (#491), and discussion #577. The CineMate owner reached for the Bose infrared
// remote to get his television sound back.
import { describe, it, expect, beforeAll } from 'vitest';
import { readFileSync } from 'node:fs';
import { inputButtons, isActiveInput, physicalInputs, sourceRowKey } from './sourceinputs.js';
import { setLocale } from './i18n/index.js';
import en from './i18n/bundles/en.json';

// The label of an input the speaker does not name itself comes from the bundle,
// so pin the locale rather than inherit the test machine's.
beforeAll(() => { setLocale('en'); });

// Line endings normalised: the sources are checked out with CRLF on some
// machines and the slices below key on a bare newline.
const mainJS = readFileSync(new URL('./main.js', import.meta.url), 'utf8').replace(/\r\n/g, '\n');

const src = (source, sourceAccount, status, isLocal, displayName = '') =>
  ({ source, sourceAccount, status, isLocal, displayName });

// Verbatim shapes from the boxes: a Portable, a SoundTouch 10 and a
// SoundTouch 30 (captured 2026-08-09). The capture kept the attributes, not the
// element text, so every displayName here is left empty and the labels below
// come from the bundle, which is the case a one-piece speaker exercises.
const SOUNDTOUCH_10 = [
  src('AUX', 'AUX', 'READY', true, ''),
  src('BLUETOOTH', '', 'UNAVAILABLE', true, ''),
  src('QPLAY', 'QPlay1UserName', 'UNAVAILABLE', true, ''),
  src('QPLAY', 'QPlay2UserName', 'UNAVAILABLE', true, ''),
  src('UPNP', 'UPnPUserName', 'UNAVAILABLE', false, ''),
  src('SPOTIFY', 'SpotifyConnectUserName', 'UNAVAILABLE', false, ''),
  src('STORED_MUSIC_MEDIA_RENDERER', 'StoredMusicUserName', 'UNAVAILABLE', false, ''),
  src('LOCAL_INTERNET_RADIO', '', 'READY', false, ''),
  src('AIRPLAY', '', 'READY', false, ''),
  src('ALEXA', '', 'READY', false, ''),
  src('NOTIFICATION', '', 'UNAVAILABLE', false, ''),
];

describe('the inputs a speaker offers', () => {
  it('offers the analogue socket and Bluetooth on a SoundTouch 10', () => {
    const labels = inputButtons(SOUNDTOUCH_10, 'SoundTouch 10').map((b) => b.label);
    expect(labels).toEqual([en['source.label.aux'], 'Bluetooth']);
  });

  it('keeps Bluetooth with nothing paired to it', () => {
    // /sources status is a CONNECTION indicator: the ST10's Bluetooth reports
    // UNAVAILABLE because no phone is paired, and hiding it on that basis takes
    // away the input the owner wants to use.
    const bt = inputButtons(SOUNDTOUCH_10, 'SoundTouch 10').find((b) => b.bluetooth);
    expect(bt).toBeTruthy();
    expect(bt.source).toBe('BLUETOOTH');
  });

  it('offers no service or protocol slot as an input', () => {
    const sources = inputButtons(SOUNDTOUCH_10, 'SoundTouch 10').map((b) => b.source);
    for (const notAnInput of ['QPLAY', 'UPNP', 'SPOTIFY', 'STORED_MUSIC_MEDIA_RENDERER',
      'LOCAL_INTERNET_RADIO', 'AIRPLAY', 'ALEXA', 'NOTIFICATION']) {
      expect(sources).not.toContain(notAnInput);
    }
  });

  it('offers a CineMate its TV sound back, under the name the box uses (#491)', () => {
    // A CineMate 130 reports its analogue input as LOCAL and its sockets as
    // PRODUCT, with the television off, so every one of them is UNAVAILABLE.
    const cinemate = [
      src('PRODUCT', 'TV', 'UNAVAILABLE', true, 'TV'),
      src('PRODUCT', 'CBL-Sat', 'UNAVAILABLE', true, 'CBL-Sat'),
      src('PRODUCT', 'BD-DVD', 'UNAVAILABLE', true, 'BD-DVD'),
      src('PRODUCT', 'Game', 'UNAVAILABLE', true, 'Game'),
      src('LOCAL', '', 'READY', true, ''),
      src('UPNP', 'UPnPUserName', 'UNAVAILABLE', false, ''),
    ];
    const buttons = inputButtons(cinemate, 'CineMate 130');
    expect(buttons.map((b) => b.label)).toEqual(['TV', 'CBL-Sat', 'BD-DVD', 'Game', en['source.label.aux']]);
    // The name that goes back to the speaker is the speaker's own: LOCAL here,
    // and the socket name in the account for the four PRODUCT entries.
    expect(buttons[0]).toMatchObject({ source: 'PRODUCT', sourceAccount: 'TV' });
    expect(buttons[4]).toMatchObject({ source: 'LOCAL', sourceAccount: '' });
  });

  it('tells an SA-5 three line inputs apart (#274)', () => {
    // All three arrive as source AUX with no name of their own, so the account
    // is the only thing that distinguishes them.
    const sa5 = [
      src('AUX', 'AUX1', 'READY', true, ''),
      src('AUX', 'AUX2', 'READY', true, ''),
      src('AUX', 'AUX3', 'READY', true, ''),
    ];
    const buttons = inputButtons(sa5, 'SA-5');
    expect(buttons).toHaveLength(3);
    expect(new Set(buttons.map((b) => b.label)).size).toBe(3);
    for (const b of buttons) expect(b.label).toContain(en['source.label.aux']);
    expect(buttons.map((b) => b.sourceAccount)).toEqual(['AUX1', 'AUX2', 'AUX3']);
  });

  it('does not repeat a socket name the speaker already gave', () => {
    // A soundbar names its sockets, so the account must not be appended as well:
    // "TV (TV)" is what naming every socket unconditionally produced.
    const named = [
      src('PRODUCT', 'TV', 'READY', true, 'TV'),
      src('PRODUCT', 'HDMI', 'READY', true, 'HDMI'),
    ];
    expect(inputButtons(named, 'SoundTouch 300').map((b) => b.label)).toEqual(['TV', 'HDMI']);
  });

  it('keeps the classic two buttons when the speaker answers nothing', () => {
    // A speaker too slow to answer /sources must not lose the inputs it has
    // always had.
    expect(inputButtons([], 'SoundTouch 20').map((b) => b.source)).toEqual(['AUX', 'BLUETOOTH']);
    expect(inputButtons(undefined, 'SoundTouch 20').map((b) => b.source)).toEqual(['AUX', 'BLUETOOTH']);
    // The Portable has no Bluetooth hardware, the Wave's own Aux is not
    // reachable through the SoundTouch pedestal (#417).
    expect(inputButtons([], 'SoundTouch Portable').map((b) => b.source)).toEqual(['AUX']);
    expect(inputButtons([], 'Wave SoundTouch music system IV').map((b) => b.source)).toEqual(['BLUETOOTH']);
  });

  it('offers nothing when the speaker answers with no socket at all', () => {
    // The Wave's own Aux is not reachable through the SoundTouch pedestal
    // (#417), and a button for it would be a button that cannot work. A list
    // that arrived and named no socket is an answer, not a missing answer.
    const services = [
      src('UPNP', 'UPnPUserName', 'READY', false, ''),
      src('LOCAL_INTERNET_RADIO', '', 'READY', false, ''),
    ];
    expect(physicalInputs(services)).toEqual([]);
    expect(inputButtons(services, 'Wave SoundTouch music system IV')).toEqual([]);
  });
});

describe('which input button lights up', () => {
  it('lights the analogue button whichever name the speaker uses (#491)', () => {
    expect(isActiveInput({ source: 'LOCAL', sourceAccount: '' }, 'AUX', 'AUX')).toBe(true);
    expect(isActiveInput({ source: 'AUX', sourceAccount: 'AUX' }, 'LOCAL', '')).toBe(true);
  });

  it('lights the one AUX socket that is playing on an SA-5', () => {
    const second = { source: 'AUX', sourceAccount: 'AUX2' };
    const third = { source: 'AUX', sourceAccount: 'AUX3' };
    expect(isActiveInput(second, 'AUX', 'AUX2')).toBe(true);
    expect(isActiveInput(third, 'AUX', 'AUX2')).toBe(false);
  });

  it('still lights a single socket when the speaker names no account', () => {
    expect(isActiveInput({ source: 'BLUETOOTH', sourceAccount: '' }, 'BLUETOOTH', '')).toBe(true);
    expect(isActiveInput({ source: 'PRODUCT', sourceAccount: 'TV' }, 'PRODUCT', '')).toBe(true);
  });

  it('leaves every input dark while STR itself is playing', () => {
    expect(isActiveInput({ source: 'AUX', sourceAccount: 'AUX' }, 'UPNP', 'UPnPUserName')).toBe(false);
    expect(isActiveInput({ source: 'STANDBY', sourceAccount: '' }, 'UPNP', '')).toBe(false);
  });

  it('lights standby, which shares the row', () => {
    expect(isActiveInput({ source: 'STANDBY', sourceAccount: '' }, 'STANDBY', '')).toBe(true);
  });
});

describe('the music tab wiring', () => {
  it('no longer ships a fixed AUX and Bluetooth pair', () => {
    expect(mainJS).not.toContain('data-source="AUX"');
    expect(mainJS).not.toContain('data-source="BLUETOOTH"');
  });

  it('builds the row from the speaker\'s own list', () => {
    expect(mainJS).toContain('renderSourceButtons(inputButtons(sources, box.model');
  });

  it('redraws the row when the speaker changes', () => {
    // A button an input refused with 1005 is hidden on that button, so the row
    // must be rebuilt for the next speaker (#417: it stayed hidden for every box
    // until the app was restarted). An unchanged row on the same speaker is left
    // alone, so a pressed button is not replaced under the user's finger.
    const buttons = inputButtons(SOUNDTOUCH_10, 'SoundTouch 10');
    expect(sourceRowKey('192.0.2.31', buttons)).toBe(sourceRowKey('192.0.2.31', buttons));
    expect(sourceRowKey('192.0.2.31', buttons)).not.toBe(sourceRowKey('192.0.2.58', buttons));
  });

  it('sends the account with the source, so a socket can be identified', () => {
    expect(mainJS).toContain('SelectBoxSource(box.host, box.port, src, account)');
  });
});

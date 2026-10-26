// Discussion #1526: forming a pair or group out of standby wakes the master,
// the firmware resumes its last station and the quiet wake stops it again. The
// master is then on, on that station, in STOP_STATE. The app drew that as
// "paused on key 1": pause bars, green text, no state word, the song title from
// the two-second resume and a lit preset key. It is "stopped".
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { isAnalogueInput, keepsLiveTitle, nowStateGlyph, nowStateLabel, presetMayLight } from './stoppedstate.js';
import { titleSwapOffered } from './titleswap.js';
import { srcOf } from './test/source.js';

describe('nowStateLabel', () => {
  it('says stopped for a stopped speaker on a real source', () => {
    expect(nowStateLabel('STOP_STATE', 'LOCAL_INTERNET_RADIO')).toEqual({ key: 'status.stopped', cls: 'idle' });
    expect(nowStateLabel('STOP_STATE', 'TUNEIN')).toEqual({ key: 'status.stopped', cls: 'idle' });
  });

  it('says stopped for a stopped Bluetooth or AirPlay stream', () => {
    // Digital streams the firmware sees end: a selected BLUETOOTH source in
    // STOP_STATE is a real case (internal/webui/stereo_backtostandby_test.go).
    expect(nowStateLabel('STOP_STATE', 'BLUETOOTH')).toEqual({ key: 'status.stopped', cls: 'idle' });
    expect(nowStateLabel('STOP_STATE', 'AIRPLAY')).toEqual({ key: 'status.stopped', cls: 'idle' });
  });

  it('leaves an analogue input in STOP_STATE to the bar\'s "active" line', () => {
    // AUX and LOCAL (a Wave's CD, radio or Aux) cannot report whether sound
    // flows, so STOP_STATE there must not read "stopped". No word here; the
    // bar's AUX/LOCAL branch fills in "active" as it always did.
    expect(nowStateLabel('STOP_STATE', 'AUX')).toEqual({ key: '', cls: 'idle' });
    expect(nowStateLabel('STOP_STATE', 'LOCAL')).toEqual({ key: '', cls: 'idle' });
    expect(isAnalogueInput('AUX')).toBe(true);
    expect(isAnalogueInput('LOCAL')).toBe(true);
    expect(isAnalogueInput('BLUETOOTH')).toBe(false);
    expect(isAnalogueInput('LOCAL_INTERNET_RADIO')).toBe(false);
  });

  it('never says standby for an awake master', () => {
    expect(nowStateLabel('STOP_STATE', 'LOCAL_INTERNET_RADIO').key).not.toBe('status.standby');
  });

  it('keeps the older answers for every other state', () => {
    expect(nowStateLabel('PLAY_STATE', 'LOCAL_INTERNET_RADIO')).toEqual({ key: 'status.playing', cls: 'play' });
    expect(nowStateLabel('BUFFERING_STATE', 'SPOTIFY')).toEqual({ key: 'status.buffering', cls: 'buf' });
    expect(nowStateLabel('PAUSE_STATE', 'UPNP')).toEqual({ key: 'status.paused', cls: 'idle' });
    expect(nowStateLabel('', 'STANDBY')).toEqual({ key: 'status.standby', cls: 'idle' });
    expect(nowStateLabel('STOP_STATE', 'STANDBY')).toEqual({ key: 'status.standby', cls: 'idle' });
    // Nothing selected: no word here, the bar's own "stopped" fallback speaks.
    expect(nowStateLabel('STOP_STATE', 'INVALID_SOURCE')).toEqual({ key: '', cls: 'idle' });
    expect(nowStateLabel('STOP_STATE', '')).toEqual({ key: '', cls: 'idle' });
    expect(nowStateLabel('', 'AUX')).toEqual({ key: '', cls: 'idle' });
  });
});

describe('nowStateGlyph', () => {
  it('draws a stop square for stopped and pause bars for paused only', () => {
    expect(nowStateGlyph('STOP_STATE')).toBe('&#9209;');
    expect(nowStateGlyph('PAUSE_STATE')).toBe('&#9208;');
    expect(nowStateGlyph('PLAY_STATE')).toBe('&#9205;');
    expect(nowStateGlyph('BUFFERING_STATE')).toBe('&#9205;');
    expect(nowStateGlyph('STOP_STATE', 'BLUETOOTH')).toBe('&#9209;');
  });

  it('keeps the play arrow for an analogue input that reads "active"', () => {
    expect(nowStateGlyph('STOP_STATE', 'AUX')).toBe('&#9205;');
    expect(nowStateGlyph('STOP_STATE', 'LOCAL')).toBe('&#9205;');
  });
});

describe('keepsLiveTitle', () => {
  it('drops the title once the speaker reports a stop', () => {
    expect(keepsLiveTitle('STOP_STATE')).toBe(false);
    expect(keepsLiveTitle('INVALID_SOURCE')).toBe(false);
  });

  it('keeps it while playing, starting or paused, and when no state is reported', () => {
    for (const ps of ['PLAY_STATE', 'BUFFERING_STATE', 'PAUSE_STATE', '', undefined]) {
      expect(keepsLiveTitle(ps), String(ps)).toBe(true);
    }
  });

  it('with the title gone the swap row is not offered either', () => {
    // refreshStatus empties nowTitle on a stop; the row gates on it.
    expect(titleSwapOffered(true, '')).toBe(false);
  });
});

describe('presetMayLight', () => {
  it('does not light the selected station of a stopped speaker', () => {
    expect(presetMayLight('STOP_STATE', 0, 1000)).toBe(false);
    expect(presetMayLight('STOP_STATE', 900, 1000)).toBe(false);
  });

  it('still lights a key at once inside its click window', () => {
    expect(presetMayLight('STOP_STATE', 7000, 1000)).toBe(true);
  });

  it('changes nothing for any other state', () => {
    for (const ps of ['PLAY_STATE', 'BUFFERING_STATE', 'PAUSE_STATE', '']) {
      expect(presetMayLight(ps, 0, 1000), ps).toBe(true);
    }
  });
});

describe('wiring', () => {
  it('the now-playing bar takes its word and glyph from these helpers', () => {
    const bar = srcOf('renderNowPlayingBar');
    expect(bar).toContain('nowStateLabel(ps, srcU)');
    expect(bar).toContain('nowStateGlyph(ps, srcU)');
    // The Play/Pause button still treats stopped like paused: Play resumes.
    expect(bar).toContain('stoppedOrPaused(ps)');
  });

  it('the AUX/LOCAL branch still fills in "active" when no word was picked', () => {
    const bar = srcOf('renderNowPlayingBar');
    expect(bar).toMatch(/if \(srcU === 'AUX' \|\| srcU === 'LOCAL'\) \{[^\n]*if \(!stateLabel\) \{ stateLabel = t\('status\.active'\); stateClass = 'play'; \}/);
  });

  it('refreshStatus drops the title on a stop', () => {
    const status = srcOf('refreshStatus');
    const at = status.indexOf('state.nowPlayState = ps;');
    expect(at).toBeGreaterThan(-1);
    const after = status.slice(at, at + 600);
    expect(after).toContain('!keepsLiveTitle(ps)');
    expect(after).toContain("state.nowTitle = '';");
    expect(after).toContain('state.nowTitleSwapped = false;');
  });

  it('the preset grid gates both matches on presetMayLight', () => {
    const grid = srcOf('renderPresets');
    expect(grid).toContain('presetMayLight(state.nowPlayState, state.optimisticUntil, Date.now())');
    expect(grid).toMatch(/const queueLit = mayLight &&/);
    expect(grid).toMatch(/queueLit \|\| \(mayLight && state\.nowLocation/);
  });

  it('a stopped line is not drawn in the playing green', () => {
    const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');
    expect(css).toMatch(/\.status-bar\.status-idle \.now \{ color: var\(--c-muted\); \}/);
  });
});

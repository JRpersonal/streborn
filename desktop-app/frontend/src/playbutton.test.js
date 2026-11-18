// Discussion #1920: on the Bose remote the Play key starts a speaker with
// what it played last. STR's Play/Pause button did nothing on a speaker in
// standby or with nothing loaded. It now wakes the speaker and resumes the
// last item there, in the desktop app (both layouts share the button) and on
// the phone remote, through the power button's existing "on". No new button,
// no new string, and the volume is not touched.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { playButtonAction, playButtonOffersPlay } from './playbutton.js';

const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8').replace(/\r\n/g, '\n');
const phone = read('../../../internal/webui/assets/index.html');

function phoneFn(name) {
  const start = phone.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} not found on the phone page`);
  return phone.slice(start, phone.indexOf('\n}\n', start) + 2);
}

// [what, play state, source, the press]
const cases = [
  ['a station plays', 'PLAY_STATE', 'LOCAL_INTERNET_RADIO', 'pause'],
  ['a stream is starting', 'BUFFERING_STATE', 'UPNP', 'pause'],
  ['a paused library track', 'PAUSE_STATE', 'UPNP', 'resume'],
  ['a stopped stream', 'STOP_STATE', 'UPNP', 'resume'],
  ['a stopped AUX input', 'STOP_STATE', 'AUX', 'resume'],
  // What a speaker in standby reports: the firmware pairs it with STOP_STATE.
  ['standby', 'STOP_STATE', 'STANDBY', 'wake'],
  ['standby with no play state', '', 'STANDBY', 'wake'],
  ['standby in lower case', 'STOP_STATE', 'standby', 'wake'],
  ['awake with nothing loaded', 'STOP_STATE', 'INVALID_SOURCE', 'wake'],
  ['nothing loaded, no transport', 'INVALID_PLAY_STATUS', 'INVALID_SOURCE', 'wake'],
  // Still playing wins over a source the speaker has not updated yet.
  ['playing while the source still reads standby', 'PLAY_STATE', 'STANDBY', 'pause'],
  ['a speaker not heard from yet', '', '', 'pause'],
  ['Bluetooth with no transport', 'INVALID_PLAY_STATUS', 'BLUETOOTH', 'pause'],
];

describe('the press of the Play/Pause button', () => {
  const phoneAction = new Function(`${phoneFn('playButtonAction')}\nreturn playButtonAction;`)();

  for (const [what, ps, src, want] of cases) {
    it(`${what}: ${want}`, () => {
      expect(playButtonAction(ps, src)).toBe(want);
      expect(playButtonOffersPlay(ps, src)).toBe(want !== 'pause');
      // The phone remote keeps the same rule in its own page.
      expect(phoneAction(ps, src)).toBe(want);
    });
  }
});

describe('the desktop app', () => {
  const wiring = read('./views/music/wiring.js');
  const now = read('./views/music/nowplaying.js');
  const sources = read('./views/music/sources.js');

  it('asks the rule on every press and for the word on the button', () => {
    expect(wiring).toContain("$('pauseBtn').onclick = () => action(playButtonAction(state.nowPlayState, state.nowSource));");
    expect(now).toContain('ppBtn.innerHTML = playButtonOffersPlay(ps, src)');
  });

  it('wakes through the power toggle path, with the button held meanwhile', () => {
    const at = now.indexOf('export async function action(kind) {');
    const body = now.slice(at, now.indexOf('\n}\n', at));
    expect(body).toMatch(/if \(kind === 'wake'\) \{\n\s+await powerOnFromPlay\(\$\('pauseBtn'\), \(e, box\) => transportFailed\('resume', e, box\)\);\n\s+return;/);
    expect(sources).toMatch(/export function powerOnFromPlay\(btn, onError\) \{\n\s+return setPower\(true, btn, onError\);/);
    const sp = sources.slice(sources.indexOf('async function setPower('));
    const setPower = sp.slice(0, sp.indexOf('\n}\n'));
    expect(setPower).toContain('await SetBoxPower(box.host, box.port, target);');
    expect(setPower).toContain('if (btn) btn.disabled = true;');
    // Never the volume.
    expect(setPower).not.toMatch(/volume/i);
  });

  it('adds no button to the transport', () => {
    const markup = read('./views/music/markup.js');
    expect((markup.match(/<button class="btn" id="pauseBtn">/g) || []).length).toBe(1);
    expect(markup).not.toMatch(/id="(playBtn|resumeBtn|wakeBtn)"/);
  });
});

describe('the phone remote', () => {
  it('wakes with the power endpoint, once, and leaves the volume alone', () => {
    const fn = phoneFn('togglePlayPause');
    expect(fn).toContain("if (ppAction === 'wake') {");
    expect(fn).toContain("await api('/api/box/power', 'POST', { on: true });");
    expect(fn).toContain('if (ppBusy) return;');
    expect(fn).not.toMatch(/volume/i);
    // A paused or stopped stream still resumes where it was.
    expect(fn).toContain("await postPlay('/api/resume');");
  });

  it('reads the source on every status poll', () => {
    expect(phone).toContain('applyTransportUI(state, src);');
    expect(phoneFn('applyTransportUI')).toContain('ppAction = playButtonAction(state, src);');
  });

  it('keeps the one transport button', () => {
    expect((phone.match(/onclick="togglePlayPause\(this\)"/g) || []).length).toBe(1);
  });
});

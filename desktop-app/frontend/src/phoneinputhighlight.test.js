import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// The phone remote lit an input button only when it was tapped on the phone
// itself. A reporter chose Bluetooth and then AUX in the desktop app: the
// phone's status line said so, and both input buttons stayed grey (#1739).
// The status poll now lights the button of the input the speaker reports.
// These tests lift that code off the served page and run it.

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8');

function lift(signature) {
  const start = page.indexOf(signature);
  expect(start, `${signature} missing from the phone page`).toBeGreaterThan(-1);
  const end = page.indexOf('\n}\n', start);
  return page.slice(start, end + 2);
}

const src = [lift('function inputIsLive('), lift('function paintActiveInput(')].join('\n');

function fakeButton(s, acct) {
  const cls = new Set();
  return {
    dataset: { src: s, acct },
    classList: {
      toggle: (c, on) => (on ? cls.add(c) : cls.delete(c)),
      contains: (c) => cls.has(c),
    },
  };
}

function load(buttons) {
  const document = { querySelectorAll: () => buttons };
  // eslint-disable-next-line no-new-func
  return new Function('document', `
    var liveInput = { src: '', acct: '' };
    var inputTapAt = 0;
    var INPUT_TAP_HOLD_MS = 4000;
    ${src}
    return {
      inputIsLive,
      report: function(s, a) { liveInput = { src: s, acct: a }; paintActiveInput(false); },
      rebuild: function() { paintActiveInput(true); },
      tap: function() { inputTapAt = Date.now(); },
    };`)(document);
}

describe('phone remote: input buttons follow the speaker (#1739)', () => {
  it('lights Bluetooth and then AUX when the switch was made elsewhere', () => {
    const bt = fakeButton('BLUETOOTH', '');
    const aux = fakeButton('AUX', 'AUX');
    const p = load([bt, aux]);
    p.report('BLUETOOTH', '');
    expect(bt.classList.contains('active')).toBe(true);
    expect(aux.classList.contains('active')).toBe(false);
    p.report('AUX', 'AUX');
    expect(bt.classList.contains('active')).toBe(false);
    expect(aux.classList.contains('active')).toBe(true);
  });

  it('lights nothing on radio, Spotify or standby', () => {
    const bt = fakeButton('BLUETOOTH', '');
    const aux = fakeButton('AUX', 'AUX');
    const p = load([bt, aux]);
    p.report('AUX', 'AUX');
    for (const s of ['UPNP', 'SPOTIFY', 'STANDBY', 'INVALID_SOURCE', '']) {
      p.report(s, '');
      expect(bt.classList.contains('active'), s).toBe(false);
      expect(aux.classList.contains('active'), s).toBe(false);
    }
  });

  it("tells a soundbar's sockets apart by the account", () => {
    const tv = fakeButton('PRODUCT', 'TV');
    const hdmi = fakeButton('PRODUCT', 'HDMI_1');
    const p = load([tv, hdmi]);
    p.report('PRODUCT', 'HDMI_1');
    expect(tv.classList.contains('active')).toBe(false);
    expect(hdmi.classList.contains('active')).toBe(true);
    expect(p.inputIsLive('aux', 'aux', 'AUX', 'AUX')).toBe(true);
  });

  it('keeps a fresh tap lit until the speaker has had time to switch', () => {
    const bt = fakeButton('BLUETOOTH', '');
    const aux = fakeButton('AUX', 'AUX');
    const p = load([bt, aux]);
    p.report('AUX', 'AUX');
    p.tap();
    bt.classList.toggle('active', true);
    aux.classList.toggle('active', false);
    p.report('AUX', 'AUX'); // a poll from before the switch took effect
    expect(bt.classList.contains('active')).toBe(true);
    // Rebuilt buttons are painted from the last report regardless of the hold.
    p.rebuild();
    expect(aux.classList.contains('active')).toBe(true);
  });

  it('is fed by the status poll and by every rebuild of the buttons', () => {
    const poll = lift('async function refreshStatus(');
    expect(poll).toContain('liveInput = {');
    expect(poll).toContain('paintActiveInput(false)');
    expect(lift('function renderInputs(')).toContain('paintActiveInput(true)');
    expect(lift('async function setSource(')).toContain('inputTapAt = Date.now()');
  });
});

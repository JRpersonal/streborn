// The stereo balance became settable on 2026-09-10, after a year as a read-out.
//
// The reason it was read-only is worth keeping in mind while reading these
// tests: every write over the firmware's HTTP API hung the endpoint until the
// speaker was woken again, so STR showed the value and pointed at the Bose app.
// gesellix found on #70 that Bose's own client never used HTTP either and sends
// the value on the speaker's WebSocket, which is where it goes now.
//
// What that history buys us is one hard rule for the UI: the write travels over
// a bus that acknowledges nothing, so "sent" and "confirmed" are two different
// states and the control must never show the first as the second. An owner has
// already reported this row as a control that does nothing ("steht neben dem
// Lautstaerkeregler und hat auch keinen Effekt", 2026-08-09); a slider that
// claims a move it cannot prove is the same complaint with an extra step.
//
// The views are read as source, the way the other settings-view tests do it:
// they cannot be imported without a DOM and the Wails runtime.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const settings = readFileSync(new URL('./views/settings.js', import.meta.url), 'utf8');
const multiroom = readFileSync(new URL('./views/multiroom.js', import.meta.url), 'utf8');
const api = readFileSync(new URL('./api.js', import.meta.url), 'utf8');
const en = JSON.parse(readFileSync(new URL('./i18n/bundles/en.json', import.meta.url), 'utf8'));
const de = JSON.parse(readFileSync(new URL('./i18n/bundles/de.json', import.meta.url), 'utf8'));

describe('the balance write', () => {
  it('posts to the balance endpoint and returns the answer', () => {
    const fn = api.slice(api.indexOf('export async function writeBoxBalance'),
                         api.indexOf('export async function writeBoxBalance') + 700);
    expect(fn).toContain("'/api/box/balance'");
    expect(fn).toContain("method: 'POST'");
    expect(fn).toContain('JSON.stringify({ target');
    // A rounded integer: the firmware range is a handful of steps and a
    // fractional slider value has no meaning on it.
    expect(fn).toContain('Math.round');
  });

  // The bounds are the whole reason the read has to come first. A widely-copied
  // community implementation assumes -50..+50 while the firmware answers -7..+7,
  // so a slider built on a constant would be wrong by a factor of seven.
  it('reads the bounds from the speaker, not from a constant', () => {
    const fn = api.slice(api.indexOf('export async function readBoxBalanceInfo'),
                         api.indexOf('// readBoxBalance reads just the current position'));
    expect(fn).toContain('min:');
    expect(fn).toContain('max:');
    expect(fn).toContain('settable:');
    const slider = settings.slice(settings.indexOf('slider.min ='), settings.indexOf('slider.min =') + 200);
    expect(slider).toContain('String(b.min)');
    expect(slider).toContain('String(b.max)');
  });

  it('leaves the read-out alone when the speaker cannot be written to', () => {
    const at = settings.indexOf('if (!b.settable)');
    expect(at, 'the settable gate must exist').toBeGreaterThan(-1);
    const gate = settings.slice(at, at + 160);
    expect(gate).toContain('slider.hidden = true');
    expect(gate).toContain('centre.hidden = true');
  });

  // One write per gesture. oninput fires per pixel of slider travel, and each
  // write puts a frame on the speaker's own bus plus a read-back behind it.
  it('writes on release, not on every drag step', () => {
    const at = settings.indexOf('slider.oninput =');
    expect(at).toBeGreaterThan(-1);
    const input = settings.slice(at, settings.indexOf('slider.onchange ='));
    expect(input, 'dragging may only move the label').not.toContain('applyBalance');
    const change = settings.slice(settings.indexOf('slider.onchange ='),
                                 settings.indexOf('slider.onchange =') + 120);
    expect(change).toContain('applyBalance');
  });

  // The three outcomes, kept apart. Refused is a failure; accepted-but-unseen is
  // not; plainly worked says nothing.
  it('tells a refused write apart from an unconfirmed one', () => {
    const fn = settings.slice(settings.indexOf('async function applyBalance'),
                              settings.indexOf('function setBalanceNote'));
    expect(fn).toContain("res.ok !== true");
    expect(fn).toContain("controls.balanceFailed");
    expect(fn).toContain("res.verified === false");
    expect(fn).toContain("controls.balanceUnverified");
  });

  // The agent clamps to the speaker's range, so what landed can differ from what
  // was asked for. The slider has to follow the speaker, not its own last value.
  it('snaps the slider to the value the speaker actually took', () => {
    const fn = settings.slice(settings.indexOf('async function applyBalance'),
                              settings.indexOf('function setBalanceNote'));
    expect(fn).toContain('res.target');
  });
});

describe('the balance texts', () => {
  it('no longer send people to the Bose app', () => {
    for (const [lang, bundle] of [['en', en], ['de', de]]) {
      expect(bundle['controls.balanceTitle'], lang).not.toMatch(/SoundTouch app|SoundTouch App/);
    }
    // The multiroom read-out points at the control instead of at Bose.
    expect(multiroom).toContain("t('controls.balanceInSettings')");
    expect(multiroom).not.toContain("balanceLabel(v) + '. ' + t('controls.balanceTitle')");
  });

  it('has every new key in English and German', () => {
    for (const k of ['controls.balanceSetTitle', 'controls.balanceCentreBtn',
                     'controls.balanceCentreTitle', 'controls.balanceUnverified',
                     'controls.balanceFailed', 'controls.balanceInSettings']) {
      expect(en[k], `${k} in en`).toBeTruthy();
      expect(de[k], `${k} in de`).toBeTruthy();
    }
  });

  // The standing voice rules, on the strings this change adds: real umlauts, and
  // no dash used as punctuation.
  it('keeps the German strings in the house style', () => {
    const added = Object.keys(de).filter(k => k.startsWith('controls.balance'));
    for (const k of added) {
      const v = de[k];
      expect(v, `${k} uses a dash as punctuation`).not.toMatch(/\s[-–—]\s/);
      expect(v, `${k} has an ASCII umlaut substitute`).not.toMatch(/\b(ae|oe|ue)\b/);
    }
  });
});

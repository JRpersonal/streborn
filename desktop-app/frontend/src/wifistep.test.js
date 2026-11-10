import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { wifiStepOutcome, wifiStepRediscoverDelays, wifiStepLogDetail } from './wifistep.js';

describe('setup Wi-Fi step outcome', () => {
  it('reads "nothing to change" from the agent answer', () => {
    expect(wifiStepOutcome({ status: 'unchanged', unchanged: true, mechanism: 'wpa' })).toBe('unchanged');
    expect(wifiStepOutcome({ status: 'unchanged' })).toBe('unchanged');
  });
  it('reads a cabled store and a real change', () => {
    expect(wifiStepOutcome({ status: 'stored', wiredUplink: true })).toBe('stored');
    expect(wifiStepOutcome({ status: 'switching', mechanism: 'wpa' })).toBe('changed');
    expect(wifiStepOutcome({ status: 'rebooting', mechanism: 'bco' })).toBe('changed');
  });
  it('treats an older agent, or an unreadable answer, as a change', () => {
    expect(wifiStepOutcome({})).toBe('changed');
    expect(wifiStepOutcome(null)).toBe('changed');
  });
});

describe('search again after the Wi-Fi step', () => {
  it('only after a real change', () => {
    expect(wifiStepRediscoverDelays({ status: 'unchanged', unchanged: true })).toEqual([]);
    expect(wifiStepRediscoverDelays({ status: 'stored', wiredUplink: true })).toEqual([]);
  });
  it('waits longer for a speaker that restarts to apply', () => {
    const live = wifiStepRediscoverDelays({ status: 'switching', mechanism: 'wpa' });
    const boot = wifiStepRediscoverDelays({ status: 'rebooting', mechanism: 'bco' });
    expect(live.length).toBe(2);
    expect(boot[0]).toBeGreaterThan(live[0]);
  });
});

describe('setup Wi-Fi step log line', () => {
  it('carries the status and nothing that names the network', () => {
    const d = wifiStepLogDetail({ status: 'switching', mechanism: 'wpa', ssid: 'ExampleNet' });
    expect(d).toBe('status=switching mechanism=wpa');
  });
  it('is written by the install view without the password', () => {
    const src = readFileSync(new URL('./views/setup/install.js', import.meta.url), 'utf8');
    const calls = src.match(/LogSetupWifiStep\([^)]*\)/g) || [];
    expect(calls.length).toBeGreaterThan(0);
    for (const c of calls) expect(c).not.toMatch(/pass/i);
  });
});

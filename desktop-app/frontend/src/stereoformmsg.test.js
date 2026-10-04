import { describe, it, expect } from 'vitest';
import { stereoFormMessage } from './stereoformmsg.js';

describe('stereoFormMessage', () => {
  it('is null for a pairing that formed', () => {
    expect(stereoFormMessage({ ok: true }, () => '')).toBeNull();
    expect(stereoFormMessage(null, () => '')).toBeNull();
  });

  it('names the reason when the agent undid a one-sided pair, even with an error text', () => {
    // The fleet case of 2026-10-04: two SoundTouch 10s that cannot reach each
    // other. The agent sends reason + an English sentence; the reason decides.
    const m = stereoFormMessage({ ok: false, reason: 'partnerUnreachable', error: 'the two speakers could not reach each other' }, () => '');
    expect(m).toEqual({ cls: 'setup-err', key: 'multiroom.pairPartnerUnreachable', params: {} });
    expect(stereoFormMessage({ ok: false, reason: 'partnerDidNotStore' }, () => '').key).toBe('multiroom.pairPartnerDidNotStore');
  });

  it('lists the speakers that were not ready by name, falling back to the address', () => {
    const m = stereoFormMessage({ ok: false, notReady: ['192.0.2.5', '192.0.2.6'] }, ip => (ip === '192.0.2.5' ? 'Küche' : ''));
    expect(m).toEqual({ cls: 'setup-warn', key: 'multiroom.notReady', params: { names: 'Küche, 192.0.2.6' } });
  });

  it('falls back to the plain error text', () => {
    expect(stereoFormMessage({ ok: false, error: 'boom' }, () => '')).toEqual({ cls: 'setup-err', key: 'multiroom.formFailed', params: { err: 'boom' } });
  });
});

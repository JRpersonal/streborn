import { describe, it, expect } from 'vitest';
import { firewallBannerView, firewallUnblockToast, firewallSuspectGate } from './firewallbanner.js';

const t = (k, p) => (p ? `${k}:${JSON.stringify(p)}` : k);
const blocked = { supported: true, checked: true, blocked: true, publicNetwork: false };

describe('firewallBannerView', () => {
  it('shows the banner for a confirmed block, private networks only', () => {
    const v = firewallBannerView(blocked, t);
    expect(v.title).toBe('firewall.bannerTitle');
    expect(v.primary).toEqual({ label: 'firewall.allowButton', includePublic: false });
    expect(v.secondary).toBeNull();
    expect(v.note).toBe('');
  });

  it('asks about Public when the PC is on a public network', () => {
    const v = firewallBannerView({ ...blocked, publicNetwork: true }, t);
    expect(v.note).toBe('firewall.publicNote');
    expect(v.primary.includePublic).toBe(true);
    expect(v.secondary).toEqual({ label: 'firewall.allowPrivateOnly', includePublic: false });
  });

  it('stays down without a confirmed block', () => {
    expect(firewallBannerView(null, t)).toBeNull();
    expect(firewallBannerView({ supported: false }, t)).toBeNull();
    expect(firewallBannerView({ ...blocked, checked: false }, t)).toBeNull();
    expect(firewallBannerView({ ...blocked, blocked: false, error: 'x' }, t)).toBeNull();
    expect(firewallBannerView(blocked, t, { dismissed: true })).toBeNull();
  });
});

describe('firewallUnblockToast', () => {
  it('reports success, a declined prompt and a failure apart', () => {
    expect(firewallUnblockToast({ ok: true }, t)).toEqual({ ok: true, text: 'firewall.allowed' });
    expect(firewallUnblockToast({ ok: false, declined: true }, t)).toEqual({ ok: false, text: 'firewall.declined' });
    expect(firewallUnblockToast({ ok: false, error: 'boom' }, t).text).toBe('firewall.failed:{"err":"boom"}');
    expect(firewallUnblockToast(null, t).ok).toBe(false);
  });
});

describe('firewallSuspectGate', () => {
  it('lets each signal through once', () => {
    const gate = firewallSuspectGate();
    expect(gate('empty')).toBe(true);
    expect(gate('empty')).toBe(false);
    expect(gate('install')).toBe(true);
  });
});

import { describe, it, expect } from 'vitest';
import { stereoInstallChoices, dissolveHosts, restoreMessage } from './stereoinstall.js';

describe('stereoInstallChoices', () => {
  it('asks nothing for an unpaired or unreadable speaker', () => {
    expect(stereoInstallChoices(null).show).toBe(false);
    expect(stereoInstallChoices({ known: false, paired: true }).show).toBe(false);
    expect(stereoInstallChoices({ known: true, paired: false }).show).toBe(false);
  });

  it('offers keep-the-pair and install-only-this-one when the partner answers', () => {
    const c = stereoInstallChoices({ known: true, paired: true, partnerIP: '192.0.2.22', partnerOnline: true });
    expect(c.show).toBe(true);
    expect(c.options).toEqual(['both', 'only']);
  });

  it('only offers dissolving when the partner does not answer', () => {
    const c = stereoInstallChoices({ known: true, paired: true, partnerIP: '192.0.2.22', partnerOnline: false });
    expect(c.options).toEqual(['dissolve']);
    expect(c.partnerKnown).toBe(false);
  });
});

describe('dissolveHosts', () => {
  const check = { partnerIP: '192.0.2.22' };
  it('dissolves on both halves for install-only-this-one', () => {
    expect(dissolveHosts('only', '192.0.2.21', check)).toEqual(['192.0.2.21', '192.0.2.22']);
  });
  it('dissolves on this half only when the partner is gone', () => {
    expect(dissolveHosts('dissolve', '192.0.2.21', check)).toEqual(['192.0.2.21']);
  });
  it('dissolves nothing when the pair is kept', () => {
    expect(dissolveHosts('both', '192.0.2.21', check)).toEqual([]);
  });
});

describe('restoreMessage', () => {
  it('maps every status to a message, and none to nothing', () => {
    expect(restoreMessage({ status: 'waitingForPartner' }).key).toBe('setup.pairInstallPartnerNext');
    expect(restoreMessage({ status: 'kept' }).cls).toBe('setup-ok');
    expect(restoreMessage({ status: 'restored' }).key).toBe('setup.pairRestored');
    expect(restoreMessage({ status: 'failed' }).cls).toBe('setup-err');
    expect(restoreMessage({ status: 'none' })).toBeNull();
    expect(restoreMessage(null)).toBeNull();
  });
});

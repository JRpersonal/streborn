import { describe, it, expect } from 'vitest';
import {
  usServiceState, usServicesRegionIsUS, usServicesOptIn, usServiceStatusKey,
  usServiceCanDisconnect, pandoraFindVisible, pandoraStationRows, agentErrorCode, agentErrorText,
} from './usservices.js';

// A GET /api/us-services answer in the agent's shape.
function status({ country = 'US', us = true, optIn = false, pandora = {}, iheart = {}, box = {} } = {}) {
  return {
    region: { country, source: 'str' },
    standIn: {
      us, optIn,
      services: [
        { type: 'PANDORA', mode: us ? 'us' : (optIn ? 'optin' : 'off'), ...pandora },
        { type: 'IHEART', mode: us ? 'us' : (optIn ? 'optin' : 'off'), ...iheart },
      ],
    },
    boxSources: { PANDORA: { status: 'absent' }, IHEART: { status: 'absent' }, ...box },
  };
}

describe('usServiceState', () => {
  it('is unknown without an answer', () => {
    expect(usServiceState(null, 'PANDORA').state).toBe('unknown');
  });
  it('is off outside the US without the opt-in', () => {
    expect(usServiceState(status({ country: 'DE', us: false }), 'PANDORA').state).toBe('off');
  });
  it('is unlinked when enabled and the speaker holds no account', () => {
    expect(usServiceState(status({ country: 'DE', us: false, optIn: true }), 'PANDORA').state).toBe('unlinked');
  });
  it('is linked while the speaker has the account but the source is not READY', () => {
    const st = usServiceState(status({ pandora: { registered: true, username: 'li***@example.com' },
      box: { PANDORA: { status: 'UNAVAILABLE', hasAccount: 'true' } } }), 'PANDORA');
    expect(st).toEqual({ state: 'linked', account: 'li***@example.com' });
  });
  it('is linked when only the stand-in has the registration', () => {
    expect(usServiceState(status({ iheart: { registered: true } }), 'IHEART').state).toBe('linked');
  });
  it('is ready when the speaker lists the source READY with an account', () => {
    const st = usServiceState(status({ pandora: { carriedOver: 'li***@example.com' },
      box: { PANDORA: { status: 'READY', hasAccount: 'true' } } }), 'PANDORA');
    expect(st).toEqual({ state: 'ready', account: 'li***@example.com' });
  });
  it('does not show the placeholder for a carried-over source without a name', () => {
    const st = usServiceState(status({ pandora: { carriedOver: '(no account name)' },
      box: { PANDORA: { status: 'READY', hasAccount: 'true' } } }), 'PANDORA');
    expect(st.account).toBe('');
  });
  it('falls back to the region flags when the service list is missing', () => {
    const s = { standIn: { optIn: true }, boxSources: {} };
    expect(usServiceState(s, 'PANDORA').state).toBe('unlinked');
  });
});

describe('region and opt-in', () => {
  it('treats the US region or the stand-in flag as US', () => {
    expect(usServicesRegionIsUS(status())).toBe(true);
    expect(usServicesRegionIsUS({ region: { country: 'de' }, standIn: { us: false } })).toBe(false);
    expect(usServicesRegionIsUS({ region: {}, standIn: { us: true } })).toBe(true);
    expect(usServicesRegionIsUS(null)).toBe(false);
  });
  it('reads the opt-in', () => {
    expect(usServicesOptIn(status({ us: false, optIn: true }))).toBe(true);
    expect(usServicesOptIn(status({ us: false }))).toBe(false);
  });
});

describe('status line and actions', () => {
  it('names the account when there is one', () => {
    expect(usServiceStatusKey({ state: 'ready', account: 'x' })).toBe('usServices.stateReady');
    expect(usServiceStatusKey({ state: 'ready', account: '' })).toBe('usServices.stateReadyNoName');
    expect(usServiceStatusKey({ state: 'linked', account: 'x' })).toBe('usServices.stateLinked');
    expect(usServiceStatusKey({ state: 'linked', account: '' })).toBe('usServices.stateLinkedNoName');
    expect(usServiceStatusKey({ state: 'unlinked' })).toBe('usServices.stateUnlinked');
    expect(usServiceStatusKey({ state: 'off' })).toBe('usServices.stateOff');
  });
  it('offers Disconnect only for an account on the speaker', () => {
    expect(usServiceCanDisconnect({ state: 'ready' })).toBe(true);
    expect(usServiceCanDisconnect({ state: 'linked' })).toBe(true);
    expect(usServiceCanDisconnect({ state: 'unlinked' })).toBe(false);
    expect(usServiceCanDisconnect(null)).toBe(false);
  });
});

describe('pandoraFindVisible', () => {
  it('shows Pandora in Find only when the speaker lists it READY', () => {
    expect(pandoraFindVisible(status({ box: { PANDORA: { status: 'READY', hasAccount: 'true' } } }))).toBe(true);
    expect(pandoraFindVisible(status({ box: { PANDORA: { status: 'UNAVAILABLE', hasAccount: 'true' } } }))).toBe(false);
    expect(pandoraFindVisible(status({ country: 'DE', us: false, box: { PANDORA: { status: 'READY', hasAccount: 'true' } } }))).toBe(false);
    expect(pandoraFindVisible(null)).toBe(false);
  });
});

describe('pandoraStationRows', () => {
  it('builds rows and drops stations without a location', () => {
    const rows = pandoraStationRows({ status: 'READY', stations: [
      { name: 'Jazz Radio', location: '111', type: 'stationurl', art: 'https://img.example.com/1.jpg' },
      { name: '', itemName: 'Blues Radio', location: ' 222 ', art: 'javascript:alert(1)' },
      { name: 'Broken', location: '' },
    ] });
    expect(rows).toEqual([
      { name: 'Jazz Radio', location: '111', type: 'stationurl', art: 'https://img.example.com/1.jpg', presetable: true },
      { name: 'Blues Radio', location: '222', type: 'stationurl', art: '', presetable: true },
    ]);
  });
  it('accepts an empty or missing list', () => {
    expect(pandoraStationRows({ stations: [] })).toEqual([]);
    expect(pandoraStationRows(null)).toEqual([]);
  });
  it('keeps a station the speaker marks as not presetable', () => {
    expect(pandoraStationRows([{ name: 'x', location: '1', presetable: false }])[0].presetable).toBe(false);
  });
});

describe('agent errors', () => {
  it('reads the code and the message', () => {
    const err = new Error('status 409: {"code":"not-linked","error":"No Pandora account is linked to this speaker."}\n');
    expect(agentErrorCode(err)).toBe('not-linked');
    expect(agentErrorText(err)).toBe('No Pandora account is linked to this speaker.');
    expect(agentErrorCode('timeout')).toBe('');
    expect(agentErrorText('timeout')).toBe('timeout');
  });
});

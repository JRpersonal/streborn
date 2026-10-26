import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// The phone remote (internal/webui/assets/index.html) on the FOLLOWING half of
// a stereo pair: the agent refuses a play there with 409 box-grouped and
// stereo:true, and the page must say where to play instead rather than
// "Could not start". The functions are lifted off the served page, the same
// way spotifyaccountlabel.test.js does it, because that page has no bundler.

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8');

function lift(signature) {
  const start = page.indexOf(signature);
  expect(start, `${signature} missing from the phone page`).toBeGreaterThan(-1);
  const end = page.indexOf('\n}\n', start);
  return page.slice(start, end + 2);
}

const src = [
  lift('function isPairFollowerRefusal('),
  lift('function isPairIncompleteRefusal('),
  lift('function pairMasterLink('),
  lift('async function postPlay('),
  lift('function playRefusal('),
].join('\n');

const T = { pairBroken: 'PAIR BROKEN', spNeedPick: 'pick', spNeedPremium: 'premium' };

function load({ answer, origin = { protocol: 'http:', port: '8080' } }) {
  const shown = [];
  const notices = [];
  const fns = new Function('apiDetail', 'showPairFollowerNotice', 'showPlayNotice', 'location', 'T',
    `${src}; return { isPairFollowerRefusal, pairMasterLink, postPlay, playRefusal };`)(
    async () => answer,
    (d) => shown.push(d),
    (m) => notices.push(m),
    origin,
    T,
  );
  return { ...fns, shown, notices };
}

const refusal = {
  error: 'box-grouped', stereo: true, master: 'AABBCCDDEEFF',
  masterIP: '192.0.2.10', masterName: 'Lounge left', masterURL: '', pairName: 'Living room',
};

describe('phone page on a stereo pair follower', () => {
  it('shows the pair notice for the follower refusal and reports it handled', async () => {
    const p = load({ answer: { ok: false, status: 409, data: refusal } });
    const res = await p.postPlay('/api/play', {});
    expect(res).toMatchObject({ ok: false, handled: true });
    expect(p.shown).toEqual([refusal]);
  });

  it('leaves a zone follower refusal and every other failure to the caller', async () => {
    const zone = load({ answer: { ok: false, status: 409, data: { error: 'box-grouped' } } });
    expect(await zone.postPlay('/api/play', {})).toMatchObject({ ok: false, handled: false });
    expect(zone.shown).toEqual([]);
    const bad = load({ answer: { ok: false, status: 502, data: { error: 'Station could not be played' } } });
    expect(await bad.postPlay('/api/play', {})).toMatchObject({ ok: false, handled: false });
  });

  it('says in words that the pair is incomplete instead of showing the code', async () => {
    const p = load({ answer: { ok: false, status: 409, data: { error: 'stereo-incomplete', reason: 'partner-gone' } } });
    expect(await p.postPlay('/api/queue', {})).toMatchObject({ ok: false, handled: true });
    expect(p.notices).toEqual(['PAIR BROKEN']);
    expect(p.shown).toEqual([]);
    expect(p.playRefusal({ error: 'stereo-incomplete' })).toBe('PAIR BROKEN');
  });

  it('reports a started play as ok', async () => {
    const p = load({ answer: { ok: true, status: 200, data: { status: 'playing' } } });
    expect(await p.postPlay('/api/play', {})).toMatchObject({ ok: true, handled: false });
  });

  it('links the master by the peer list address, else by its pair address on this port', () => {
    const p = load({ answer: null });
    expect(p.pairMasterLink({ ...refusal, masterURL: 'http://192.0.2.10:8080/' })).toBe('http://192.0.2.10:8080/');
    expect(p.pairMasterLink(refusal)).toBe('http://192.0.2.10:8080/');
    expect(p.pairMasterLink({ ...refusal, masterIP: '' })).toBe('');
  });
});

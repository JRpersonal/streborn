// The install pushed the Spotify engine the moment a speaker reported it
// missing, without checking that the agent it had just installed was the one
// running or had settled, and took the first "no space" answer as final (#994).
// These tests pin the install to pushing only at the build the app carries,
// and to one settled retry after a space refusal, without making an install
// that fits wait for anything.
import { describe, it, expect } from 'vitest';
import { verifyInstalledLoop, waitForStableAgent, installAgentOnTarget } from './agentsettle.js';

const SHA = 'a'.repeat(64);
const OLD = 'b'.repeat(64);

// A fake clock: sleeping advances it, nothing really waits.
function clock() {
  let t = 1_000_000;
  return {
    now: () => t,
    sleep: async (ms) => { t += ms; },
    advance: (ms) => { t += ms; },
  };
}

function harness({ answers, engine, settles = true }) {
  const c = clock();
  const calls = { probe: 0, push: 0, settle: 0 };
  const probe = async () => {
    calls.probe++;
    const a = typeof answers === 'function' ? answers(calls.probe) : answers;
    if (a instanceof Error) throw a;
    return a;
  };
  const ensureEngine = async () => {
    calls.push++;
    return engine(calls.push);
  };
  const waitStable = async (deadlineMs) => {
    calls.settle++;
    c.advance(30_000);
    return settles && c.now() < deadlineMs;
  };
  return { c, calls, run: (extra = {}) => verifyInstalledLoop({
    probe, ensureEngine, waitStable, appSha: SHA, sleep: c.sleep, now: c.now, ...extra,
  }) };
}

const onTarget = { version: 'v1.0.8', agentRunningSha256: SHA, goLibrespot: 'missing' };
const present = { version: 'v1.0.8', agentRunningSha256: SHA, goLibrespot: 'present' };

describe('which agent the install may push the engine at', () => {
  it('waits for the build the app carries when both report a hash', () => {
    expect(installAgentOnTarget({ version: 'v1.0.7', agentRunningSha256: OLD }, SHA)).toBe(false);
    expect(installAgentOnTarget(onTarget, SHA)).toBe(true);
  });

  it('accepts any answering agent when a hash is missing on either side', () => {
    expect(installAgentOnTarget({ version: 'v0.9.10' }, SHA)).toBe(true);
    expect(installAgentOnTarget({ version: 'v1.0.8', agentRunningSha256: OLD }, '')).toBe(true);
    expect(installAgentOnTarget(null, SHA)).toBe(false);
    expect(installAgentOnTarget({ goLibrespot: 'missing' }, SHA)).toBe(false);
  });
});

describe('the install engine delivery', () => {
  it('does not push at an agent that is still the old build', async () => {
    const h = harness({
      answers: (n) => (n < 4 ? { version: 'v1.0.7', agentRunningSha256: OLD, goLibrespot: 'missing' }
        : (n < 5 ? onTarget : present)),
      engine: () => 'installed',
    });
    const r = await h.run();
    expect(r.ok).toBe(true);
    expect(r.engineMissing).toBeUndefined();
    // Three passes on the old build went by without a push.
    expect(h.calls.push).toBe(1);
  });

  it('pushes at once when the engine fits, with no settle wait', async () => {
    const h = harness({ answers: (n) => (n < 2 ? onTarget : present), engine: () => 'installed' });
    const r = await h.run();
    expect(r).toEqual({ ok: true, version: present });
    expect(h.calls.settle).toBe(0);
    expect(h.calls.push).toBe(1);
  });

  it('settles and retries once after a first space refusal, then succeeds', async () => {
    const h = harness({
      answers: (n) => (n < 3 ? onTarget : present),
      engine: (n) => { if (n === 1) throw new Error('insufficient nand: need 16 MB'); return 'installed'; },
    });
    const r = await h.run();
    expect(r.ok).toBe(true);
    expect(r.engineMissing).toBeUndefined();
    expect(h.calls.push).toBe(2);
    expect(h.calls.settle).toBe(1);
  });

  it('reports the engine missing after a second refusal from a settled agent', async () => {
    const h = harness({ answers: onTarget, engine: () => { throw new Error('HTTP 507 no space left'); } });
    const r = await h.run();
    expect(r.ok).toBe(true);
    expect(r.engineMissing).toBe(true);
    expect(r.engineReason).toMatch(/507/);
    expect(h.calls.push).toBe(2);
    expect(h.calls.settle).toBe(1);
  });

  it('names the space refusal when the window closes before the agent settles', async () => {
    const h = harness({
      answers: onTarget,
      engine: () => { throw new Error('insufficient nand'); },
      settles: false,
    });
    const r = await h.run();
    expect(r.ok).toBe(true);
    expect(r.engineMissing).toBe(true);
    expect(r.engineReason).toBe('insufficient nand');
    expect(h.calls.push).toBe(1);
  });

  it('does not settle-wait after an error that is not about space', async () => {
    const h = harness({
      answers: (n) => (n < 3 ? onTarget : present),
      engine: (n) => { if (n === 1) throw new Error('connection reset'); return 'installed'; },
    });
    const r = await h.run();
    expect(r.ok).toBe(true);
    expect(h.calls.settle).toBe(0);
    expect(h.calls.push).toBe(2);
  });

  it('stays inside the 30 minute ceiling when the agent never reaches the target build', async () => {
    const h = harness({
      answers: { version: 'v1.0.7', agentRunningSha256: OLD, goLibrespot: 'missing' },
      engine: () => 'installed',
    });
    const start = h.c.now();
    const r = await h.run();
    expect(r.ok).toBe(true);
    expect(r.engineMissing).toBe(true);
    expect(r.engineReason).toBe('engine not delivered inside the install window');
    expect(h.calls.push).toBe(0);
    expect(h.c.now() - start).toBeLessThanOrEqual(1_800_000 + 20_000);
  });

  it('finishes at once on a build that carries no engine', async () => {
    const h = harness({ answers: onTarget, engine: () => 'no embedded engine in this build' });
    const r = await h.run();
    expect(r).toEqual({ ok: true, version: onTarget });
  });

  it('fails only when the speaker never answered at all', async () => {
    const h = harness({ answers: new Error('unreachable'), engine: () => 'installed' });
    const r = await h.run();
    expect(r.ok).toBe(false);
    expect(h.calls.push).toBe(0);
  });
});

describe('waitForStableAgent', () => {
  it('waits out the post-restart settling window before calling the agent stable', async () => {
    const c = clock();
    let uptime = 100;
    const probe = async () => { uptime += 3; return { version: 'v1.0.8', uptimeSec: String(uptime) }; };
    const ok = await waitForStableAgent(probe, c.now() + 600_000, 30_000, 150, { sleep: c.sleep, now: c.now });
    expect(ok).toBe(true);
    expect(uptime).toBeGreaterThanOrEqual(150 + 30);
  });

  it('restarts the stability clock when the uptime drops', async () => {
    const c = clock();
    const seq = [200, 203, 206, 5, 160, 163, 166, 169, 172, 175, 178, 181, 184, 187, 190, 193];
    let i = 0;
    const probe = async () => ({ uptimeSec: String(seq[Math.min(i++, seq.length - 1)]) });
    const ok = await waitForStableAgent(probe, c.now() + 600_000, 30_000, 150, { sleep: c.sleep, now: c.now });
    expect(ok).toBe(true);
    // Stability counted from the first probe after the reboot that is past 150 s.
    expect(i).toBeGreaterThanOrEqual(5 + 10);
  });

  it('returns false at the deadline', async () => {
    const c = clock();
    const probe = async () => { throw new Error('down'); };
    const ok = await waitForStableAgent(probe, c.now() + 60_000, 30_000, 150, { sleep: c.sleep, now: c.now });
    expect(ok).toBe(false);
  });
});

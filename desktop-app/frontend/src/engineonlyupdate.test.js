// A speaker that already runs this app's agent and only lacks the Spotify
// engine must get the engine, not another agent upload. The upload reboots the
// speaker, and on a slow chassis (a CineMate needs about eight minutes) that
// reboot outlasted the verify window, so the engine step after it never ran and
// every click on "Update" started the same round again (2026-10-10).
import { describe, it, expect } from 'vitest';
import { agentCurrentEngineMissing } from './utils.js';

const SHA = 'a'.repeat(64);
const OTHER = 'b'.repeat(64);
const APP = { version: 'v1.0.10', build: '2026-10-08-1954', agentSha256: SHA };

describe('agentCurrentEngineMissing', () => {
  it('skips the agent when the running agent is this one and the engine is missing', () => {
    expect(agentCurrentEngineMissing({ version: 'v1.0.10', build: '2026-10-08-1954', agentRunningSha256: SHA, goLibrespot: 'missing' }, APP)).toBe(true);
  });

  it('pushes the agent when the speaker runs a different agent', () => {
    expect(agentCurrentEngineMissing({ version: 'v1.0.9', build: '2026-10-05-1200', agentRunningSha256: OTHER, goLibrespot: 'missing' }, APP)).toBe(false);
  });

  it('trusts the running checksum over a matching version string', () => {
    // Same version and build stamp, different running binary: a push that
    // landed on disk but did not boot, or a dev build. The agent goes again.
    expect(agentCurrentEngineMissing({ version: 'v1.0.10', build: '2026-10-08-1954', agentRunningSha256: OTHER, goLibrespot: 'missing' }, APP)).toBe(false);
  });

  it('falls back to version plus build when no checksum is known', () => {
    expect(agentCurrentEngineMissing({ version: 'v1.0.10', build: '2026-10-08-1954', goLibrespot: 'missing' }, APP)).toBe(true);
    expect(agentCurrentEngineMissing({ version: 'v1.0.10', build: 'other', goLibrespot: 'missing' }, APP)).toBe(false);
  });

  it('leaves a speaker with its engine, or an unknown one, to the normal update', () => {
    expect(agentCurrentEngineMissing({ version: 'v1.0.10', build: '2026-10-08-1954', agentRunningSha256: SHA, goLibrespot: 'present' }, APP)).toBe(false);
    expect(agentCurrentEngineMissing(null, APP)).toBe(false);
    expect(agentCurrentEngineMissing({ agentRunningSha256: SHA, goLibrespot: 'missing' }, null)).toBe(false);
  });
});

// #2070: a speaker woke at full volume because its start volume was 100, and
// the owner did not remember setting it. Ticking "Start volume" in the app must
// start the marker at a quiet 20, never at 100 and never at whatever the speaker
// plays right now; the agent keeps it off until something is saved.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('./basics.js', import.meta.url), 'utf8');

describe('start volume default', () => {
  it('turns on at 20', () => {
    const block = src.slice(src.indexOf('export function refreshStartVolume'), src.indexOf('export function refreshMaxVolume'));
    expect(block).toContain("prefix: 'boxStartVol'");
    expect(block).toMatch(/firstLevel:\s*20\b/);
  });

  it('only a stored level replaces the first level', () => {
    expect(src).toContain('vol = saved > 0 ? saved : cfg.firstLevel;');
  });
});

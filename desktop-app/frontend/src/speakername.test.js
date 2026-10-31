import { describe, it, expect } from 'vitest';
import { sanitizeSpeakerName, boxHasUnsafeName, unsafeNameBoxes } from './speakername.js';

describe('sanitizeSpeakerName', () => {
  it('replaces & and drops < > the way the agent does', () => {
    expect(sanitizeSpeakerName('Test&B')).toBe('Test+B');
    expect(sanitizeSpeakerName('Tom & Jerry')).toBe('Tom + Jerry');
    expect(sanitizeSpeakerName('<Office>')).toBe('Office');
    expect(sanitizeSpeakerName('  Küche  ')).toBe('Küche');
    expect(sanitizeSpeakerName('"Den" O\'Brien')).toBe('"Den" O\'Brien');
    expect(sanitizeSpeakerName('')).toBe('');
    expect(sanitizeSpeakerName(undefined)).toBe('');
  });
});

describe('boxHasUnsafeName', () => {
  it('trusts the agent flag', () => {
    expect(boxHasUnsafeName({ friendlyName: 'Küche', speakerNameUnsafe: true })).toBe(true);
  });
  it('falls back to the name for an older agent', () => {
    expect(boxHasUnsafeName({ friendlyName: 'Test&B' })).toBe(true);
    expect(boxHasUnsafeName({ friendlyName: 'Kitchen' })).toBe(false);
  });
  it('never flags a stock or offline speaker', () => {
    expect(boxHasUnsafeName({ friendlyName: 'Test&B', kind: 'stock' })).toBe(false);
    expect(boxHasUnsafeName({ friendlyName: 'Test&B', offline: true })).toBe(false);
  });
  it('lists only the affected speakers', () => {
    const boxes = [{ friendlyName: 'A&B' }, { friendlyName: 'C' }, null];
    expect(unsafeNameBoxes(boxes).map(b => b.friendlyName)).toEqual(['A&B']);
  });
});

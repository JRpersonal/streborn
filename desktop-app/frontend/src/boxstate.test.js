import { describe, it, expect } from 'vitest';
import { answersWithoutSTR } from './boxstate.js';

describe('answersWithoutSTR', () => {
  it('is false for a missing or offline record: nothing answers, that is the dead case', () => {
    expect(answersWithoutSTR(null)).toBe(false);
    expect(answersWithoutSTR(undefined)).toBe(false);
    expect(answersWithoutSTR({ kind: 'stock', offline: true })).toBe(false);
    expect(answersWithoutSTR({ kind: 'str', strSilent: true, offline: true })).toBe(false);
  });

  it('is false for a live STR record whose agent answered', () => {
    expect(answersWithoutSTR({ kind: 'str', version: '0.9.74' })).toBe(false);
  });

  it('is true when the Bose firmware answered but the agent did not', () => {
    // This refresh only: stock :8090 answered, agent port silent.
    expect(answersWithoutSTR({ kind: 'str', version: '0.9.74', strSilent: true })).toBe(true);
    // Discovery already degraded the record.
    expect(answersWithoutSTR({ kind: 'stock', strNotRunning: true })).toBe(true);
    // STR was removed: a plain stock speaker.
    expect(answersWithoutSTR({ kind: 'stock' })).toBe(true);
  });
});

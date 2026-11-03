import { describe, expect, it, vi } from 'vitest';

vi.mock('../../api.js', () => ({}));

const { modelWithoutClockDisplay } = await import('./sound.js');

describe('modelWithoutClockDisplay', () => {
  it('hides the clock on models with no front display', () => {
    for (const m of [
      'SoundTouch 10',
      'SoundTouch Wireless Link Adapter', // #635
      'Wireless Link',
      'SoundTouch SA-5 amplifier',
      'SA-4',
    ]) {
      expect(modelWithoutClockDisplay(m), m).toBe(true);
    }
  });

  it('keeps the clock on models that show the time', () => {
    for (const m of [
      'SoundTouch 20',
      'SoundTouch 30',
      'SoundTouch Portable',
      'Wave SoundTouch', // segment display shows the time
      'SoundTouch 300',
      '',
      undefined,
    ]) {
      expect(modelWithoutClockDisplay(m), String(m)).toBe(false);
    }
  });
});

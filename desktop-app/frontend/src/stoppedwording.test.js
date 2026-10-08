// #1034: a stopped speaker was "ready" in the app and "Idle" on the phone page.
// Both now say stopped; the app's key is status.stopped in every bundle.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';

const dir = new URL('./i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.slice(0, -5), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');

describe('stopped wording', () => {
  it('every bundle has status.stopped and no status.ready', () => {
    for (const [lang, b] of Object.entries(bundles)) {
      expect(b['status.stopped'], lang).toBeTruthy();
      expect(b['status.ready'], lang).toBeUndefined();
      // Stopped and paused are different states and must read differently.
      expect(b['status.stopped'], lang).not.toBe(b['status.paused']);
    }
  });

  it('matches the phone page in English and German', () => {
    expect(bundles.en['status.stopped']).toBe('stopped');
    expect(bundles.de['status.stopped']).toBe('gestoppt');
  });

  it('is what the status bar shows when nothing plays', () => {
    expect(main).toContain("t('status.stopped')");
    expect(main).not.toContain("t('status.ready')");
  });
});

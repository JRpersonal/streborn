// The setup picker says under a stock speaker's card when that speaker is still
// set up for another cloud replacement (AfterTouch, OpenCloudTouch): discovery
// reads it from the firmware's /info cloud address (rivalCloudURL, Go side
// discovery_probe.go). Field case 2026-11-03: two Waves still on AfterTouch.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const bundles = join(here, 'i18n', 'bundles');

describe('the rival cloud note in the setup picker', () => {
  it('is drawn for a stock speaker that carries rivalCloudURL, escaped', () => {
    const picker = srcOf('renderSetupTargetPicker');
    const stockLoop = picker.slice(picker.indexOf('for (const b of stockBoxes)'));
    expect(stockLoop).toContain('if (b.rivalCloudURL)');
    expect(stockLoop).toMatch(/escapeHtml\(t\('setup\.rivalCloudNote', \{ url: b\.rivalCloudURL \}\)\)/);
  });

  it('exists in every language and names the server', () => {
    for (const f of readdirSync(bundles).filter(n => n.endsWith('.json'))) {
      const b = JSON.parse(readFileSync(join(bundles, f), 'utf8'));
      expect(b['setup.rivalCloudNote'], f).toBeTruthy();
      expect(b['setup.rivalCloudNote'], f).toContain('{{url}}');
      expect(b['setup.rivalCloudNote'], f).toContain('AfterTouch');
    }
  });
});

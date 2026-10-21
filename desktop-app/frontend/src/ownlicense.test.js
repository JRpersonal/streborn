// STR moved to Apache-2.0 with a NOTICE file. The NOTICE asks every work based
// on STR to show the attribution where it shows its other credits, so STR's
// own credits dialog shows it first.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const main = readFileSync(join(here, 'main.js'), 'utf8');
const bundles = join(here, 'i18n', 'bundles');

describe('own license in the credits dialog', () => {
  it('names the holder, the license and links LICENSE and NOTICE', () => {
    expect(main).toContain('&copy; 2026 Jens Roggenfelder');
    expect(main).toContain("repo + '/LICENSE'");
    expect(main).toContain("repo + '/NOTICE'");
    // The footer fine print comes from footer.fine, not hardcoded English.
    expect(main).toContain("t('footer.fine')");
    expect(main).not.toMatch(/donation funded, MIT license/);
  });

  it('has its text in every language', () => {
    for (const f of readdirSync(bundles).filter((n) => n.endsWith('.json'))) {
      const b = JSON.parse(readFileSync(join(bundles, f), 'utf8'));
      expect(b['footer.fine'], f).toMatch(/Apache-2\.0/);
      expect(b['footer.fine'], f).not.toMatch(/MIT|[–—]/);
      expect(b['credits.ownLicense'], f).toMatch(/Apache/);
      expect(b['credits.licenseLink'], f).toBeTruthy();
    }
  });
});

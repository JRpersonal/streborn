// The batch update's pre-scan warns about a USB stick and about weak Wi-Fi,
// and until now it never looked at storage at all.
//
// The single-box update has asked since the ST30 reports and stops to confirm
// when it is tight. So the owner of a tight speaker got a warning or no warning
// depending purely on which button he pressed, and the batch is exactly the
// button somebody with five speakers uses.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'main.js'), 'utf8');
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const KEY = 'updateAll.noteTightNand';

// The pre-scan loop, from the notes array to the prompt that renders it.
const scan = src.slice(src.indexOf('const notes = []'), src.indexOf('const notes = []') + 3200);

describe('the batch update warns about tight storage', () => {
  it('asks the speaker during the pre-scan', () => {
    expect(scan).toContain('BoxStoragePreflight(b.host, b.port)');
    expect(scan).toContain(KEY);
  });

  it('renders the gate verdict instead of deciding for itself', () => {
    // Deriving this in JavaScript went wrong twice on the single-box path: it
    // compared the raw agent size instead of the compression-credited need,
    // and it credited a present engine only when one field was reported.
    expect(scan).toContain('pf.tight');
    expect(scan).toContain('pf.freeBytes');
    expect(scan).toContain('pf.needBytes');
    expect(scan).not.toMatch(/nandNeed|0\.7|ubifs/i);
  });

  it('never blocks a batch when the headroom is unknown', () => {
    // An older agent reports no free figure at all, and a speaker that cannot
    // answer must not stop the other four from being updated.
    const at = scan.indexOf('BoxStoragePreflight');
    expect(scan.slice(at)).toMatch(/catch\s*\{/);
  });

  it('is a note, not a second confirmation', () => {
    // The batch already asks once for the whole run. Tight storage costs a
    // re-delivered engine after the reboot, which the flow does by itself.
    const at = scan.indexOf('BoxStoragePreflight');
    expect(scan.slice(at)).not.toContain('confirmWarn');
  });

  it('has the string in every language, with the same placeholders', () => {
    for (const lang of langs) {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      expect(b[KEY], `${lang} is missing ${KEY}`).toBeTruthy();
      for (const ph of ['{{name}}', '{{freeMB}}', '{{needMB}}']) {
        expect(b[KEY], `${lang} lost ${ph}`).toContain(ph);
      }
    }
  });

  it('keeps real umlauts in the German string', () => {
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'))[KEY];
    expect(de).toMatch(/[äöüß]/);
    expect(de).not.toMatch(/\b(noetig|laeuft|fuer|ueber)\b/);
  });
});

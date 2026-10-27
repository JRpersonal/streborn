// Field case 2026-10-21: an update to a SoundTouch 20 was slow but worked,
// the app said "takes longer than expected, check power / network", and the
// owner set the speaker up again with a USB stick on top of the update that
// was still finishing. The backend now refuses that install
// (install_guard.go). These guard the frontend half: the refusal is not
// rendered as a failure (no repair button, no stick section, no failure
// report), and the timeout toast says to wait and never to reach for the
// stick.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const bundleDir = join(here, 'i18n', 'bundles');
const bundles = Object.fromEntries(readdirSync(bundleDir)
  .filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(join(bundleDir, f), 'utf8'))]));

describe('install refused by the guard', () => {
  const wait = srcOf('waitForBoxAfterSetup');
  const refused = wait.indexOf("result.code === 'update-finishing' || result.code === 'str-running'");

  it('is handled before the failure screen', () => {
    expect(refused).toBeGreaterThan(0);
    const failure = wait.indexOf("const repairCodes = [");
    expect(failure).toBeGreaterThan(refused);
    // The refusal branch returns on its own, before any of the failure
    // screen's offers can render.
    const branch = wait.slice(refused, failure);
    expect(branch).toContain('return;');
    for (const offer of ['installRepairSSH', 'installFailureReportHtml', 'setupStickDetails', 'powerCycleAdviceHtml']) {
      expect(branch).not.toContain(offer);
    }
    expect(branch).toContain("t('setup.installUpdateFinishing')");
    expect(branch).toContain("t('setup.installRefusedRemoveStick')");
  });
});

describe('the guard strings', () => {
  const keys = ['setup.installUpdateFinishing', 'setup.installRefusedRemoveStick', 'update.tookLongerToast'];

  it('exist in every bundle', () => {
    expect(Object.keys(bundles).length).toBe(13);
    for (const [lang, b] of Object.entries(bundles)) {
      for (const k of keys) expect(b[k], `${lang} ${k}`).toBeTruthy();
    }
  });

  it('use no dashes as punctuation', () => {
    for (const [lang, b] of Object.entries(bundles)) {
      for (const k of keys) expect(b[k], `${lang} ${k}`).not.toMatch(/ [-–—] |—|–/);
    }
  });

  it('never send the owner of a slow update to the stick', () => {
    const toast = bundles.en['update.tookLongerToast'];
    expect(toast).toMatch(/do not set it up again with a USB stick/i);
    expect(toast).not.toMatch(/check power/i);
  });

  it('address German readers informally', () => {
    const de = bundles.de;
    expect(de['setup.installRefusedRemoveStick']).toContain('du');
    expect(de['setup.installUpdateFinishing']).toContain('Lass ihn');
    for (const k of keys) expect(de[k]).not.toMatch(/\bSie\b|\bIhr(en)?\b/);
  });
});

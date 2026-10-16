// A preset key with its own volume (#1323).
//
// Source-level assertions, like presetrename.test.js next door: the edit
// dialog is DOM-bound and lives in main.js (the view-extraction trap), so the
// rules are pinned on the source.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const mainJS = readFileSync(join(here, 'main.js'), 'utf-8');
const apiJS = readFileSync(join(here, 'api.js'), 'utf-8');
const styleCSS = readFileSync(join(here, 'style.css'), 'utf-8');
const bundlesDir = join(here, 'i18n', 'bundles');

const fn = mainJS.slice(
  mainJS.indexOf('async function renamePresetKey(slot)'),
  mainJS.indexOf('\n// applyTrackScroll'),
);

describe('a preset key can keep its own volume', () => {
  it('offers the control in the key dialog, off unless the key has a level', () => {
    expect(fn).toContain('presetVolumeControlHTML()');
    expect(fn).toContain('const currentVol = p.volume > 0 ? p.volume : 0;');
    expect(fn).toContain('on.checked = currentVol > 0;');
    expect(styleCSS).toContain('.preset-volume-row');
  });

  it('writes the level through its own binding, and only when it changed', () => {
    expect(apiJS).toContain('SetPresetVolume');
    expect(fn).toContain('SetPresetVolume(state.currentBox.host, state.currentBox.port, slot, vol)');
    expect(fn).toContain('const volChanged = vol !== currentVol;');
    // Off is 0, so the agent leaves the volume alone.
    expect(fn).toContain('if (!on || !range || !on.checked) return 0;');
  });

  it('names an old speaker instead of failing silently', () => {
    expect(fn).toContain("includes('preset-volume-unsupported')");
    expect(fn).toContain("t('preset.volumeUnsupported')");
  });

  it('is translated in every bundle', () => {
    const keys = ['preset.volumeToggle', 'preset.volumeHint', 'preset.volumeSetToast',
      'preset.volumeOffToast', 'preset.volumeUnsupported'];
    for (const f of readdirSync(bundlesDir).filter(n => n.endsWith('.json'))) {
      const b = JSON.parse(readFileSync(join(bundlesDir, f), 'utf-8'));
      for (const k of keys) {
        expect(b[k], `${f} ${k}`).toBeTruthy();
      }
      expect(b['preset.volumeSetToast'], f).toContain('{{v}}');
    }
  });
});

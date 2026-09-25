// The install help checklist was keyed on the failure code alone. On a
// SoundTouch 300 the code "speaker-not-back" means the soundbar is sitting in
// its alternating-yellow blink with every port dead, which nothing but a power
// interrupt clears, and the checklist told the owner to check that the speaker
// is on the network, check the Wi-Fi and check the cable.
//
// The third 300 owner in a week (2026-09-24) did exactly that, twice: he
// factory-reset the soundbar and re-entered his Wi-Fi in the Bose app, then
// reported that STR had failed on his 300. The headline above the checklist had
// told him to unplug it since v0.9.84. The list under it outvoted the headline.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { isSoundTouch300 } from './utils.js';

const here = dirname(fileURLToPath(import.meta.url));
const setup = readFileSync(join(here, 'views', 'setup.js'), 'utf8');

const help = setup.slice(
  setup.indexOf('const ST300_STUCK_CODES'),
  setup.indexOf('// waitForBoxAfterSetup runs after'),
);

describe('the checklist knows which speaker it is talking about', () => {
  it('takes the box, not only the failure code', () => {
    expect(help).toContain('function installHelpHtml(code, isNetwork, box)');
    expect(setup).toContain('installHelpHtml(result && result.code, !!knownBox, foundBox)');
  });

  it('drops the network steps for a blinking 300 instead of demoting them', () => {
    expect(help).toContain('isSoundTouch300(box)');
    expect(help).toContain('ST300_STUCK_CODES.indexOf(code) >= 0');
    // The reduced list is the logs step and nothing else. netWifi / netCable /
    // netOnNetwork are the three that sent the reporter to a factory reset.
    const branch = help.slice(help.indexOf('if (isSoundTouch300(box)'), help.indexOf('const steps = isNetwork'));
    expect(branch).toContain("isNetwork ? 'netLogs' : 'logs'");
    expect(branch).not.toContain('netWifi');
    expect(branch).not.toContain('netCable');
    expect(branch).not.toContain('netOnNetwork');
  });

  it('covers every code whose 300 symptom is the blink', () => {
    for (const code of ['speaker-not-back', 'agent-not-up', 'install-timeout',
      'not-reachable', 'control-unresponsive']) {
      expect(help).toContain(`'${code}'`);
    }
  });

  it('leaves every other model alone', () => {
    // The generic path is still the code-keyed one.
    expect(help).toContain('INSTALL_HELP_STEPS_NET[code] || NET_HELP_DEFAULT');
  });
});

describe('the waiting screen shows the 300 its one remaining step', () => {
  it('renders the mandatory power-cycle block above the checklist', () => {
    const draw = setup.slice(setup.indexOf('const st300 = isSoundTouch300(foundBox)'),
      setup.indexOf("const save = $('setupWaitSaveLogs')"));
    expect(draw).toContain('powerCycleAdviceHtml(foundBox)');
    expect(draw).toMatch(/\+ st300 \+ wait \+ help/);
  });

  it('shows it to no other model', () => {
    expect(setup).toContain('isSoundTouch300(foundBox) ? powerCycleAdviceHtml(foundBox) :');
  });
});

describe('the model test reads both fields a speaker record can carry', () => {
  it('matches a 300 by model or by the Bose type field', () => {
    expect(isSoundTouch300({ model: 'SoundTouch 300' })).toBe(true);
    expect(isSoundTouch300({ type: 'SoundTouch 300' })).toBe(true);
    // A stock speaker discovered before the install often has only `type`, and
    // keying on `model` alone dropped exactly those owners onto the generic
    // advice.
    expect(isSoundTouch300({ model: '', type: 'SoundTouch 300' })).toBe(true);
  });

  it('never catches the SoundTouch 30', () => {
    expect(isSoundTouch300({ model: 'SoundTouch 30' })).toBe(false);
    expect(isSoundTouch300({ type: 'SoundTouch 30' })).toBe(false);
    expect(isSoundTouch300({ model: 'SoundTouch 10' })).toBe(false);
    expect(isSoundTouch300({ model: 'SoundTouch Portable' })).toBe(false);
    expect(isSoundTouch300({})).toBe(false);
    expect(isSoundTouch300(null)).toBe(false);
  });
});

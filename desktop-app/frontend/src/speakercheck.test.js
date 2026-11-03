import { describe, it, expect, vi } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import {
  CHECK_VERDICTS, CHECK_STEP_KEYS, MAX_CHECKS, allowsInstallAgain, checkLeadText, checkMailSection,
  checkResultHTML, checkSteps, checkTargets, failingSpeakers, needsAttention, precheckHTML,
  runSpeakerChecks, speakerCheckKeys,
} from './speakercheck.js';
import { fileSource } from './test/source.js';

const dir = new URL('./i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
const en = bundles.en;
const tEn = (k, vars = {}) => String(en[k] || k).replace(/\{\{(\w+)\}\}/g, (_, v) => (v in vars ? vars[v] : ''));
const placeholders = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

const res = (verdict, extra = {}) => ({ host: '192.0.2.20', verdict, pcNetworks: '192.0.2.0/24 on WLAN', report: '', ...extra });

describe('speaker check strings', () => {
  const keys = speakerCheckKeys();

  it('has every key in all thirteen languages with the English placeholders', () => {
    expect(Object.keys(bundles)).toHaveLength(13);
    for (const [code, b] of Object.entries(bundles)) {
      for (const k of keys) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(placeholders(b[k]), `${code}: ${k}`).toEqual(placeholders(en[k]));
      }
    }
  });

  it('uses no dashes as punctuation and blames no Wi-Fi band or firewall', () => {
    for (const [code, b] of Object.entries(bundles)) {
      for (const k of [...keys, 'speaker.strNotRunningTooltip', 'ui2.speakers.list.strNotRunningNote']) {
        expect(b[k], `${code}: ${k}`).not.toMatch(/[–—]| - /);
        expect(b[k], `${code}: ${k}`).not.toMatch(/GHz|WPA/i);
      }
      for (const k of keys) expect(b[k], `${code}: ${k}`).not.toMatch(/firewall/i);
    }
  });

  it('no longer sends a not-answering speaker straight to the install', () => {
    expect(en['speaker.strNotRunningTooltip']).not.toMatch(/install/i);
    expect(en['ui2.speakers.list.strNotRunningNote']).not.toMatch(/setting up STR again/i);
    expect(bundles.de['speaker.strNotRunningTooltip']).not.toContain('installieren');
    // The old confirm body that went straight to the installation is gone.
    for (const b of Object.values(bundles)) expect(b['speaker.strNotRunningConfirmBody']).toBeUndefined();
  });

  it('points a speaker without Wi-Fi to the Bose app first, and writes German with du', () => {
    expect(en['speakerCheck.step.boseApp']).toContain('Add speaker');
    expect(en['speakerCheck.step.boseApp']).toContain('Bose SoundTouch app');
    expect(bundles.de['speakerCheck.step.boseApp']).toContain('deinem Handy');
    expect(bundles.de['speakerCheck.step.power']).toContain('weiß');
    expect(bundles.de['speakerCheck.beforeReport']).toContain('du');
  });
});

describe('verdict to steps', () => {
  it('knows the six verdicts of the Go side', () => {
    expect(CHECK_VERDICTS).toEqual(['ok', 'str-silent', 'pc-offline', 'hung', 'other-network', 'off-network']);
    // speakercheck.go must use the same words.
    const go = readFileSync(new URL('../../speakercheck.go', import.meta.url), 'utf8');
    for (const v of CHECK_VERDICTS) expect(go).toContain(`= "${v}"`);
    for (const k of Object.values(speakerCheckKeys()).filter((x) => x.startsWith('speakerCheck.lead.') && x !== 'speakerCheck.lead.failed')) {
      expect(go).toContain(`"${k}"`);
    }
  });

  it('walks an off-network speaker through power, the Bose app, a new address and unplugging', () => {
    expect(checkSteps(res('off-network'))).toEqual(CHECK_STEP_KEYS['off-network']);
    expect(checkSteps(res('off-network'))).toEqual([
      'speakerCheck.step.power', 'speakerCheck.step.boseApp', 'speakerCheck.step.newAddress', 'speakerCheck.step.unplugLast',
    ]);
    expect(checkSteps(res('other-network'))[0]).toBe('speakerCheck.step.sameWifi');
    expect(checkSteps(res('hung'))[0]).toBe('speakerCheck.step.unplugFirst');
    expect(checkSteps(res('ok'))).toEqual([]);
    expect(checkSteps(null)).toEqual([]);
    expect(checkSteps(res('weird'))).toEqual([]);
  });

  it('keeps "install again" only for a speaker whose Bose firmware answers and STR does not', () => {
    expect(allowsInstallAgain(res('str-silent'))).toBe(true);
    for (const v of ['ok', 'pc-offline', 'hung', 'other-network', 'off-network', '']) {
      expect(allowsInstallAgain(res(v)), v).toBe(false);
    }
    expect(allowsInstallAgain(null)).toBe(false);
    expect(allowsInstallAgain('busy')).toBe(false);
  });

  it('needs attention for everything but a speaker that answers', () => {
    expect(needsAttention(res('ok'))).toBe(false);
    expect(needsAttention(res('off-network'))).toBe(true);
    expect(needsAttention(res(''))).toBe(true);
    expect(needsAttention(null)).toBe(false);
  });

  it('words the lead with the name, the address and this computer\'s networks', () => {
    expect(checkLeadText(res('other-network'), 'Kitchen', tEn))
      .toBe('Kitchen has the address 192.0.2.20, which is in a different network than this computer (192.0.2.0/24 on WLAN). This computer cannot reach it there. That usually comes from a second router, a repeater or a guest Wi-Fi.');
    expect(checkLeadText(res('off-network'), 'Kitchen', tEn)).toBe('Nothing answers at 192.0.2.20, not even a ping. Kitchen is probably switched off or no longer on your Wi-Fi.');
    expect(checkLeadText({ host: '192.0.2.20', verdict: '', error: 'x' }, 'Kitchen', tEn)).toBe(en['speakerCheck.lead.failed']);
  });

  it('renders the lead and numbered steps, escaped', () => {
    const html = checkResultHTML(res('off-network'), 'Living <Room>', tEn);
    expect(html).toContain('Living &lt;Room&gt; is probably switched off');
    expect(html).toContain('<ol class="spk-check-steps">');
    expect((html.match(/<li>/g) || []).length).toBe(4);
    expect(checkResultHTML(res('ok'), 'Kitchen', tEn)).not.toContain('<ol');
  });
});

describe('who a report checks', () => {
  const A = { host: '192.0.2.1', friendlyName: 'A' };
  const OFF = { host: '192.0.2.2', friendlyName: 'Off', offline: true };
  const GONE = { host: '192.0.2.3', friendlyName: 'Gone', kind: 'stock', strNotRunning: true };
  const B = { host: '192.0.2.4', friendlyName: 'B' };

  it('picks the speakers the list shows as not answering', () => {
    expect(failingSpeakers([A, OFF, GONE, B, null])).toEqual([OFF, GONE]);
    expect(failingSpeakers(undefined)).toEqual([]);
  });

  it('checks the selected speaker first, then the failing ones, each once, capped', () => {
    expect(checkTargets(A, [A, OFF, GONE, B])).toEqual([A, OFF, GONE]);
    expect(checkTargets(OFF, [A, OFF, GONE])).toEqual([OFF, GONE]);
    expect(checkTargets(null, [A, B])).toEqual([]);
    const many = Array.from({ length: 9 }, (_, i) => ({ host: `192.0.2.${10 + i}`, offline: true }));
    expect(checkTargets(A, many)).toHaveLength(MAX_CHECKS);
  });

  it('runs each check once and survives a failing binding', async () => {
    const fn = vi.fn(async (host) => {
      if (host === OFF.host) throw new Error('boom');
      return res('off-network', { host });
    });
    const out = await runSpeakerChecks(fn, [GONE, OFF]);
    expect(fn).toHaveBeenCalledTimes(2);
    expect(out[0]).toEqual({ box: GONE, res: res('off-network', { host: GONE.host }) });
    expect(out[1].box).toBe(OFF);
    expect(out[1].res.verdict).toBe('');
    expect(out[1].res.error).toContain('boom');
  });

  it('lists what it found and carries the Go fact blocks in the mail', () => {
    const found = [{ box: OFF, res: res('off-network') }];
    const html = precheckHTML(found, (b) => b.friendlyName, tEn);
    expect(html).toContain(en['speakerCheck.beforeReport']);
    expect(html).toContain('<b>Off</b>');
    const mail = checkMailSection([
      { box: OFF, res: res('off-network', { report: 'speaker check 192.0.2.20\nresult off-network' }) },
      { box: GONE, res: { host: GONE.host, verdict: '', error: 'boom' } },
    ]);
    expect(mail).toContain('Speaker check (run before this mail)');
    expect(mail).toContain('result off-network');
    expect(mail).toContain('speaker check 192.0.2.3\nthe check could not run: boom');
    expect(checkMailSection([])).toBe('');
  });
});

describe('wiring', () => {
  it('runs the check from the classic list, the report link and the support mail', () => {
    const main = fileSource('main.js');
    expect(main).toContain('if (box.strNotRunning) { checkSpeakerDialog(box); return; }');
    expect(main).toContain("reportProblemWithCheck(`${repo}/issues`)");
    expect(main).toContain('checkSpeaker: CheckSpeaker,');
    expect(main).toContain('reportProblem: reportProblemWithCheck,');
    expect(main).not.toContain('speaker.strNotRunningConfirmBody');
    const actions = fileSource('views/settings/actions.js');
    expect(actions).toContain('runSpeakerChecks(CheckSpeaker, checkTargets(box, state.boxes))');
    expect(actions).toContain("t('speakerCheck.mailAnyway')");
    expect(actions).toContain('checkMailSection(checks)');
    const help = fileSource('shell2/help/help.js');
    expect(help).toContain("data-report=\"1\"");
    expect(help).toContain('deps.reportProblem(btn.dataset.url)');
  });
});

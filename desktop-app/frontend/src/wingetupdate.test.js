import { describe, it, expect } from 'vitest';
import { wingetInstallLabel, wingetOutcomeView, wingetFailureView } from './wingetupdate.js';
import en from './i18n/bundles/en.json';
import de from './i18n/bundles/de.json';

const t = (key, vars) => `${key}${vars ? JSON.stringify(vars) : ''}`;
const CMD = 'winget upgrade --id JRpersonal.STReborn --exact --silent --accept-source-agreements --accept-package-agreements';

describe('winget app update', () => {
  it('labels the button so the user knows winget does the update', () => {
    expect(wingetInstallLabel(t)).toBe('banner.updateViaWinget');
  });

  it('says the app closes when the helper started', () => {
    const v = wingetOutcomeView({ started: true, command: CMD, reason: 'started' }, 'v1.0.3', t);
    expect(v.kind).toBe('started');
    expect(v.command).toBeUndefined();
  });

  it('names the version winget does not offer yet, with the command', () => {
    const v = wingetOutcomeView({ notYet: true, command: CMD, reason: 'not-offered' }, 'v1.0.3', t);
    expect(v.kind).toBe('notYet');
    expect(v.text).toContain('v1.0.3');
    expect(v.command).toBe(CMD);
  });

  it('falls back to the manual command when winget is missing', () => {
    const v = wingetOutcomeView({ command: CMD, reason: 'winget-not-found' }, 'v1.0.3', t);
    expect(v).toEqual({ kind: 'manual', text: 'banner.wingetManual', command: CMD });
  });

  it('still has a command when the answer is garbage', () => {
    expect(wingetOutcomeView(null, '', t).command).toBe(CMD);
    expect(wingetOutcomeView('x', '', t).kind).toBe('manual');
  });

  it('reports a failed upgrade from the previous run once, with the exit code', () => {
    expect(wingetFailureView(null, t)).toBeNull();
    expect(wingetFailureView({ failed: false }, t)).toBeNull();
    const v = wingetFailureView({ failed: true, exitCode: '-1978335189', command: CMD }, t);
    expect(v.text).toContain('-1978335189');
    expect(v.command).toBe(CMD);
  });

  it('has real English and German text for every key it uses', () => {
    for (const k of ['banner.updateViaWinget', 'banner.wingetStarted', 'banner.wingetNotYet',
      'banner.wingetManual', 'banner.wingetFailed', 'banner.wingetCopy']) {
      expect(en[k], k).toBeTruthy();
      expect(de[k], k).toBeTruthy();
      expect(de[k], k).not.toBe(en[k]);
    }
    expect(en['banner.wingetNotYet']).toContain('{{version}}');
    expect(de['banner.wingetNotYet']).toContain('{{version}}');
    expect(de['banner.wingetFailed']).toContain('{{code}}');
  });
});

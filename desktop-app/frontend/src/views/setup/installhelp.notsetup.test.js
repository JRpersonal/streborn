// A speaker that was reset and never set up again turns ST Reborn's stand-in
// account down itself (Bose error 1038). The install used to report that as
// "still restarting, try again", and a Wave IV owner pressed Install five times
// across four releases (2026-11-18 to 11-23). The result now carries its own
// code, and the checklist sends the owner to one setup in Bose's SoundTouch app.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { installHelpHtml } from './installhelp.js';
import { setLocale } from '../../i18n/index.js';

setLocale('en');

describe('speaker-not-set-up', () => {
  for (const isNetwork of [true, false]) {
    it(`sends the owner to the Bose SoundTouch app (${isNetwork ? 'network' : 'stick'} install)`, () => {
      const html = installHelpHtml('speaker-not-set-up', isNetwork, { model: 'Wave SoundTouch' });
      expect(html).toContain('SoundTouch app');
      expect(html).toContain('factory settings');
      expect(html).toContain('press Install again');
      // None of the network checklist that sends people round again.
      expect(html).not.toContain('Give the speaker a minute');
    });
  }

  it('is in every language bundle', () => {
    for (const lang of ['ar', 'de', 'en', 'es', 'fr', 'it', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant']) {
      const b = JSON.parse(readFileSync(new URL(`../../i18n/bundles/${lang}.json`, import.meta.url), 'utf8'));
      expect(b['setup.help.boseAppSetup'], lang).toBeTruthy();
      expect(b['setup.help.boseAppRetry'], lang).toBeTruthy();
    }
  });

  it('is the code the Go install reports for the refusal', () => {
    const go = readFileSync(new URL('../../../../install_str.go', import.meta.url), 'utf8');
    expect(go).toContain('errors.Is(unlockErr, errBootstrapAccountRefused)');
    expect(go).toContain('res.Code = "speaker-not-set-up"');
  });
});

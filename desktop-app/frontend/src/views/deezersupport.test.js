// The Deezer card asks owners to write to Deezer support: since October 2025
// Deezer registers no API apps for private persons, and that is the one door
// that would let ST Reborn sign a speaker in. The card offers Deezer's help
// centre and a ready message, like the Spotify refusal notice does.
import { describe, it, expect, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { setLocale, t } from '../i18n/index.js';

vi.mock('../api.js', () => ({
  DeezerStatus: vi.fn(), DeezerRecheck: vi.fn(), UnlinkMusicService: vi.fn(),
  BrowserOpenURL: vi.fn(), ClipboardSetText: vi.fn(),
}));
vi.mock('./settings/shared.js', () => ({ confirmFor: vi.fn() }));

const { deezerSupportHTML, deezerSupportCopy, DEEZER_SUPPORT_URL, COPIED_MS } = await import('./deezercard.js');

setLocale('en');

describe('Deezer support', () => {
  it('draws the line and both buttons', () => {
    const html = deezerSupportHTML(t);
    expect(html).toContain('Ask Deezer support');
    expect(html).toContain('id="deezerSupportOpen"');
    expect(html).toContain('id="deezerSupportCopy"');
    expect(html).toContain('Copy a message for Deezer');
  });

  it('opens Deezer\'s own help centre', () => {
    expect(DEEZER_SUPPORT_URL).toBe('https://support.deezer.com/');
  });

  it('copies the message and says Copied for a moment', async () => {
    const btn = { textContent: 'Copy a message for Deezer' };
    const copy = vi.fn().mockResolvedValue(true);
    let undo;
    const later = vi.fn((fn, ms) => { undo = fn; expect(ms).toBe(COPIED_MS); });
    expect(await deezerSupportCopy(btn, { copy, later, t })).toBe(true);
    const text = copy.mock.calls[0][0];
    expect(text).toContain('ST Reborn');
    expect(text).toContain('personal API access');
    expect(btn.textContent).toBe('Copied');
    undo();
    expect(btn.textContent).toBe('Copy a message for Deezer');
  });

  it('leaves the button alone when the clipboard refuses', async () => {
    const btn = { textContent: 'x' };
    expect(await deezerSupportCopy(btn, { copy: () => false, later: vi.fn(), t })).toBe(false);
    expect(btn.textContent).toBe('x');
  });

  it('has its texts in every language', () => {
    for (const lang of ['ar', 'de', 'en', 'es', 'fr', 'it', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant']) {
      const b = JSON.parse(readFileSync(new URL(`../i18n/bundles/${lang}.json`, import.meta.url), 'utf8'));
      for (const k of ['deezer.support', 'deezer.supportOpen', 'deezer.supportCopy', 'deezer.supportCopied', 'deezer.supportMessage']) {
        expect(b[k], `${lang} ${k}`).toBeTruthy();
      }
      expect(b['deezer.supportMessage'], lang).toContain('ST Reborn');
    }
  });
});

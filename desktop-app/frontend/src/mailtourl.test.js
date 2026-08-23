// Tests for encodeURIStrict, the encoder the support-mail button uses.
//
// Wails validates every URL handed to BrowserOpenURL and refuses one containing
// any of ; | ` $ \ < > * { } [ ] ( ) ~ ! or whitespace, before the OS handler
// ever sees it. encodeURIComponent deliberately leaves six of those literal,
// so the button died on the single parenthesis in "(Please describe the
// problem here.)" with "Invalid URL shell metacharacters not allowed", on
// every Windows machine. A field log from 2026-08-22 ends on exactly that line.
import { describe, it, expect } from 'vitest';
import { encodeURIStrict } from './utils.js';

// Copied verbatim from wails v2.12.0
// internal/frontend/utils/urlValidator.go, shellDangerous.
const WAILS_REJECTS = /[;|`$\\<>*{}[\]()~! \t\n\r]/;

const SUPPORT_BODY = [
  'Hi,',
  '',
  'I need help with ST Reborn.',
  '',
  '(Please describe the problem here.)',
  '',
  '--------------------------------',
  'Diagnostic logs were saved to:',
  'C:\\Users\\me\\Documents\\str-diagnostic-Lifestyle+ST10-v0.9.55.zip',
  '',
  'IMPORTANT: please attach that file to this email before sending.',
  '',
].join('\n');

describe('encodeURIStrict', () => {
  it('encodes the six characters encodeURIComponent leaves literal', () => {
    expect(encodeURIStrict("!'()*~")).toBe('%21%27%28%29%2A%7E');
  });

  it('produces a mailto Wails accepts, where encodeURIComponent does not', () => {
    const base = 'mailto:str@sichtbar-app.de?subject=';
    const subject = 'ST Reborn support request';

    const today = base + encodeURIComponent(subject) + '&body=' + encodeURIComponent(SUPPORT_BODY);
    const fixed = base + encodeURIStrict(subject) + '&body=' + encodeURIStrict(SUPPORT_BODY);

    expect(WAILS_REJECTS.test(today)).toBe(true);
    expect(WAILS_REJECTS.test(fixed)).toBe(false);
  });

  it('still encodes everything encodeURIComponent encodes', () => {
    expect(encodeURIStrict('a b&c=d\n')).toBe('a%20b%26c%3Dd%0A');
    expect(encodeURIStrict('C:\\Users\\me')).toBe('C%3A%5CUsers%5Cme');
  });

  it('round-trips, so the mail client shows the text unchanged', () => {
    expect(decodeURIComponent(encodeURIStrict(SUPPORT_BODY))).toBe(SUPPORT_BODY);
    expect(decodeURIComponent(encodeURIStrict('Grüße (bitte) ~ jetzt!'))).toBe('Grüße (bitte) ~ jetzt!');
  });
});

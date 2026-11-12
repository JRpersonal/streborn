// #1871: Play on a music-library card in Recently played while its music
// server was off opened the error dialog with the agent's raw JSON body:
//   Error: {"code":"library-server-offline","error":"The music server ...
// The preset keys already turned that body into a sentence (#1339); the card
// and the Library row handed it to showError as it came. friendlyErrorText is
// what every dialog and every card line goes through, so the body becomes the
// localized sentence there.
import { describe, it, expect, beforeEach } from 'vitest';
import { setLocale, t } from './i18n/index.js';
import { friendlyErrorText } from './utils.js';
import { srcOf } from './test/source.js';

const SERVER = 'MinimServer[example-mac.local]';
const offBody = JSON.stringify({
  code: 'library-server-offline',
  error: `The music server ${SERVER} is not answering. Switch it on and try again.`,
  name: 'Some Album', offline: true, server: SERVER, slot: 11,
});
const goneBody = JSON.stringify({
  code: 'library-file-gone',
  error: `The music server ${SERVER} answers, but no longer has the music on this key.`,
  gone: true, name: 'Some Album', server: SERVER, slot: 11,
});

describe('a refused music-library play in a dialog or a card line', () => {
  beforeEach(() => { setLocale('en'); });

  it('says the sentence, not the JSON body, for a server that is off', () => {
    // What a rejected Wails call coerces to: "Error: <body>".
    for (const err of [offBody, `Error: ${offBody}`, `PlayURL: Error: ${offBody}`]) {
      const out = friendlyErrorText(err);
      expect(out).toBe(`The music server ${SERVER} is not answering. Switch it on and try again.`);
      expect(out).not.toContain('{');
      expect(out).not.toContain('library-server-offline');
    }
  });

  it('says it in the language of the app', () => {
    setLocale('de');
    const out = friendlyErrorText(`Error: ${offBody}`);
    expect(out).toBe(t('play.errMusicServerOffHelp', { name: SERVER }));
    expect(out).toContain('Musikserver');
    expect(out).toContain(SERVER);
  });

  it('has a sentence for a refusal that names no server', () => {
    const anon = JSON.stringify({ code: 'library-server-offline', offline: true, server: '', name: 'Song' });
    expect(friendlyErrorText(`Error: ${anon}`)).toBe(t('play.errMusicServerOffHelpAny'));
  });

  it('does the same for a server that no longer has the music', () => {
    const out = friendlyErrorText(`Error: ${goneBody}`);
    expect(out).toBe(t('play.errMusicFileGoneHelp', { name: SERVER }));
    expect(out).not.toContain('{"');
  });

  it('leaves every other error as it is', () => {
    expect(friendlyErrorText('Error: speaker not reachable')).toBe('Error: speaker not reachable');
    const grouped = '{"error":"box-grouped","master":"192.0.2.1"}';
    expect(friendlyErrorText(grouped)).toBe(grouped);
  });

  it('is what the Recently-played card and the Library row show', () => {
    // The dialog itself.
    expect(srcOf('showError')).toContain('friendlyErrorText(String(msg');
    // The card: its own line in the new layout, the dialog in the current one.
    const card = srcOf('showCardError');
    expect(card).toContain('showError(err)');
    expect(card).toContain('friendlyErrorText(');
    // A refused folder replay reaches one of the two.
    expect(srcOf('replayFolderCard')).toMatch(/c\.ui2OnError\(err\);\s*else showError\(err\);/);
    // The Library row of the new layout.
    expect(srcOf('libraryPlay')).toContain("detail: friendlyErrorText(String(e))");
  });
});

// libraryserveroff.js: a library preset whose music server is switched off.
//
// The agent refuses an app press on such a key with
// {"code":"library-server-offline","server":"<name>",...} instead of handing
// the speaker a file nobody serves (#1339). Before that, the press went ahead,
// the speaker timed out on the dead file, and the app said nothing at all,
// where the old Bose app used to say "server unavailable".
//
// friendlyError on the Go side passes this body through raw, so the server's
// name reaches the frontend and is said in the user's language. Pure string in,
// string out, so libraryserveroff.test.js covers it without a DOM.

export const LIBRARY_SERVER_OFFLINE = 'library-server-offline';

// libraryServerOffName reads the music server's name out of a play error. It
// returns null when the error is not this refusal, and '' when the refusal
// names no server.
export function libraryServerOffName(err) {
  const s = String((err && err.message) || err || '');
  if (!s.includes(LIBRARY_SERVER_OFFLINE)) return null;
  const at = s.indexOf('{');
  if (at >= 0) {
    try {
      const o = JSON.parse(s.slice(at));
      if (o && typeof o.server === 'string') return o.server.trim();
    } catch { /* not the raw body: no name to show */ }
  }
  return '';
}

// libraryServerOffNotice is the full sentence for the toast: with the
// server's name when the refusal carries one.
export function libraryServerOffNotice(name, t) {
  return name
    ? t('play.errMusicServerOffHelp', { name })
    : t('play.errMusicServerOffHelpAny');
}

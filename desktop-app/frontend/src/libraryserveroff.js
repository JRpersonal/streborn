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
      // Another refusal whose preset name holds this code is not this one.
      if (o && typeof o.code === 'string' && o.code !== LIBRARY_SERVER_OFFLINE) return null;
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

// The sibling refusal: the music server answers, but no longer has the key's
// music (#919, #733, #726). Its library was re-indexed, or the folder or the
// drive with the music is not there any more, and the speaker would only
// answer the file with AUDIO_ERROR_BAD_URL. The agent refuses the press with
// {"code":"library-file-gone","server":"<name>",...}, passed through raw the
// same way.
export const LIBRARY_FILE_GONE = 'library-file-gone';

// libraryFileGoneName reads the music server's name out of a play error. It
// returns null when the error is not this refusal, and '' when the refusal
// names no server. A raw body is matched by its code, so a preset whose name
// happens to hold the code's words is not taken for it.
export function libraryFileGoneName(err) {
  const s = String((err && err.message) || err || '');
  if (!s.includes(LIBRARY_FILE_GONE)) return null;
  const at = s.indexOf('{');
  if (at >= 0) {
    try {
      const o = JSON.parse(s.slice(at));
      if (o && typeof o === 'object') {
        if (o.code !== LIBRARY_FILE_GONE) return null;
        return typeof o.server === 'string' ? o.server.trim() : '';
      }
    } catch { /* not the raw body: no name to show */ }
  }
  return '';
}

// libraryFileGoneNotice is the full sentence for the toast: with the server's
// name when the refusal carries one.
export function libraryFileGoneNotice(name, t) {
  return name
    ? t('play.errMusicFileGoneHelp', { name })
    : t('play.errMusicFileGoneHelpAny');
}

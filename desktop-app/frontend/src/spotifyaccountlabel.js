// What a preset tile may show about the Spotify account it was saved under.
//
// The tile has carried the account since the Spotify-logo change, to tell apart
// presets belonging to different accounts. That is genuinely useful when the
// account is an old-style Spotify username, which is a name a person chose and
// recognises.
//
// Modern Spotify accounts do not have one. What arrives is the canonical user
// id, an opaque 22-to-28 character run of lowercase letters and digits. Printed
// on the tile it tells the owner nothing they can use, and it is a personal
// identifier that resolves to a public profile page: it went into a support mail
// on 2026-09-30, and on 2026-10-02 it reached a public issue after all, both in
// a screenshot of this line and in clear inside an attached diagnostic.
//
// It also misled a reporter about what STR was doing. Two presets carried two
// different ids and they read the pair as evidence that the speaker was reusing
// a cached credential from another account. The ids were simply the two accounts
// they had actually used.
//
// The first attempt kept the line and cut an opaque id to a four-character tail.
// The reporter's answer to that was the right one: four characters of an opaque
// id are not a name either, and the line still serves no purpose to an owner.
//
// So the id does not go on screen at all now, in any length. What goes on screen
// is the account's DISPLAY NAME, which the speaker remembers the first time
// Spotify answers for that account and then keeps, so it survives the engine
// being logged in as somebody else, Spotify being unreachable, and a reboot.

// OPAQUE_MIN_LEN is where "this is a machine-generated id, not a name" starts.
// Spotify's canonical ids are 22 characters or more; the longest human usernames
// that show up in the field are well under this.
const OPAQUE_MIN_LEN = 16;

// looksOpaque reports whether an account string is a machine id rather than a
// name somebody chose: long, and nothing but lowercase letters and digits. A
// username with capitals, a dot, a dash, an underscore or an at-sign is a name.
export function looksOpaque(account) {
  const s = String(account || '').trim();
  return s.length >= OPAQUE_MIN_LEN && /^[a-z0-9]+$/.test(s);
}

// spotifyAccountName is what the tile prints about the account, or an empty
// string for nothing at all.
//
// names maps account id to the display name the speaker remembered. An old-style
// username is already a name and stands on its own when nothing was remembered.
//
// The id is never a fallback. That is the whole point of this function: an
// unknown account prints nothing, because nothing is better than an identifier
// the owner cannot read and a stranger can look up.
export function spotifyAccountName(account, names) {
  const id = String(account || '').trim();
  if (!id) return '';
  if (names && typeof names === 'object') {
    const remembered = String(names[id] || '').trim();
    if (remembered) return remembered;
  }
  if (!looksOpaque(id)) return id;
  return '';
}

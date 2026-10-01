// What a preset tile may show about the Spotify account it was saved under.
//
// The tile has carried the account since the Spotify-logo change, to tell apart
// presets belonging to different accounts. That is genuinely useful when the
// account is an old-style Spotify username, which is a name a person chose and
// recognises.
//
// Modern Spotify accounts do not have one. What arrives is the canonical user
// id, an opaque string like a25-character run of lowercase letters and digits.
// Printed on the tile it tells the owner nothing they can use, and it is a
// personal identifier: it went into a support mail on 2026-09-30 and was about
// to go into a public GitHub issue, in a screenshot, because the app put it on
// screen in readable green text.
//
// It also misled the reporter about what STR was doing. Two presets carried two
// different ids and she read the pair as evidence that the speaker was reusing a
// cached credential from another account. The ids were simply the two accounts
// she had actually used.
//
// So: a readable username still shows in full, because that is the case the
// feature exists for. An opaque id is reduced to a short tail, which still tells
// two accounts apart at a glance and is not an identifier anybody can look up or
// link to a person.

// OPAQUE_MIN_LEN is where "this is a machine-generated id, not a name" starts.
// Spotify's canonical ids are 22 characters or more; the longest human usernames
// that show up in the field are well under this.
const OPAQUE_MIN_LEN = 16;

// TAIL is how much of an opaque id survives. Four characters separate the
// accounts a person actually has without being a handle anybody can resolve.
const TAIL = 4;

// looksOpaque reports whether an account string is a machine id rather than a
// name somebody chose: long, and nothing but lowercase letters and digits. A
// username with capitals, a dot, a dash, an underscore or an at-sign is a name.
export function looksOpaque(account) {
  const s = String(account || '').trim();
  return s.length >= OPAQUE_MIN_LEN && /^[a-z0-9]+$/.test(s);
}

// spotifyAccountLabel is what the tile prints, or an empty string for nothing at
// all. Never returns the whole of an opaque id.
export function spotifyAccountLabel(account) {
  const s = String(account || '').trim();
  if (!s) return '';
  if (!looksOpaque(s)) return s;
  return '…' + s.slice(-TAIL);
}

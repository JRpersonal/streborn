// sourceinputs.js — the line inputs a speaker actually has, taken from the
// speaker's own source list.
//
// The music tab used to offer two fixed buttons, AUX and Bluetooth. That is
// every input a one-piece SoundTouch has and no input a soundbar has, so four
// owners across three chassis families were left with sockets they could not
// reach: a SoundTouch 300 with TV and HDMI (discussion #385 item 1), an SA-5
// whose three line inputs all arrive as source AUX and differ only in the
// account (#274), a CineMate 130 with TV, CBL-Sat, BD-DVD, Game and Aux on the
// back (#491), and discussion #577. The CineMate owner had to pick up the Bose
// infrared remote to get his television sound back once he had switched to
// radio, because the app offered him no way back.
//
// So the buttons come from what the speaker reports, and two rules that look
// obvious are wrong on hardware:
//
//   - status is NOT a capability. /sources status is a CONNECTION indicator: a
//     SoundTouch 10's Bluetooth says UNAVAILABLE simply because nothing is
//     paired to it, and an HDMI socket with the television switched off can be
//     expected to look the same. Filtering on status would hide exactly the
//     input the owner wants back.
//   - isLocal alone is not enough. QPlay reports isLocal=true on every box
//     measured (Portable, SoundTouch 10, SoundTouch 30 on 2026-08-09) and is a
//     network protocol, so taking isLocal at face value puts two buttons
//     labelled "QPlay1UserName" under everybody's inputs.
//
// What separates a socket from a service slot is the account: the firmware
// gives every service slot an account ending in "UserName" (QPlay1UserName,
// UPnPUserName, SpotifyConnectUserName, StoredMusicUserName) and a physical
// socket never has one. That rule needs no list of input names, which matters
// because the input names are the part that cannot be known in advance: a
// CineMate calls its analogue input LOCAL where a SoundTouch 10 says AUX, and a
// soundbar's sockets arrive as PRODUCT with the socket name in sourceAccount.
//
// The speaker's own phone page carries the same two rules in isPhysicalInput
// (internal/webui/assets/index.html); the agent validates the chosen source
// against the box's list before it builds the ContentItem.

import { tLookup } from './i18n/index.js';

// STR's own playback sources and the firmware's bookkeeping entries are not
// inputs a person can switch to. Everything else the box reports as local IS
// one, so this is a list of what is known NOT to be an input rather than a
// guess at what is.
const NOT_AN_INPUT = new Set([
  'UPNP', 'LOCAL_INTERNET_RADIO', 'STORED_MUSIC', 'STORED_MUSIC_MEDIA_SERVER',
  'STORED_MUSIC_MEDIA_RENDERER', 'STANDBY', 'INVALID_SOURCE', 'NOTIFICATION',
  'UPDATE', 'SETUP',
]);

const up = (v) => String(v ?? '').trim().toUpperCase();

// isPhysicalInput decides what becomes a button. Deliberately blind to status.
export function isPhysicalInput(src) {
  const name = up(src && src.source);
  if (!name || !(src && src.isLocal) || NOT_AN_INPUT.has(name)) return false;
  return !/username$/i.test(String((src && src.sourceAccount) || '').trim());
}

// physicalInputs keeps the speaker's own order, so a soundbar's buttons line up
// with the sockets on its back the way the firmware lists them.
export function physicalInputs(sources) {
  return (Array.isArray(sources) ? sources : []).filter(isPhysicalInput);
}

// inputName is what the button says. The speaker's own name for the socket wins,
// because that is what its owner called it when the system was set up: a
// CineMate reports "CBL-Sat" and the button should read CBL-Sat. Bluetooth stays
// untranslated like the other brand names in the UI.
function inputName(input) {
  const src = up(input.source);
  if (src === 'BLUETOOTH') return 'Bluetooth';
  const own = String(input.displayName || '').trim();
  if (own) return own;
  // LOCAL is the analogue socket under the name a CineMate 130 uses for it, so
  // it gets the AUX label rather than the raw enum.
  const translated = tLookup('source.label', src === 'LOCAL' ? 'AUX' : src);
  if (translated) return translated;
  const account = String(input.sourceAccount || '').trim();
  return account || (src === 'LOCAL' ? 'AUX' : src);
}

// fallbackInputs is what the row held before this existed: the two inputs a
// one-piece SoundTouch has, minus the ones a model is known not to have (the
// Portable has no Bluetooth, the Wave's own Aux is not reachable through the
// SoundTouch pedestal, #417). A speaker too slow to answer /sources keeps the
// inputs it has always had rather than losing them to a timeout.
function fallbackInputs(model) {
  const m = String(model || '');
  const out = [];
  if (!/wave/i.test(m)) out.push({ source: 'AUX', sourceAccount: 'AUX', isLocal: true });
  if (!/portable/i.test(m)) out.push({ source: 'BLUETOOTH', sourceAccount: '', isLocal: true });
  return out;
}

// inputButtons turns a speaker's source list into the buttons to draw:
// {source, sourceAccount, label, bluetooth}. source and sourceAccount are sent
// back to the speaker verbatim, which is what lets the agent confirm them
// against the box's own list.
export function inputButtons(sources, model) {
  // Only a non-empty list counts as an answer. A speaker that did answer and
  // named no socket has none to offer: the Wave's own Aux is not reachable
  // through the SoundTouch pedestal (#417), and falling back there would put a
  // button on screen that cannot work.
  const answered = Array.isArray(sources) && sources.length > 0;
  const inputs = answered ? physicalInputs(sources) : fallbackInputs(model);
  const out = inputs.map((x) => ({
    source: up(x.source),
    sourceAccount: String(x.sourceAccount || '').trim(),
    label: inputName(x),
    bluetooth: up(x.source) === 'BLUETOOTH',
  }));
  // An SA-5 answers with three AUX entries that differ only in the account and
  // gives them no separate name, so all three buttons would read "Aux input"
  // (#274). Name the socket only where two labels really collide, so a soundbar
  // whose sockets the firmware does name is not turned into "TV (TV)".
  const count = new Map();
  for (const b of out) count.set(b.label, (count.get(b.label) || 0) + 1);
  for (const b of out) {
    if (count.get(b.label) > 1 && b.sourceAccount) b.label = `${b.label} (${b.sourceAccount})`;
  }
  return out;
}

// sourceRowKey identifies the row as drawn, so a refresh that changes nothing
// leaves the buttons alone: redrawing them every minute takes a pressed state
// and the mouse focus with it.
//
// The speaker is part of the key even when its inputs look the same, because a
// button the speaker refused with 1005 UNKNOWN_SOURCE_ERROR is hidden on that
// button, and it has to come back for the next speaker (#417: it stayed hidden
// for every box until the app was restarted).
export function sourceRowKey(host, buttons) {
  return String(host || '') + ' ' + JSON.stringify(buttons || []);
}

// isActiveInput reports whether a button is the input the speaker is currently
// on, for the green highlight.
//
// AUX and LOCAL are the same socket under the two names the firmware uses for
// it, so either name lights the button the speaker offered (#491). The account
// only decides when both sides name one: on a box with three AUX sockets it is
// the only thing that tells them apart, while a speaker reporting no account
// must still light its single AUX button.
export function isActiveInput(button, nowSource, nowAccount) {
  const analogue = (v) => (v === 'LOCAL' ? 'AUX' : v);
  const want = analogue(up(button && button.source));
  const playing = analogue(up(nowSource));
  if (!want || want !== playing) return false;
  const wantAccount = up(button && button.sourceAccount);
  const playingAccount = up(nowAccount);
  if (!wantAccount || !playingAccount) return true;
  return wantAccount === playingAccount;
}

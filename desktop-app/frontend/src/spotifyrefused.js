// spotifyrefused.js: the short notice that Spotify refuses the speaker's
// account on (STR) (discussion #1671).
//
// Some accounts never play through STR's Spotify engine: free accounts, and
// Premium accounts Spotify moved to its newer DRM. The speaker remembers which
// account it was refused for until that account plays (the agent's
// accountRefused, internal/spotify/accountrefused.go), so the notice stays up
// for as long as it is true instead of flashing past once as a toast while the
// app happened to be open.
//
// An older agent has only audioKeyRefused, its ten-minute flag. That feeds the
// same notice, so there is one message whichever agent answers.
//
// The notice says why and what to try, not only that it happened (GH #78,
// #1426, #1648, #1636, #973, discussion #311). The two causes get different
// words, because the cure differs:
//
//   free     the speaker says the account is a free one. Spotify on (STR)
//            needs Premium; upgrading (then a restart) or another Premium
//            account is the way on. Nothing is claimed about the speaker's
//            own Spotify entry here, it is not known to play free accounts.
//   premium  everything else, including "plan unknown". Spotify refuses some
//            accounts on unlicensed players, usually newer ones, even on
//            Premium (docs/streaming/spotify.md, "Known limitation"). The
//            speaker's own entry is a licensed client and keeps playing it.
//            The decisive test is another, older account on the (STR) entry:
//            if that one plays, the speaker is fine and it is the account.
//            The cutoff is not known, so the words say "usually".

import { escapeHtml } from './utils.js';

// isFreePlan reads the plan from one /spotify/info reply: the explicit
// product when the speaker knows it, otherwise its premiumRequired flag.
function isFreePlan(np) {
  const product = String((np && np.product) || '').toLowerCase();
  if (product === 'premium') return false;
  return product === 'free' || product === 'open' || !!(np && np.premiumRequired);
}

// refusalFromReply turns one /spotify/info reply into what the notice needs,
// or null when there is nothing to say.
export function refusalFromReply(np) {
  if (!np || !(np.accountRefused || np.audioKeyRefused)) return null;
  return {
    name: np.accountRefused ? String(np.accountRefusedName || '').trim() : '',
    free: isFreePlan(np),
  };
}

// refusalText is the notice itself: with the account's name when the speaker
// knows it, otherwise about "this account".
export function refusalText(ref, t) {
  if (!ref) return '';
  if (ref.free) {
    return ref.name ? t('spotify.accountRefusedFreeNamed', { account: ref.name }) : t('spotify.accountRefusedFree');
  }
  return ref.name ? t('spotify.accountRefusedNamed', { account: ref.name }) : t('spotify.accountRefused');
}

// refusalHelp is the "why" and the "what you can try" under the notice.
export function refusalHelp(ref, t) {
  if (!ref) return { why: '', tryText: '' };
  return ref.free
    ? { why: t('spotify.refusedFreeWhy'), tryText: t('spotify.refusedFreeTry') }
    : { why: t('spotify.refusedWhy'), tryText: t('spotify.refusedTry') };
}

// refusalHTML is the notice with its why and what-to-try folded away under a
// short toggle, so the now-playing area stays one line until someone asks.
// opts.lead overrides the first line (the UI 2.0 accounts row names the
// account itself, so it uses the unnamed form).
export function refusalHTML(ref, t, opts = {}) {
  if (!ref) return '';
  const lead = opts.lead !== undefined ? opts.lead : refusalText(ref, t);
  const help = refusalHelp(ref, t);
  return `<span class="spotify-refused-lead">${escapeHtml(lead)}</span>`
    + `<details class="spotify-refused-more"><summary>${escapeHtml(t('spotify.refusedMore'))}</summary>`
    + `<p class="spotify-refused-why">${escapeHtml(help.why)}</p>`
    + `<p class="spotify-refused-try">${escapeHtml(help.tryText)}</p></details>`;
}

// strSpotifyLocation reports whether the speaker plays from STR's own Spotify
// engine: the engine's stream, or a container location that is not the
// speaker's own Spotify receiver. Same predicate the status poll asks the
// engine under, so the notice shows exactly where its answer comes from.
export function strSpotifyLocation(loc, boxSpotify) {
  return /\/spotify\/stream|\/playback\/container/.test(loc || '') && !boxSpotify;
}

// refusalFor is the refusal the now-playing area shows, null for none: only
// while the selected speaker plays from STR's Spotify, and only from an answer
// that speaker gave.
export function refusalFor(st) {
  const ref = st && st.spotifyRefusal;
  const box = st && st.currentBox;
  if (!ref || !box || ref.host !== box.host || ref.port !== box.port) return null;
  if (!strSpotifyLocation(st.nowLocation, st.nowBoxSpotify)) return null;
  return ref;
}

// refusalNoticeFor is the text the now-playing area leads with, '' for none.
export function refusalNoticeFor(st, t) {
  return refusalText(refusalFor(st), t);
}

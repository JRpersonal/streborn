// What the world-map invite actually asks the user to do.
//
// The invite's headline only states a fact ("your SoundTouch is alive again").
// The one sentence that ASKS for a pin lived in the count line, and the count
// line was shown only when a live fetch from the website returned a number above
// zero. That fetch returns 0 on every failure: no network, blocked DNS, a
// six-second timeout, a non-200, unparseable JSON. All of it silent.
//
// So a user whose machine could not reach the site got confetti and a button,
// and was never actually asked to add their pin. That is why the prompt appeared
// unreliably (Jens, 2026-09-28).
//
// The ask no longer depends on the network. The count is what it always should
// have been: a nicer version of the same sentence when the number happens to
// arrive.

// pinPromptKey returns the i18n key for the prompt line given the rescued-speaker
// count, and whether that line needs the count interpolated.
//
// n is whatever the count fetch produced: a number, 0, null, undefined, or
// something not a number at all when the binding misbehaves. Anything that is
// not a usable positive integer falls back to the plain ask, because the ask
// must always be there.
export function pinPromptKey(n) {
  const usable = typeof n === 'number' && Number.isFinite(n) && n > 0;
  return usable
    ? { key: 'worldMap.countLine', params: { n: Math.floor(n) } }
    : { key: 'worldMap.pinPrompt', params: undefined };
}

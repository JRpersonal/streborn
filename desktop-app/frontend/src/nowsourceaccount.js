// Which socket of a multi-input speaker is playing.
//
// A speaker with three analogue inputs reports them all as source="AUX" and
// says WHICH one only in the account. The input row needs that to light one
// button instead of three, and the rule that decides it treats an empty account
// as matching everything, which is right for a speaker with a single socket and
// wrong for a speaker with three.
//
// The account is not always in the same place. Compare two documents from the
// same diagnostic:
//
//   <nowPlaying source="UPNP" sourceAccount="UPnPUserName">
//     <ContentItem source="UPNP" sourceAccount="UPnPUserName" ...>
//
//   <nowPlaying source="AUX">
//     <ContentItem source="AUX" sourceAccount="AUX1" isPresetable="true">
//       <itemName>Phono</itemName>
//
// UPnP carries it on both elements. AUX carries it only on the ContentItem. So
// reading the outer element alone, which is what the app did, finds nothing for
// exactly the speaker that needs it, and all three buttons light at once.
//
// Reported on an SA-5 amplifier (#274) and confirmed from its owner's
// diagnostic, which is where both shapes above are copied from.

// sourceAccountFrom reads the playing socket out of a /now_playing document.
//
// The outer element wins when it has one, because that is the speaker's own
// summary of what is playing. The ContentItem is the fallback, not the other
// way round: a document can carry several ContentItems and only the outer
// element is guaranteed to describe the CURRENT source.
export function sourceAccountFrom(xml) {
  const s = String(xml || '');
  // Anchored on the opening tag and on a space before the attribute name, so
  // nothing matches "sourceAccount" inside a later element or inside a value.
  // [^>] cannot cross the end of the tag, which is what keeps the outer read
  // from picking the ContentItem's up by accident.
  const outer = /<nowPlaying[^>]*\ssourceAccount="([^"]*)"/.exec(s);
  if (outer && outer[1]) return outer[1];
  const item = /<ContentItem[^>]*\ssourceAccount="([^"]*)"/.exec(s);
  if (item && item[1]) return item[1];
  return '';
}

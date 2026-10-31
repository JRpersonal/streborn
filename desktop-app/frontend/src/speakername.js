// speakername.js: speaker names the firmware cannot survive (#1582).
//
// The speaker's UPnP renderer pastes its name unescaped into its device
// description. A name with "&" (or "<", ">", or stored as invalid UTF-8)
// takes the renderer down on the next boot, and Spotify and the music library
// both play through it, so neither works while native radio does. This mirrors
// the Go rule in speakername/speakername.go, which the agent and the app's Go
// side also apply; the copy here only lets the field show the stored name
// straight away.

// sanitizeSpeakerName: "&" becomes "+", "<" and ">" are dropped, control
// characters are dropped, white space is trimmed. (Invalid UTF-8 cannot reach
// a JS string; the Go side handles it.)
export function sanitizeSpeakerName(name) {
  return String(name || '')
    .replace(/&/g, '+')
    .replace(/[<>]/g, '')
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u001f\u007f-\u009f]/g, '')
    .trim();
}

// boxHasUnsafeName: the agent says so, or (for an agent older than the flag)
// the name the app knows carries one of the characters.
export function boxHasUnsafeName(box) {
  if (!box || box.offline || box.kind === 'stock') return false;
  if (box.speakerNameUnsafe) return true;
  const name = box.friendlyName || '';
  return /[&<>]/.test(name);
}

// unsafeNameBoxes lists the speakers the banner names.
export function unsafeNameBoxes(boxes) {
  return (boxes || []).filter(boxHasUnsafeName);
}

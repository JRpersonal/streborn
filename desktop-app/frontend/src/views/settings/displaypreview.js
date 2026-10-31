// displaypreview.js: which example picture belongs to the display settings
// as they stand. track and clock are true, false, or null (unknown); mode is
// the "Show" choice. '' means no picture: with both off the speaker shows its
// own screen, which STR does not draw.
export function displayPreviewName(track, mode, clock) {
  const m = mode === 'artist' || mode === 'title' ? mode : 'both';
  if (track === true) return clock === true ? `${m}-clock` : m;
  if (clock === true) return 'clock';
  return '';
}

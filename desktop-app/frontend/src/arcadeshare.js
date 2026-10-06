// Sharing a hidden game's score: copy buttons for the last round's final
// screen (as an image, score included) and for a score line, plus the
// announcement thread.
// Separate buttons because GitHub's paste takes either an image, which it
// uploads by itself, or text: an image and text pasted together lose the text.

// What each game's second number counts, in the score line.
const COUNTS = { blockfall: 'rows', starguard: 'waves' };

// scoreText is the line the player pastes into the thread, for one game from
// arcadeState. English on purpose: the thread is read by everyone.
export function scoreText(game, model) {
  const where = model ? `the ${model}` : 'my SoundTouch';
  const unit = COUNTS[game.id] || 'rows';
  return `My ${game.title} highscore on ${where}: **${game.best} points**. Last round: ${game.last} points, ${game.rows} ${unit}.`;
}

// pngBlob turns the PNG data URL from arcadeState into a Blob, without an
// await, so the clipboard write still runs inside the click (Safari's
// WKWebView refuses a clipboard write after the user gesture has passed).
export function pngBlob(dataURL) {
  const m = /^data:image\/png;base64,([A-Za-z0-9+/=]+)$/.exec(dataURL || '');
  if (!m) return null;
  const bin = atob(m[1]);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new Blob([bytes], { type: 'image/png' });
}

// copyScreenshot puts the screenshot on the clipboard. Resolves true when it
// is there, false where the webview cannot hold an image (then the app saves
// the file instead).
export async function copyScreenshot(dataURL, nav = globalThis.navigator, Item = globalThis.ClipboardItem) {
  const blob = pngBlob(dataURL);
  if (!blob || !nav?.clipboard?.write || typeof Item !== 'function') return false;
  try {
    await nav.clipboard.write([new Item({ 'image/png': blob })]);
    return true;
  } catch {
    return false;
  }
}

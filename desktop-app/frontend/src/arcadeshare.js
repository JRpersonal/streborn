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

// A new highscore gets a notice in the app. The agent reports when it saved
// its last round (box.arcadeAt, unix seconds) in the version answer the app
// probes every minute anyway, so the scores are fetched only after a new
// round. Remembered per speaker on this computer; the first sighting of a
// speaker only sets the baseline, unless that round is fresh.
const SEEN_KEY = 'str-arcade-seen';
const FRESH_SEC = 15 * 60;

function readSeen(storage) {
  try { return JSON.parse(storage.getItem(SEEN_KEY) || '{}') || {}; } catch { return {}; }
}
function writeSeen(storage, seen) {
  try { storage.setItem(SEEN_KEY, JSON.stringify(seen)); } catch { /* storage blocked: no notices */ }
}

// roundIsNew decides from the box record whether a round happened since the
// last look. Returns the timestamp to remember, or 0 for nothing new.
export function roundIsNew(box, seen, nowSec) {
  const at = Number(box && box.arcadeAt);
  if (!Number.isFinite(at) || at <= 0) return 0;
  const key = box.deviceID || box.host;
  if (!key) return 0;
  const prev = seen[key];
  if (prev === undefined) return nowSec - at < FRESH_SEC ? at : -at;
  return at > prev ? at : 0;
}

// newHighscore picks the game of the latest round from a GetArcade answer and
// says whether that round set the highscore.
export function newHighscore(list) {
  const games = (Array.isArray(list) ? list : []).filter((g) => g && Number(g.rounds) > 0 && g.lastAt);
  if (!games.length) return null;
  const g = games.reduce((a, b) => (Date.parse(b.lastAt) > Date.parse(a.lastAt) ? b : a));
  if (g.bestAt !== g.lastAt || Number(g.best) !== Number(g.last) || !(Number(g.best) > 0)) return null;
  const title = String(g.title || g.id);
  return { id: g.id, title: title.charAt(0) + title.slice(1).toLowerCase(), best: Number(g.best) };
}

// announceHighscores runs after every refresh of the speaker list.
export async function announceHighscores(boxes, deps) {
  const { getArcade, toast, t, storage, nowSec } = deps;
  const seen = readSeen(storage);
  let changed = false;
  for (const box of boxes || []) {
    const at = roundIsNew(box, seen, nowSec);
    if (!at) continue;
    seen[box.deviceID || box.host] = Math.abs(at);
    changed = true;
    if (at < 0) continue; // baseline only
    try {
      const hs = newHighscore(await getArcade(box.host, box.port));
      if (hs) {
        toast(t('settingsView.arcadeHighscoreToast', {
          game: hs.title, best: hs.best, name: box.friendlyName || box.name || box.host,
        }), 8000);
      }
    } catch { /* the speaker did not answer: the section still shows it later */ }
  }
  if (changed) writeSeen(storage, seen);
}

// A folder played from the Library is remembered as ONE Recently-played card,
// and that card stores only its FIRST track's URL. Replaying it sent that single
// URL through PlayURL, which clears the agent queue by design, so the folder
// played one song and the speaker then stopped with the indicator amber (#978,
// reported 2026-09-26 on v0.9.87).
//
// The card key carries the media server and the container, so the speaker can
// rebuild the whole queue. What is tested here is the decision around that call:
// only folder cards take the new path, a speaker that cannot do it yet still
// replays the single track, and a REAL refusal is shown rather than hidden behind
// a fallback that would look exactly like the old bug.
import { describe, it, expect, vi } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'views', 'recent.js'), 'utf8').replace(/\r\n/g, '\n');

// The real function, not a copy of it: slice it out of the view and give it the
// four things it reaches for.
const at = src.indexOf('async function replayFolderCard');
const fnSrc = src.slice(at, src.indexOf('\n}\n', at) + 2);
const MISSING = 'missing_binding';

function build(replay) {
  const showToast = vi.fn();
  const showError = vi.fn();
  const t = (k, v) => `${k}:${(v && v.name) || ''}`;
  const isMissingBinding = (e) => String((e && e.message) || e).includes(MISSING);
  // eslint-disable-next-line no-new-func
  const make = new Function('ReplayFolderCard', 'showToast', 'showError', 't', 'isMissingBinding',
    `${fnSrc}\nreturn replayFolderCard;`);
  return { fn: make(replay, showToast, showError, t, isMissingBinding), showToast, showError };
}

const BOX = { host: '192.0.2.10', port: 17008 };
const folderCard = { cardKey: 'queue:uuid:AAAA-BBBB:64$0', name: 'Jazz', art: 'http://nas/art.png' };

describe('replaying a Recently-played folder card', () => {
  it('asks the speaker to rebuild the whole folder', async () => {
    const replay = vi.fn().mockResolvedValue(undefined);
    const { fn, showToast } = build(replay);
    expect(await fn(folderCard, BOX)).toBe(true);
    expect(replay).toHaveBeenCalledWith('192.0.2.10', 17008, 'queue:uuid:AAAA-BBBB:64$0', 'Jazz', 'http://nas/art.png');
    expect(showToast).toHaveBeenCalled();
  });

  it('leaves a single track alone', async () => {
    // A radio card and a NAS single-track card must keep the play path they have.
    const replay = vi.fn();
    const { fn } = build(replay);
    expect(await fn({ cardKey: 'http://stream.example/1', name: 'FFN' }, BOX)).toBe(false);
    expect(await fn({ cardKey: 'spotify:playlist:37i9dQ' }, BOX)).toBe(false);
    expect(replay).not.toHaveBeenCalled();
  });

  it('falls back when the speaker cannot do it yet', async () => {
    // Two shapes of "older speaker": the binding is missing from this build, and
    // the agent answered 404. Both mean replay the one stored track, as before.
    const missing = build(vi.fn().mockRejectedValue(new Error(`${MISSING}: ReplayFolderCard is not available in this build`)));
    expect(await missing.fn(folderCard, BOX)).toBe(false);
    expect(missing.showError).not.toHaveBeenCalled();

    const old = build(vi.fn().mockRejectedValue(new Error('folder_replay_unsupported')));
    expect(await old.fn(folderCard, BOX)).toBe(false);
    expect(old.showError).not.toHaveBeenCalled();
  });

  it('shows a real refusal instead of quietly playing one track', async () => {
    // The NAS is off, or the folder is gone. Playing the first track here would
    // reproduce the reported symptom and blame the speaker for it.
    const { fn, showError } = build(vi.fn().mockRejectedValue(new Error('the music server Synology is not reachable right now')));
    expect(await fn(folderCard, BOX)).toBe(true);
    expect(showError).toHaveBeenCalled();
  });

  it('is asked before the single-track replay runs', () => {
    // The order is the fix. If PlayURL ran first the queue would be cleared.
    const branch = src.slice(src.indexOf("} else if (c.source === 'upnp') {"));
    const askedAt = branch.indexOf('if (await replayFolderCard(c, box)) return;');
    const playedAt = branch.indexOf('await PlayURL(');
    expect(askedAt).toBeGreaterThan(-1);
    expect(askedAt).toBeLessThan(playedAt);
  });
});

// Discussion #709 read "this station is already on key 1" as a bug; discussion
// #925 read it as "so I can only keep one Spotify playlist" and stopped at one
// playlist. The refusal is correct and stays (#836: silently deleting the other
// key wiped users' presets), so what was missing is the third option: move the
// station to the key that was just pressed.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { parsePresetConflict, presetConflictNote, offerPresetMove } from './presetmove.js';

const dir = fileURLToPath(new URL('./i18n/bundles/', import.meta.url));
const langs = readdirSync(dir).filter((f) => f.endsWith('.json')).map((f) => f.replace('.json', ''));
const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));

// t renders a real bundle so the assertions read like what the user sees.
const translator = (l = 'en') => {
  const b = bundle(l);
  return (key, vals) => String(b[key] || key).replace(/\{\{(\w+)\}\}/g, (_, k) => (vals && vals[k] != null ? String(vals[k]) : ''));
};

// harness drives the offer with a scripted answer and records what happened.
function harness({ answer, moveFails }) {
  const calls = { confirms: [], moves: [], toasts: [], fails: [], repaints: 0 };
  const run = (conflict, toSlot) => offerPresetMove({
    conflict,
    toSlot,
    t: translator(),
    confirm: (title, body, opts) => { calls.confirms.push({ title, body, opts }); return Promise.resolve(answer); },
    move: (from, to) => {
      calls.moves.push([from, to]);
      return moveFails ? Promise.reject(new Error('status 500')) : Promise.resolve();
    },
    toast: (m) => calls.toasts.push(m),
    fail: (m) => calls.fails.push(m),
    done: () => { calls.repaints++; return Promise.resolve(); },
  });
  return { calls, run };
}

describe('the already-on-another-key refusal', () => {
  it('reads the other key and its station out of the agent 409', () => {
    const c = parsePresetConflict('status 409: {"code":"already-on-slot","slot":1,"name":"Rock Classics"}');
    expect(c).toEqual({ slot: 1, name: 'Rock Classics' });
    // A name carrying a quote, which is what breaks a naive match.
    expect(parsePresetConflict('{"code":"already-on-slot","slot":2,"name":"Rock \\"Live\\""}').name).toBe('Rock "Live"');
    // No name in the body: the key is still usable, the name simply is not.
    expect(parsePresetConflict('{"code":"already-on-slot","slot":3}')).toEqual({ slot: 3, name: '' });
    // Every other failure must fall through to the normal error path.
    expect(parsePresetConflict('status 422: {"code":"stream-url-missing"}')).toBeNull();
    expect(parsePresetConflict(new Error('connection refused'))).toBeNull();
    expect(parsePresetConflict(null)).toBeNull();
  });

  it('still names what the station collided with', () => {
    const t = translator();
    expect(presetConflictNote({ slot: 4, name: 'Jens Chill' }, t)).toContain('Jens Chill');
    expect(presetConflictNote({ slot: 4, name: 'Jens Chill' }, t)).toContain('key 4');
    // Without a name it must not print an empty pair of quotes.
    expect(presetConflictNote({ slot: 4, name: '' }, t)).not.toContain('""');
  });
});

describe('the offer to move the station', () => {
  it('moves it from the old key to the new one when the user accepts', async () => {
    const { calls, run } = harness({ answer: true });
    const moved = await run({ slot: 1, name: 'Jens Chill' }, 3);
    expect(moved).toBe(true);
    expect(calls.moves).toEqual([[1, 3]]);
    // The button has to say where it is going, or "move" is a guess.
    expect(calls.confirms[0].opts.confirmLabel).toBe('Move it to key 3');
    // The note the user reads before deciding is the refusal itself.
    expect(calls.confirms[0].body).toContain('Jens Chill');
    expect(calls.confirms[0].body).toContain('key 1');
    expect(calls.toasts[0]).toContain('key 3');
    expect(calls.repaints).toBe(1);
  });

  it('leaves both keys alone when the user declines, and still says why', async () => {
    const { calls, run } = harness({ answer: false });
    const moved = await run({ slot: 1, name: 'Jens Chill' }, 3);
    expect(moved).toBe(false);
    expect(calls.moves).toEqual([]);
    expect(calls.repaints).toBe(0);
    // The plain refusal stays: it is what the user saw before the offer existed.
    expect(calls.toasts).toHaveLength(1);
    expect(calls.toasts[0]).toContain('Jens Chill');
    expect(calls.toasts[0]).toContain('key 1');
  });

  it('says so when the speaker could not move it', async () => {
    const { calls, run } = harness({ answer: true, moveFails: true });
    const moved = await run({ slot: 2, name: 'Jens Chill' }, 5);
    expect(moved).toBe(false);
    expect(calls.fails).toHaveLength(1);
    expect(calls.fails[0]).toContain('status 500');
    // Nothing may claim success, above all not the repaint's "saved" feel.
    expect(calls.toasts).toEqual([]);
    expect(calls.repaints).toBe(0);
  });

  it('does not offer a move onto the key the station is already on', async () => {
    const { calls, run } = harness({ answer: true });
    expect(await run({ slot: 3, name: 'Jens Chill' }, 3)).toBe(false);
    expect(calls.confirms).toEqual([]);
    expect(calls.moves).toEqual([]);
    // Still say something: a silent save is how a user ends up pressing again.
    expect(calls.toasts).toHaveLength(1);
  });

  it('asks calmly: nothing went wrong, the speaker wants a decision', async () => {
    const { calls, run } = harness({ answer: false });
    await run({ slot: 1, name: 'Jens Chill' }, 2);
    expect(calls.confirms[0].opts.icon).toBeNull();
    expect(calls.confirms[0].opts.calm).toBe(true);
    expect(calls.confirms[0].opts.confirmClass).not.toContain('danger');
  });
});

describe('the move strings', () => {
  it('are in every language with their placeholders', () => {
    for (const l of langs) {
      const b = bundle(l);
      expect(b['preset.moveTitle'], l).toBeTruthy();
      expect(b['preset.moveButton'], l + ' lost the key number').toContain('{{n}}');
      expect(b['preset.movedToKey'], l + ' lost the new key').toContain('{{n}}');
      expect(b['preset.movedToKey'], l + ' lost the freed key').toContain('{{from}}');
      expect(b['preset.moveFailed'], l + ' lost the reason').toContain('{{err}}');
      // House rule: no dash used as punctuation in user-facing text.
      for (const k of ['preset.moveTitle', 'preset.moveButton', 'preset.movedToKey', 'preset.moveFailed']) {
        expect(b[k], l + ' ' + k).not.toMatch(/\s[-–—]\s/);
      }
    }
    // German keeps its umlauts.
    expect(bundle('de')['preset.moveButton']).toBe('Auf Taste {{n}} verschieben');
  });
});

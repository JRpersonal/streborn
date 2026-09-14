import { describe, it, expect } from 'vitest';

// Juergen's eleven speakers, 2026-09-14. A seven-box group on a SoundTouch 30.
// Nine dissolves in a row, from the group frame's x, from the Multiroom tab,
// from the phone page, and twice after restarting the app. Every one of them
// showed a green "Group dissolved" and every one of them left the group
// playing; only pulling the master's power ended it.
//
// The agent had said so each time. handleZoneDissolve answers ok:false with a
// "remaining" count when the speaker still reports members, and two of the
// three call sites threw that answer away and painted the zone empty anyway.
//
// Re-implemented here rather than imported, as errornoise.test.js does: main.js
// starts the whole application at module load. What matters is the SHAPE of the
// decision, and painting a refusal as success is the shape that cost the trust.

const classify = (res) => {
  if (res && res.ok === false) return 'incomplete';
  if (res && res.nothing) return 'nothing-to-do';
  return 'dissolved';
};

// A refusal must never apply the optimistic empty zone: that is what made the
// group frame disappear and come back on the next refresh.
const paintsEmpty = (res) => classify(res) !== 'incomplete';

const message = (res, t) => {
  const left = res && Number(res.remaining) > 0 ? Number(res.remaining) : 0;
  return left ? t('multiroom.dissolveIncompleteN', { n: left }) : t('multiroom.dissolveIncomplete');
};

describe('what the app does with the speaker verdict on a dissolve', () => {
  it('calls a refusal a refusal, and does not empty the group on screen', () => {
    // Verbatim shape of the agent answer in his bundle.
    const refused = { ok: false, remaining: 6, error: 'the speaker still reports members in the group' };
    expect(classify(refused)).toBe('incomplete');
    expect(paintsEmpty(refused)).toBe(false);
  });

  it('says how many speakers are still in it', () => {
    const t = (k, v) => `${k}:${v ? v.n : ''}`;
    expect(message({ ok: false, remaining: 6 }, t)).toBe('multiroom.dissolveIncompleteN:6');
    // No count from an older agent: fall back, never print "undefined speakers".
    expect(message({ ok: false }, t)).toBe('multiroom.dissolveIncomplete:');
    expect(message({ ok: false, remaining: 0 }, t)).toBe('multiroom.dissolveIncomplete:');
  });

  it('still reports a real dissolve as done', () => {
    for (const res of [{ ok: true }, { ok: true, remaining: 0 }, {}, null, undefined]) {
      expect(classify(res)).toBe('dissolved');
      expect(paintsEmpty(res)).toBe(true);
    }
  });

  it('keeps the there-was-no-group case separate (#843)', () => {
    const res = { ok: true, nothing: true };
    expect(classify(res)).toBe('nothing-to-do');
    // Nothing to take apart is still a correct empty state.
    expect(paintsEmpty(res)).toBe(true);
  });

  it('a refusal outranks nothing:true, so an odd answer is never a green tick', () => {
    expect(classify({ ok: false, nothing: true, remaining: 2 })).toBe('incomplete');
  });
});

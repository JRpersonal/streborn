// A badge carries the colour of the state that produced it. The Multi-Room
// count was always brand, while a saved group that is not currently formed is
// drawn as a dashed muted frame, so a count made only of those sat in blue over
// a grey frame and the two did not read as one thing.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { groupCountSplit, groupCount } from './groups.js';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');

const BOXES = [
  { deviceID: 'AAA', host: '192.0.2.10', kind: 'str' },
  { deviceID: 'BBB', host: '192.0.2.11', kind: 'str' },
];

describe('the Multi-Room count badge', () => {
  it('splits a live group from a saved one, and still totals like groupCount', () => {
    const live = { AAA: { master: 'AAA', members: [{ deviceID: 'BBB', ip: '192.0.2.11' }] } };
    const s = groupCountSplit(live, BOXES);
    expect(s.live).toBe(1);
    expect(s.stored).toBe(0);
    expect(s.total).toBe(groupCount(live, BOXES));
  });

  it('counts a saved group nobody is playing as stored, not live', () => {
    const saved = { AAA: { master: 'AAA', permanent: true, members: [{ deviceID: 'BBB', ip: '192.0.2.11' }] } };
    const s = groupCountSplit(saved, BOXES);
    expect(s.total).toBe(groupCount(saved, BOXES));
    expect(s.live + s.stored).toBe(s.total);
  });

  it('reports nothing when there is nothing', () => {
    const s = groupCountSplit({}, BOXES);
    expect(s).toEqual({ live: 0, stored: 0, total: 0 });
  });

  it('wears the muted class only while every counted group is a saved one', () => {
    const fn = main.slice(main.indexOf('function updateMultiroomTabBadge'), main.indexOf('onZoneLive(updateMultiroomTabBadge)'));
    expect(fn).toContain('tab-badge-stored');
    expect(fn).toContain('split.live === 0');
    expect(fn).toContain('n > 0');
  });

  it('has the muted style, and it matches the frame it refers to', () => {
    expect(css).toContain('.tab-badge-stored');
    const rule = css.slice(css.indexOf('.tab-badge-stored'), css.indexOf('.tab-badge-stored') + 320);
    expect(rule).toContain('--c-muted');
    expect(rule).toContain('dashed');
    // .box-group-stored is the frame a saved group is drawn with. Match the
    // DECLARATION, not the prose above it that names the same selector.
    const at = css.indexOf('.box-group-stored {');
    expect(at, 'the saved-group frame rule is gone').toBeGreaterThan(-1);
    const frame = css.slice(at, at + 120);
    expect(frame).toContain('dashed');
    expect(frame).toContain('--c-muted');
  });
});

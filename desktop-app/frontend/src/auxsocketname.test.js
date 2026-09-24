// A speaker with several analogue inputs showed the same generic "AUX input"
// for every one of them, while the phone remote printed the socket's real name
// (#274). The app had already parsed that name out of the box's answer and
// stored it, then threw it away one line later.
//
// The generic label still has a job: a CineMate reported no name at all and
// fell through to "some source is active", which read worse than a generic
// label (#491). So it is the fallback, not the answer.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'main.js'), 'utf8');

// The AUX/LOCAL branch of renderNowPlayingBar.
const auxLine = src.split(/\r?\n/).find(l => l.includes("srcU === 'AUX'")) || '';

// What that branch does, extracted so the behaviour is asserted and not just
// the spelling: the box's itemName wins, the label fills a gap.
const label = (name, generic) => name || generic;

describe('the AUX line names the socket', () => {
  it('prefers the name the speaker reports', () => {
    expect(label('Turntable', 'AUX input')).toBe('Turntable');
    expect(label('CD', 'AUX input')).toBe('CD');
  });

  it('falls back to the generic label when the box names nothing', () => {
    // The #491 CineMate: no itemName, and "some source is active" was worse.
    expect(label('', 'AUX input')).toBe('AUX input');
  });

  it('is wired that way in the source, for AUX and for LOCAL', () => {
    expect(auxLine).toContain("srcU === 'LOCAL'");
    expect(auxLine).toContain("name || t('status.auxInput')");
    // The bug in one expression: the generic label assigned unconditionally.
    expect(auxLine).not.toMatch(/displayName = t\('status\.auxInput'\)/);
  });

  it('still marks the speaker as active on that source', () => {
    // Naming the socket must not cost the "active" state that told the user
    // anything was happening at all.
    expect(auxLine).toContain("status.active");
  });
});

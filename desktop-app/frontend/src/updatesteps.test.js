import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// "Restarting" looks like the finish line and is roughly halfway.
//
// The order a speaker actually goes through is: send the software, restart,
// confirm, then the ~16 MB Spotify engine. A row reading "Rebooting (usually
// 2-4 min)" with a full bar is the longest-lived phase and the most convincing
// impression of being done, and an owner who closes the app there leaves the
// speaker on the new version with no engine. That is not hypothetical: it is
// what happened to one speaker in #963.
//
// Jens, 2026-09-15, on being offered a Close button at that moment: "könnte ein
// unaufmerksamer user meinen das es fertig sei und schließt das fenster und
// dann eventuell auch die app".

const src = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
const en = JSON.parse(readFileSync(new URL('./i18n/bundles/en.json', import.meta.url), 'utf8'));
const de = JSON.parse(readFileSync(new URL('./i18n/bundles/de.json', import.meta.url), 'utf8'));

describe('the phases say where they are in the sequence', () => {
  it('has a step marker in every language', () => {
    expect(en['updateAll.phase.step']).toContain('{{n}}');
    expect(en['updateAll.phase.step']).toContain('4');
    expect(de['updateAll.phase.step']).toContain('{{n}}');
  });

  it('numbers restart as 2 and the engine as 4, not the other way round', () => {
    const at = src.indexOf('const UPDATE_STEPS');
    expect(at).toBeGreaterThan(-1);
    const decl = src.slice(at, at + 200);
    expect(decl).toMatch(/send:\s*1/);
    expect(decl).toMatch(/restart:\s*2/);
    expect(decl).toMatch(/confirm:\s*3/);
    expect(decl).toMatch(/engine:\s*4/);
  });

  it('marks the restart phase, which is the one that misleads', () => {
    // Both the single-speaker status line and the overlay row.
    expect(src).toContain("withStep(t('update.rebooting'), UPDATE_STEPS.restart)");
    expect(src).toContain("withStep(t('updateAll.phase.rebooting'), UPDATE_STEPS.restart)");
  });

  it('marks the engine phase, so the last step is visibly still to come', () => {
    expect(src).toContain("withStep(t('updateAll.phase.engineQueued'), UPDATE_STEPS.engine)");
    expect(src).toContain("withStep(t('updateAll.phase.engineUploading'), UPDATE_STEPS.engine)");
  });

  it('leaves the finished states unmarked', () => {
    // A row that says Done must not also say "step 4 of 4": the sequence is
    // over, and a step count there reads as more still to come.
    for (const key of ['updateAll.phase.done', 'updateAll.phase.failed', 'updateAll.phase.engineDone']) {
      expect(src).not.toContain(`withStep(t('${key}')`);
    }
  });
});

describe('hiding the panel leaves something behind', () => {
  it('has a strip that reopens it', () => {
    expect(src).toContain('id="uaMini"');
    expect(src).toContain('updateAll.stillRunning');
    expect(en['updateAll.stillRunning']).toContain('{{n}}');
  });

  it('only shows the strip while something is genuinely unfinished', () => {
    const at = src.indexOf('const showMiniIfStillRunning');
    expect(at).toBeGreaterThan(-1);
    const body = src.slice(at, at + 420);
    // Counted from the rows, not from a flag that can lie.
    expect(body).toContain('counts()');
    expect(body).toMatch(/left\s*<=\s*0/);
  });

  it('takes the strip away when the batch ends', () => {
    const at = src.indexOf('// Batch done: release the global lock');
    expect(at).toBeGreaterThan(-1);
    expect(src.slice(at, at + 300)).toContain("mini.classList.add('hidden')");
  });

  it('does not disable the close button, which strands people', () => {
    // Tried once and reverted: a speaker that drops off Wi-Fi mid-update never
    // reaches a final state, so the panel could not be dismissed at all
    // (reported 2026-08-23).
    const at = src.indexOf('id="uaClose"');
    const around = src.slice(Math.max(0, at - 700), at + 200);
    expect(around).toContain('Never disabled');
  });
});

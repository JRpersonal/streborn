// Every thumbs-up STR sent went to a 404.
//
// VoteStation POSTed /api/radio/vote/<uuid> to the SPEAKER. Commit 671f96c3
// (2026-06-10, v0.7.16) moved radio-browser out of the agent and deleted
// /api/radio from it, and the three call sites all swallow failures with
// .catch(() => {}), so nothing surfaced. The app-side RadioVote binding that
// replaced it existed the whole time and was never called.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const main = readFileSync(join(here, 'main.js'), 'utf8');
const api = readFileSync(join(here, 'api.js'), 'utf8');
const boxops = readFileSync(join(here, '..', '..', 'app_boxops.go'), 'utf8');
const webuiRoutes = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'server.go'), 'utf8');
const bindings = readFileSync(join(here, '..', 'wailsjs', 'go', 'main', 'App.d.ts'), 'utf8');

describe('a vote reaches radio-browser', () => {
  it('goes through the app, not through the speaker', () => {
    expect(main.match(/RadioVote\(/g) || []).toHaveLength(3);
    expect(main).not.toContain('VoteStation');
    expect(api).not.toContain('VoteStation');
  });

  it('keeps all three places a vote is earned', () => {
    // a long-press save from the app list, a save of what is playing, and a
    // save from the search results
    expect(main).toContain('RadioVote(app.uuid)');
    expect(main).toContain('RadioVote(state.nowUUID)');
    expect(main).toContain('RadioVote(station.stationuuid)');
  });

  it('no longer carries a bound method that posts to a route the agent dropped', () => {
    expect(boxops).not.toContain('api/radio/vote');
    expect(bindings).not.toContain('VoteStation');
    // and the agent really does not serve it, so nothing here can be restored
    // by pointing at the box again (the one mention left is the comment that
    // records the removal)
    expect(webuiRoutes).not.toMatch(/mux\.HandleFunc\("\/api\/radio/);
  });

  it('still guards on a real uuid, so an empty one is never sent', () => {
    for (const guard of ['if (app.uuid)', 'if (state.nowUUID)', 'if (station.stationuuid)']) {
      expect(main).toContain(guard);
    }
  });
});

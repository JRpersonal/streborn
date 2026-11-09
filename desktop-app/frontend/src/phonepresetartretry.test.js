// #1233: on the phone remote a folder key's cover that failed while its music
// server was off never came back: the 15 s re-read repaints only a changed
// list, and the failed image had been removed from its tile. The page now
// asks for a missing cover again at most once a minute.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const phone = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8').replace(/\r\n/g, '\n');

function phoneFn(name) {
  const start = phone.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} not found on the phone page`);
  const end = phone.indexOf('\n}\n', start);
  return phone.slice(start, end + 2);
}

// eslint-disable-next-line no-new-func
const presetArtRetryDue = new Function('PRESET_ART_RETRY_MS', `${phoneFn('presetArtRetryDue')}\nreturn presetArtRetryDue;`)(60000);

describe('a lost key cover is asked for again (#1233)', () => {
  it('retries only while a cover is missing, at most once a minute', () => {
    expect(presetArtRetryDue(0, 1e9)).toBe(false);
    expect(presetArtRetryDue(1000, 1000 + 59999)).toBe(false);
    expect(presetArtRetryDue(1000, 1000 + 60000)).toBe(true);
  });

  it('records the loss when the last candidate fails, and retries off-screen when due', () => {
    expect(phoneFn('presetArtWalk')).toContain('presetArtMissed(st);');
    expect(phoneFn('presetArtMissed')).toContain('if (!presetArtMissingAt) presetArtMissingAt = st.failedAt;');
    // #1799: the retry walks the failed chains behind their stand-in instead
    // of rebuilding the grid, which made the keys flicker once a minute.
    const load = phoneFn('loadPresets');
    expect(load).toContain('if (presetArtRetryDue(presetArtMissingAt, Date.now())) { presetArtMissingAt = 0; presetArtRetry(); }');
  });
});

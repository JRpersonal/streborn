import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { compareNames, keepsTrackOrder } from './librarysort.js';

// The phone remote (internal/webui/assets/index.html) lists a media-server
// folder A to Z with its own copy of the rule (#1257): the page cannot import
// this module. Lifted off the served page and run against the case list the
// desktop module and the speaker's Go copy pin, so the three cannot drift.

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8')
  .replace(/\r\n/g, '\n');

const from = page.indexOf('var LIB_FOLD');
const to = page.indexOf('/* brOrdered is the open folder as listed');
const src = page.slice(from, to);

const phone = new Function(`${src}; return { libSortCompare, libSortList, libKeepsOrder };`)();

const AZ_CASES = {
  in: ['beta', '_Inbox', '10 Years', '2 Unlimited', 'Alpha', 'Äpfel', '_archive',
    'track 10', 'track 2', 'Zebra', 'apple', 'Ölberg', 'Ostsee'],
  want: ['_archive', '_Inbox', '2 Unlimited', '10 Years', 'Alpha', 'Äpfel', 'apple',
    'beta', 'Ölberg', 'Ostsee', 'track 2', 'track 10', 'Zebra'],
};

describe('phone page library order A to Z (#1257)', () => {
  it('finds the rule on the page', () => {
    expect(from).toBeGreaterThan(-1);
    expect(to).toBeGreaterThan(from);
  });

  it('sorts the shared case list like the desktop app and the speaker', () => {
    expect([...AZ_CASES.in].sort(phone.libSortCompare)).toEqual(AZ_CASES.want);
    expect([...AZ_CASES.in].sort(compareNames)).toEqual(AZ_CASES.want);
    const rows = AZ_CASES.in.map((title, i) => ({ title, i }));
    expect(phone.libSortList(rows).map((r) => r.title)).toEqual(AZ_CASES.want);
  });

  it('agrees with the desktop app on what keeps its order', () => {
    const cases = [
      [[{ trackNumber: 1 }, { trackNumber: 2 }], ''],
      [[{ trackNumber: 1 }, { trackNumber: 1 }], ''],
      [[{}, {}], 'object.container.album.musicAlbum'],
      [[{}, {}], 'object.container.playlistContainer'],
      [[{}, {}], 'object.container.storageFolder'],
      [[{ trackNumber: 1, discNumber: 1 }, { trackNumber: 1, discNumber: 2 }], ''],
    ];
    for (const [tracks, cls] of cases) {
      expect(phone.libKeepsOrder(tracks, cls)).toBe(keepsTrackOrder(tracks, cls));
    }
  });
});

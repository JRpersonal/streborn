import { describe, it, expect } from 'vitest';
import { collectFolderTree, treeNote, LIB_TREE_MAX_DEPTH } from './librarytree.js';
import { SORT_AZ, SORT_SERVER } from './librarysort.js';

// A fake media server: id -> { containers, items }, paged like BrowseLibrary.
function server(tree) {
  const calls = [];
  const browse = async (id, start, count) => {
    calls.push(id);
    const node = tree[id];
    if (!node) throw new Error(`no such folder ${id}`);
    const all = [...(node.containers || []).map((c) => ({ c })), ...(node.items || []).map((i) => ({ i }))];
    const page = all.slice(start, start + count);
    return {
      containers: page.filter((x) => x.c).map((x) => x.c),
      items: page.filter((x) => x.i).map((x) => x.i),
      totalMatches: all.length,
      containerClass: node.cls || '',
    };
  };
  return { browse, calls };
}

const track = (id, title, n) => ({ id, title, streamURL: `http://192.0.2.60:8200/${id}.flac`, trackNumber: n || 0 });

// The #1682 library: an artist with one folder per album, the server listing
// "Zenith" before "_Anfang", each album numbered against its alphabet.
const ARTIST = {
  // BrowseLibrary hands a one-page album over in track order already (#1487).
  z: { items: [track('z2', 'Omega', 1), track('z1', 'Alpha', 2)] },
  a: { items: [track('a1', 'Eins', 1), track('a2', 'Zwei', 2)] },
};
const SUBS = [{ id: 'z', title: 'Zenith' }, { id: 'a', title: '_Anfang' }];
const titles = (tree) => tree.items.map((it) => it.title);

describe('a folder of folders plays its albums (#1682)', () => {
  it('plays every album in track order, the albums in the order on screen', async () => {
    const { browse } = server(ARTIST);
    const tree = await collectFolderTree({ browse, subfolders: SUBS, sort: SORT_SERVER });
    expect(titles(tree)).toEqual(['Omega', 'Alpha', 'Eins', 'Zwei']);
    expect(tree).toMatchObject({ folders: 3, cut: false, cutDepth: false });
    const az = await collectFolderTree({ browse, subfolders: SUBS, sort: SORT_AZ });
    expect(titles(az)).toEqual(['Eins', 'Zwei', 'Omega', 'Alpha']);
  });

  it('leaves out folders deeper than the limit and says so', async () => {
    const t = {};
    const ids = ['l1', 'l2', 'l3', 'l4'];
    ids.forEach((id, i) => {
      t[id] = { items: [track(`${id}t`, id)], containers: ids[i + 1] ? [{ id: ids[i + 1], title: ids[i + 1] }] : [] };
    });
    const { browse } = server(t);
    const tree = await collectFolderTree({ browse, subfolders: [{ id: 'l1', title: 'l1' }], sort: SORT_SERVER });
    expect(titles(tree)).toEqual(['l1', 'l2', 'l3']);
    expect(tree.cutDepth).toBe(true);
    expect(tree.cut).toBe(false);
    expect(LIB_TREE_MAX_DEPTH).toBe(3);
  });

  it('stops at the track cap, plays a track listed twice once, and skips a folder that fails', async () => {
    const t = { broken: null };
    const subs = [{ id: 'broken', title: 'A broken' }];
    for (let a = 0; a < 3; a++) {
      t[`al${a}`] = { items: Array.from({ length: 10 }, (_, n) => track(`al${a}-${n}`, `Song ${n}`)) };
      subs.push({ id: `al${a}`, title: `Album ${a}` });
    }
    t.al1.items[0] = t.al0.items[0];
    delete t.broken;
    const { browse } = server(t);
    const tree = await collectFolderTree({ browse, subfolders: subs, sort: SORT_SERVER, limits: { tracks: 15 } });
    expect(tree.items).toHaveLength(15);
    expect(tree.cut).toBe(true);
    expect(new Set(tree.items.map((i) => i.streamURL)).size).toBe(15);
  });

  it('pages a long album and puts it in track order once it is all in', async () => {
    const items = Array.from({ length: 250 }, (_, n) => track(`x${n}`, `T${String(250 - n).padStart(3, '0')}`, 250 - n));
    const { browse, calls } = server({ long: { items, cls: 'object.container.album.musicAlbum' } });
    const tree = await collectFolderTree({ browse, subfolders: [{ id: 'long', title: 'Long' }], sort: SORT_SERVER });
    expect(calls).toEqual(['long', 'long']);
    expect(tree.items[0].trackNumber).toBe(1);
    expect(tree.items[249].trackNumber).toBe(250);
  });

  it('stops when the user moved on', async () => {
    const { browse } = server(ARTIST);
    let gone = false;
    const tree = await collectFolderTree({
      browse: async (...a) => { gone = true; return browse(...a); },
      subfolders: SUBS, sort: SORT_SERVER, isCancelled: () => gone,
    });
    expect(tree.cut).toBe(true);
    expect(tree.items).toHaveLength(0);
  });

  it('says what was left out', () => {
    const t = (k, v) => `${k}:${JSON.stringify(v)}`;
    expect(treeNote(null, t)).toBe('');
    expect(treeNote({ items: [], cut: false, cutDepth: false }, t)).toBe('');
    expect(treeNote({ items: [1, 2], cut: true, cutDepth: true }, t))
      .toBe('library.treeCut:{"n":2} library.treeDepthCut:{"depth":3}');
  });
});

// Mail 2026-11-03: 332 songs on a slow QNAP took half a minute to collect and
// the screen said nothing meanwhile. The walk reports its count as it goes.
describe('the walk reports its progress', () => {
  it('counts the folders read and the tracks collected after every folder', async () => {
    const { browse } = server(ARTIST);
    const seen = [];
    await collectFolderTree({ browse, subfolders: SUBS, sort: SORT_SERVER, onProgress: (p) => seen.push(p) });
    expect(seen).toEqual([{ folders: 2, tracks: 2 }, { folders: 3, tracks: 4 }]);
  });

  it('a progress callback that throws does not stop the walk', async () => {
    const { browse } = server(ARTIST);
    const tree = await collectFolderTree({ browse, subfolders: SUBS, sort: SORT_SERVER, onProgress: () => { throw new Error('ui gone'); } });
    expect(titles(tree)).toEqual(['Omega', 'Alpha', 'Eins', 'Zwei']);
  });
});

// librarytree.js: playing a Library folder together with the folders below it
// (#1682).
//
// "Play folder" and "Put folder on a key" were only there in a folder that
// holds tracks itself. An artist folder with one folder per album holds none,
// so neither was on offer exactly where the owner wanted "everything by this
// artist". Such a folder now plays the tracks of its sub-folders: each album
// whole, in its track order (#1487), the albums in the order on screen.
//
// A folder that holds tracks keeps playing exactly those. The speaker follows
// the same rule when it replays the folder from Recently played or reads a
// key's folder again (internal/webui/libraryfoldertree.go), so a folder is the
// same list of tracks wherever it is started.
//
// A tree can be any size, so the walk is bounded the same way the speaker's
// is: LIB_TREE_MAX_DEPTH levels below the folder, LIB_TREE_MAX_TRACKS tracks,
// LIB_TREE_MAX_FOLDERS folders read. What did not fit is reported, so the
// Library can say the queue is not the whole folder.

import { orderLongFolder } from './trackorder.js';
import { sortFolders, sortTracks } from './librarysort.js';
import { withoutMacMetadata } from './libraryplayfail.js';

// Keep in step with libraryTreeMaxDepth / libraryTreeMaxTracks /
// libraryTreeMaxFolders in internal/webui/libraryfoldertree.go. The track cap
// is the folder-key cap (MAX_QUEUE_PRESET_ITEMS), so a walked folder fits on a
// key whole or says it was cut.
export const LIB_TREE_MAX_DEPTH = 3;
export const LIB_TREE_MAX_TRACKS = 1000;
export const LIB_TREE_MAX_FOLDERS = 300;

// The Browse page size and the per-folder ceiling, as the Library pages an
// open folder (LIB_PAGE / LIB_MAX in views/library.js).
const PAGE = 200;
const MAX_CHILDREN = 2500;

// readFolder pages one folder the way the Library does: every page, the Mac's
// "._" companion files dropped, a folder longer than a page put in album
// track order once it is all in.
async function readFolder(browse, id) {
  let start = 0;
  let cls = '';
  let containers = [];
  let items = [];
  for (let first = true; ; first = false) {
    const page = (await browse(id, start, PAGE)) || {};
    const cs = page.containers || [];
    const its = page.items || [];
    if (first) cls = page.containerClass || '';
    containers = containers.concat(cs);
    items = items.concat(withoutMacMetadata(its));
    const got = cs.length + its.length;
    start += PAGE;
    const done = got < PAGE || (page.totalMatches && start >= page.totalMatches);
    if (done) {
      if (start > PAGE) items = orderLongFolder(items, cls);
      return { containers, items, cls };
    }
    if (containers.length + items.length >= MAX_CHILDREN) return { containers, items, cls };
  }
}

// collectFolderTree collects the tracks below a folder that holds none itself.
//
//   browse(id, start, count)  one Browse page of a folder (BrowseLibrary)
//   subfolders                the open folder's sub-folders, as listed
//   sort                      the viewer's order (librarysort.js): it decides
//                             which album comes first, as on screen
//   isCancelled()             true once the user moved on; the walk stops
//
// Resolves with { items, folders, cut, cutDepth }: the tracks in play order
// (each played once, however many folders list it), how many folders were
// read, whether the walk stopped at a cap, and whether deeper folders were
// left out. A sub-folder that cannot be read is skipped, not fatal.
export async function collectFolderTree({ browse, subfolders, sort, isCancelled = () => false, limits = {} }) {
  const maxDepth = limits.depth || LIB_TREE_MAX_DEPTH;
  const maxTracks = limits.tracks || LIB_TREE_MAX_TRACKS;
  const maxFolders = limits.folders || LIB_TREE_MAX_FOLDERS;
  const out = { items: [], folders: 1, cut: false, cutDepth: false };
  const seen = new Set();
  const visited = new Set();

  async function walk(list, depth) {
    for (const c of sortFolders(list || [], sort)) {
      if (out.cut) return;
      if (!c || visited.has(c.id)) continue; // a server that lists a folder inside itself
      visited.add(c.id);
      if (isCancelled() || out.folders >= maxFolders) {
        out.cut = true;
        return;
      }
      out.folders++;
      let f;
      try {
        f = await readFolder(browse, c.id);
      } catch {
        continue; // one unreadable album does not cost the others
      }
      if (isCancelled()) {
        out.cut = true;
        return;
      }
      for (const it of sortTracks(f.items, sort, c.class || f.cls)) {
        if (!it || !it.streamURL || seen.has(it.streamURL)) continue;
        seen.add(it.streamURL);
        out.items.push(it);
        if (out.items.length >= maxTracks) {
          out.cut = true;
          return;
        }
      }
      if (!f.containers.length) continue;
      if (depth >= maxDepth) {
        out.cutDepth = true;
        continue;
      }
      await walk(f.containers, depth + 1);
    }
  }

  await walk(subfolders, 1);
  return out;
}

// treeNote is what the Library says about a walk that did not take the whole
// tree, '' when it did. t is the i18n function.
export function treeNote(tree, t) {
  if (!tree) return '';
  const parts = [];
  if (tree.cut) parts.push(t('library.treeCut', { n: tree.items.length }));
  if (tree.cutDepth) parts.push(t('library.treeDepthCut', { depth: LIB_TREE_MAX_DEPTH }));
  return parts.join(' ');
}

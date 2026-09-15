// Put back the tracked placeholder that vite's emptyOutDir removes.
//
// dist/ is gitignored except for this one file, which exists so that
// `go:embed all:frontend/dist` has something to match on a clean checkout. A
// local `npm run build` wipes the folder and takes it with it, and a commit
// made with `git add -A` then carries the deletion: the desktop app stops
// compiling for everyone, with a message ("no matching files found") that
// points at the embed rather than at the build that caused it.
//
// It happened twice in one day (2026-09-15) before this existed.
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const anchor = resolve(here, '..', 'dist', '.gitkeep');

if (!existsSync(anchor)) {
  mkdirSync(dirname(anchor), { recursive: true });
  writeFileSync(anchor, '');
  console.log('restored the dist embed anchor:', anchor);
}

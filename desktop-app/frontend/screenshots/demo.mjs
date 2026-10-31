// Shared by the screenshot harness (shoot.mjs) and the UI 2.0 check
// (ui2-check.mjs): the scenario each shot runs, and the vite build/preview
// helpers.
//
// The demo data and the mocks themselves live in ../testkit/, shared with the
// Playwright suite (../e2e/): demo-data.mjs (fixtures), scenario.mjs (what a
// run looks like), desktop-mock.mjs (window.go and window.runtime in the
// page) and agent-routes.mjs (the agent HTTP endpoints).

import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { DEMO } from '../testkit/demo-data.mjs';
import { defaultScenario, resolveScenario } from '../testkit/scenario.mjs';
import { installDesktopMock } from '../testkit/desktop-mock.mjs';
import { agentRoutes } from '../testkit/agent-routes.mjs';

export { DEMO, installDesktopMock, agentRoutes };

const __dirname = path.dirname(fileURLToPath(import.meta.url));
export const FRONTEND = path.resolve(__dirname, '..');                // desktop-app/frontend

// shotScenario is the mocked world of one shot, resolved for the mock.
//
// The classic views see one speaker (plus the Bose original on the setup
// views, so the network-install card has a target); app-listen plays, every
// other view boots in standby. The UI 2.0 views (app-ui2-*) run on the
// simulated network of eleven speakers (DEMO.ui2Net): a group of two playing,
// a stereo pair, an offline speaker, a Bose original, idle speakers.
export function shotScenario(view, locale) {
  const sc = defaultScenario();
  sc.seed = { locale, ui2: false, theme: null, scale: null, cachedBoxes: ['livingRoom'], extraStorage: { searchCountry: '' } };
  if (view.startsWith('app-ui2') || view.startsWith('app-classic-')) {
    // app-classic-*: the same network with the preview off, for comparison.
    sc.seed.ui2 = view.startsWith('app-ui2');
    sc.seed.theme = view.endsWith('-light') ? 'light' : view.endsWith('-contrast') ? 'contrast' : 'dark';
    sc.seed.scale = view === 'app-ui2-xl' ? '3' : '1';
    sc.seed.cachedBoxes = true;
    sc.boxes = DEMO.ui2Net;
    sc.playing = Object.fromEntries(DEMO.ui2Playing.map((h) => [h, 'PLAY_STATE']));
    sc.zones = DEMO.ui2Zones;
  } else {
    sc.boxes = view.startsWith('app-install') || view.startsWith('app-stick') ? ['livingRoom', 'kitchen'] : ['livingRoom'];
    sc.playing = { livingRoom: view === 'app-listen' ? 'PLAY_STATE' : 'STANDBY' };
    // The classic shots were taken with these two unanswered (null); kept so
    // the published pictures do not change underneath the docs.
    sc.bindings = { RefreshKnownBoxes: { value: null }, GetZoneState: { value: null } };
  }
  return resolveScenario(sc);
}

// shotDemo is the demo data one shot renders. The UI 2.0 and classic phase 2
// shots move the played history to the shot's own clock, so Recently played
// shows "Today", "Yesterday" and an older day whenever the shots are taken:
// the latest station minutes ago, the next two cards a day earlier, the last
// one three days back; the gaps between the entries stay as they are. (A shot
// taken in the first hours after midnight can see "yesterday" slip a day.)
// The published classic shots keep the fixed history.
export function shotDemo(view, now = Date.now()) {
  if (!view.startsWith('app-ui2') && !view.startsWith('app-classic-')) return DEMO;
  const DAY = 86400000;
  const recent = DEMO.recent;
  const newest = Math.max(...recent.map((e) => Date.parse(e.ts)));
  const cards = [...new Set(recent.map((e) => e.cardKey))];
  const daysBack = (key) => {
    const at = cards.indexOf(key);
    return at <= 0 ? 0 : at <= 2 ? 1 : 3;
  };
  const moved = recent.map((e) => {
    const ts = now - 3 * 60000 - (newest - Date.parse(e.ts)) - daysBack(e.cardKey) * DAY;
    return { ...e, ts: new Date(ts).toISOString() };
  });
  return { ...DEMO, recent: moved };
}

// ---- build + vite preview server -------------------------------------------
// We build/serve with the parent frontend's own vite (resolved via npx in
// cwd=FRONTEND), so this harness's only dependency is playwright. That keeps
// playwright out of the app's package.json and therefore out of every CI /
// wails build (it would otherwise download ~130 MB of browsers on install).
//
// The build goes to desktop-app/.e2e-out/shots-<port> (git-ignored, outside
// the Vite root), never to frontend/dist: a bare `vite build` empties dist and
// deletes the tracked go:embed anchor dist/.gitkeep. The port in the name
// keeps two harness runs on two ports from sharing a build.
export const outDirFor = (port) => path.resolve(FRONTEND, '..', '.e2e-out', `shots-${port}`);
// The win32 spawns go through a shell (npx.cmd), which splits an unquoted
// path with spaces; elsewhere there is no shell, and quotes would stay literal.
const outDirArg = (port) => (process.platform === 'win32' ? `"${outDirFor(port)}"` : outDirFor(port));

export function buildFrontend(port) {
  const bin = process.platform === 'win32' ? 'npx.cmd' : 'npx';
  return new Promise((resolve, reject) => {
    const p = spawn(bin, ['vite', 'build', '--outDir', outDirArg(port), '--emptyOutDir'], { cwd: FRONTEND, stdio: 'ignore', shell: process.platform === 'win32' });
    p.on('exit', (code) => (code === 0 ? resolve() : reject(new Error('vite build exited ' + code))));
    p.on('error', reject);
  });
}

export function startServer(port) {
  const bin = process.platform === 'win32' ? 'npx.cmd' : 'npx';
  const srv = spawn(bin, ['vite', 'preview', '--outDir', outDirArg(port), '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: FRONTEND, stdio: 'ignore', shell: process.platform === 'win32' });
  return srv;
}
// assertPortFree fails when something already answers on base: vite would
// exit on the taken port (--strictPort) and the run would screenshot whatever
// build that other server holds, another checkout's included.
export async function assertPortFree(base) {
  let taken = false;
  try { await fetch(base); taken = true; } catch (e) {}
  if (taken) throw new Error(`${base} already answers (another preview server?); pick a free port with SHOOT_PORT / UI2_CHECK_PORT`);
}

// waitForServer waits for the preview server. Given the server process it
// also fails at once when that process exits, which is what vite does with
// --strictPort on a port another checkout's preview still holds: without the
// check the run would silently screenshot THAT checkout's build.
export async function waitForServer(base, server) {
  let exited = null;
  if (server) server.once('exit', (code) => { exited = code; });
  for (let i = 0; i < 60; i++) {
    if (exited !== null) throw new Error(`vite preview exited (${exited}); is the port of ${base} taken?`);
    try { const r = await fetch(base); if (r.ok) return; } catch (e) {}
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error('vite preview did not come up on ' + base);
}


// stopServer takes the preview server down. On Windows it runs under a shell
// (see startServer), and killing the shell leaves vite listening on the port,
// where the next run then finds a stale build: take the whole tree down.
export function stopServer(server) {
  if (!server) return;
  if (process.platform === 'win32') spawn('taskkill', ['/pid', String(server.pid), '/T', '/F'], { stdio: 'ignore' });
  else server.kill();
}

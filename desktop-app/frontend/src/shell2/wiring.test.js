// How the UI 2.0 preview shell hooks into the app, pinned by source text
// (main.js needs a DOM and the Wails runtime, so it cannot be imported here).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileSource, srcOf, srcContaining, sourceFiles } from '../test/source.js';

const mount = fileSource('shell2/mount.js');
const main = fileSource('main.js');
const group = fileSource('views/music/group.js');
const markup = fileSource('shell2/markup.js');
const shellCss = readFileSync(new URL('./shell2.css', import.meta.url), 'utf8');

describe('UI 2.0 shell mount', () => {
  it('moves the existing nodes and writes no markup into the views', () => {
    expect(mount).toContain('parent.appendChild(node)');
    expect(mount).toContain('insertBefore(node, next)');
    // innerHTML only ever lands in the shell's own containers.
    for (const m of mount.matchAll(/(\w+)\.innerHTML\s*=/g)) {
      expect(['holder', 'lists'], m[0]).toContain(m[1]);
    }
    expect(mount).not.toMatch(/cloneNode/);
  });

  it('keeps an undo log and replays it backwards on a failure', () => {
    expect(mount).toContain('const log = []');
    expect(mount).toMatch(/for \(let i = log\.length - 1; i >= 0; i--\)/);
  });

  it('adds no shell id to the views or the skeleton', () => {
    expect(srcOf('musicViewMarkup')).not.toMatch(/ui2/);
    const skeleton = srcContaining("document.querySelector('#app').innerHTML");
    expect(skeleton).not.toMatch(/id="ui2/);
  });

  it('is built only with the preview on, right after the Music view', () => {
    const at = main.indexOf('mountMusicView();\n');
    const guard = main.indexOf('if (getUi2Preview()) safeMountShell2();');
    expect(at).toBeGreaterThan(0);
    expect(guard).toBeGreaterThan(at);
    expect(main.slice(at, guard)).not.toMatch(/\n[a-zA-Z]/);
    expect(srcOf('applyBoxList')).toContain('if (getUi2Preview() && !shell2Mounted()) safeMountShell2();');
  });
});

describe('UI 2.0 hooks in the app', () => {
  it('follows the open view, the nav dots, the badges and the selection', () => {
    expect(srcOf('switchView')).toContain('syncShellNav(view)');
    expect(srcOf('renderNavDots')).toContain("'.tab-btn, .ui2-nav-item'");
    expect(srcOf('updateSettingsTabBadge')).toContain('mirrorShellBadges({ settingsUpdates: n })');
    expect(srcOf('updateMultiroomTabBadge')).toContain('mirrorShellBadges({ groups:');
    const vis = srcOf('updateBoxUiVisibility');
    expect(vis).toContain('syncShellPlayer(hasBox)');
    expect(vis).toContain('renderShellSpeakers()');
  });

  it('chooses a speaker through pickBox from the picker and the sidebar', () => {
    const pick = srcOf('pickBox');
    expect(pick).toContain("state.setupTarget = { kind: 'stock', box }; switchView('setup');");
    const sel = srcOf('renderBoxSelect');
    expect(sel.match(/pickBox\(/g).length).toBeGreaterThanOrEqual(2);
    expect(main).toMatch(/mountShell2\(\{[\s\S]*?pickBox: pickBoxFromShell,/);
    expect(mount).toContain('deps.pickBox(box)');
  });

  it('brings an open Settings or Setup view along when the sidebar picks a speaker', () => {
    const shellPick = srcOf('pickBoxFromShell');
    expect(shellPick).toContain('await pickBox(box);');
    // Settings: the reconnect timer of the old speaker stops, the form reloads.
    expect(shellPick).toContain('clearTimeout(state.settingsReconnect.timer)');
    expect(shellPick).toContain('loadBoxSettings();');
    // Setup: a running install or a chosen factory reset keeps its target.
    expect(shellPick).toContain('installRunActive()');
    expect(shellPick).toContain("prevTarget.kind === 'factory-reset'");
    expect(shellPick).toContain('state.setupTarget = prevTarget');
    expect(shellPick).toContain('renderSetupTargetPicker();');
  });

  it('keeps the ticking clock out of the persistent player live region', () => {
    const quiet = srcOf('quietPlayerClock');
    expect(quiet).toContain("bar.removeAttribute('aria-live')");
    expect(quiet).toContain("main.setAttribute('aria-live', 'polite')");
    expect(srcOf('mountShell2')).toContain('quietPlayerClock();');
  });

  it('leaves the current picker as it was: click only, the drawn tile decides offline', () => {
    const sel = srcOf('renderBoxSelect');
    // No new keyboard wiring on the speaker tiles with the preview off.
    expect(sel).not.toMatch(/btn\.onkeydown/);
    // A tile drawn offline stays offline for its click, whatever the live list
    // says by now: toast and rediscovery, as before.
    expect(sel).toContain('if (btn.dataset.offline) { pickBox({ ...(box || {}), offline: true }, btn.title); return; }');
  });

  it('marks the Layout switch after a late mount', () => {
    expect(srcOf('safeMountShell2')).toContain('syncLayoutPressed();');
    expect(srcOf('syncLayoutPressed')).toContain('#a11yMenu button[data-ui2]');
  });

  it('polls the player off the Music tab only as the scope allows', () => {
    expect(srcOf('nextStatusDelayMs')).toContain('playerOnScreen(');
    expect(srcOf('syncMusicTabVolumeFromBox')).toContain('playerOnScreen(');
    expect(srcOf('refreshStatus')).toContain('persistentPlayer: !!state.ui2Shell');
    const interval = srcContaining('refreshEverySpeaker();\n}, 15000)');
    expect(interval).toContain("(state.view !== 'box' && !shellListsSpeakers())");
  });

  it('runs the every-speaker poll for the sidebar only where the player is on screen', () => {
    expect(srcOf('refreshEverySpeaker')).toContain('if (state.otaInProgress || installRunActive() || !state.boxes.length) return;');
    const lists = srcOf('shellListsSpeakers');
    expect(lists).toContain('!!state.ui2Shell');
    expect(lists).toContain('playerOnScreen(');
    expect(main).not.toContain('onRoomsOpen');
  });

  it('hides the player volume and transport where nothing keeps them in sync', () => {
    const sync = srcOf('syncShellPlayer');
    expect(sync).toContain('playerOnScreen(');
    expect(sync).toContain("'.ui2-player-transport, .ui2-player-more, .ui2-player-volume, .ui2-player-groupvol', live");
    // Levels and the grouped look follow live AND grouped, set in one place.
    expect(sync).toContain('applyPlayerGroup();');
    expect(srcOf('syncShellGroupPanel')).toContain('applyPlayerGroup();');
    expect(srcOf('applyPlayerGroup')).toContain('const on = playerLive && groupShown;');
    expect(srcOf('switchView')).toContain('syncShellPlayer();');
  });

  it('switches the preview from the Aa menu with a reload', () => {
    expect(main).toContain('data-ui2="0"');
    expect(main).toContain('data-ui2="1"');
    expect(main).toMatch(/setUi2Preview\(on\);[\s\S]{0,80}location\.reload\(\)/);
  });
});

describe('UI 2.0 sidebar speaker list', () => {
  it('replaces the room switcher and the Output popover', () => {
    for (const id of ['ui2RoomBtn', 'ui2RoomPop', 'ui2RoomList', 'ui2RoomAdd', 'ui2OutputBtn', 'ui2OutputPop']) {
      expect(markup, id).not.toContain(id);
    }
    const spk = markup.indexOf('id="ui2Speakers"');
    const nav = markup.indexOf('<nav ');
    expect(spk).toBeGreaterThan(0);
    expect(nav).toBeGreaterThan(spk);
    for (const id of ['ui2LevelsPop', 'ui2GroupVolSlot', 'ui2Version', 'ui2SpkLists']) expect(markup, id).toContain(`id="${id}"`);
  });

  it('moves the group panel into Levels and keeps the version line in the frame', () => {
    expect(mount).toContain('move(n.groupControl, n.fLevelsBody);');
    expect(mount).toContain('MutationObserver');
    expect(mount).toContain("attributeFilter: ['class']");
    expect(srcOf('watchAppUpdateBanner')).toContain("banner.classList.contains('app-update-quiet')");
    // The mac and deb hand-offs never set the quiet class: they rely on the
    // full notice having removed it first, which sends the banner to the top.
    const check = srcOf('checkAppUpdate');
    expect(check.indexOf("banner.classList.remove('app-update-quiet')")).toBeGreaterThan(0);
    expect(srcOf('showMacHandoff')).not.toContain('app-update-quiet');
    expect(srcOf('showDebHandoff')).not.toContain('app-update-quiet');
  });

  it("edits groups only through the Music view's own path", () => {
    expect(main).toMatch(/mountShell2\(\{[\s\S]*?toggleGroupMember,[\s\S]*?groupEditPendingHosts,/);
    expect(mount).toContain('deps.toggleGroupMember(');
    expect(group).toMatch(/export function toggleGroupMember\(/);
    expect(group).toMatch(/export function groupEditPendingHosts\(/);
    for (const f of sourceFiles().filter((x) => x.startsWith('shell2/') && !x.endsWith('.test.js'))) {
      expect(fileSource(f), f).not.toMatch(/FormZone|DissolveZone|SetZone/);
    }
    // The pending mark reaches the sidebar on every change of the pending set.
    expect(srcOf('markGroupChipPending')).toContain('renderShellSpeakers(true)');
  });

  it('runs a group edit on the group it was clicked in', () => {
    const toggle = srcOf('toggleGroupMember');
    expect(toggle).toContain('pendingGroupMaster');
    expect(srcOf('runPendingGroupEdits')).toContain('runGroupMemberToggle(edits, masterHost)');
    const run = srcOf('runGroupMemberToggle');
    expect(run).toContain('resolvePlayTarget(lead, state.zoneLive, state.boxes) === lead');
    expect(run).toContain('.filter(b => b && b.host !== box.host)');
    expect(run).toContain('inAnyStereoPair(tgt, state.zoneLive)');
  });

  it('keeps the Music view markup as it was without the player slot', () => {
    const render = srcOf('renderGroupControl');
    expect(render).toContain("const slot = $('ui2GroupVolSlot');");
    expect(render).toMatch(/\} else \{\s*cont\.innerHTML = `<span class="group-label muted small">\$\{escapeHtml\(t\('group\.label'\)\)\}<\/span>`\s*\+ memberNote\s*\+ `<div class="group-chips">\$\{chips\}<\/div>\$\{volRow\}`;/);
    expect(render).toContain('groupCandidates(box, state.zoneLive, state.boxes)');
    expect(render).toContain('syncShellGroupPanel(slaves.length > 0)');
  });
});

describe('UI 2.0 shell hygiene', () => {
  const shellFiles = sourceFiles().filter((f) => f.startsWith('shell2/') || f === 'uipreview.js' || f === 'speakertile.js');

  it('never uses the Mac-only modifier', () => {
    for (const f of shellFiles) expect(fileSource(f), f).not.toMatch(/metaKey/);
  });

  it('touches no document, storage or navigator at import time', () => {
    for (const f of shellFiles) {
      const top = fileSource(f).split('\n').filter((l) => /^\S/.test(l) && !/^(\/\/|import|export function|function|\}|const \w+ = \(|let |\/\*| \*)/.test(l));
      for (const l of top) expect(l, `${f}: ${l}`).not.toMatch(/\b(document|localStorage|navigator)\b/);
    }
  });

  it('scopes the new focus ring to the shell chrome', () => {
    expect(shellCss).toContain('html.ui2 :is(.ui2-sidebar, .ui2-player, .u-popover, .u-skip) :focus-visible');
    expect(shellCss).not.toMatch(/^html\.ui2 \*:focus-visible/m);
  });
});

// The UI 2.0 preview's Library (#1286): one level per step with a trail of
// crumbs, the speaker named on every play and key action, and feedback,
// empty and error states in place.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import {
  SERVERS_DEPTH, countLine, folderActionsHTML, folderRowHTML, headerHTML, libraryTrail, manualAddHTML,
  repeatLabelKey, serverRowsHTML, stateHTML, statusHTML, trackRowHTML, trailHTML, trailParent,
} from './library.js';
import { setLocale } from '../i18n/index.js';
import { fileSource, srcOf } from '../test/source.js';

setLocale('en');

const css = readFileSync(new URL('./library.css', import.meta.url), 'utf8');
const shellCss = readFileSync(new URL('./shell2.css', import.meta.url), 'utf8');

describe('the trail', () => {
  const stack = [{ id: '0', title: '' }, { id: 'c1', title: 'Music' }, { id: 'c1a', title: 'Evening <Sessions>' }];

  it('starts at the music servers and is the only crumb there', () => {
    const c = libraryTrail(stack, { inServer: false });
    expect(c).toEqual([{ depth: SERVERS_DEPTH, label: 'Music servers', current: true }]);
    expect(trailParent(c)).toBeNull();
  });

  it('has one crumb per step, the root named after its server, the last one current', () => {
    const c = libraryTrail(stack, { inServer: true, serverName: 'Living Room NAS' });
    expect(c.map((x) => x.label)).toEqual(['Music servers', 'Living Room NAS', 'Music', 'Evening <Sessions>']);
    expect(c.map((x) => x.depth)).toEqual([SERVERS_DEPTH, 0, 1, 2]);
    expect(c.filter((x) => x.current).map((x) => x.depth)).toEqual([2]);
    expect(trailParent(c).depth).toBe(1);
  });

  it('puts search results one step below where they were searched from', () => {
    const c = libraryTrail(stack.slice(0, 2), { inServer: true, serverName: 'NAS', searchQuery: 'jazz' });
    expect(c.at(-1)).toEqual({ depth: null, label: 'Results for “jazz”', current: true });
    expect(trailParent(c).label).toBe('Music');
  });

  it('draws Back to the parent and every other crumb as a button, escaped', () => {
    const html = trailHTML(libraryTrail(stack, { inServer: true, serverName: 'NAS' }));
    expect(html).toContain('aria-label="Back to Music"');
    expect(html).toContain('class="u-btn u-btn--secondary lib2-back" data-depth="1"');
    expect(html.match(/<button type="button" class="lib2-crumb"/g)).toHaveLength(3);
    expect(html).toContain('aria-current="location">Evening &lt;Sessions&gt;</span>');
    expect(html).not.toContain('<Sessions>');
    expect(html).toContain('<nav class="lib2-trail" aria-label="Where you are in the library">');
  });

  it('has no Back at the top', () => {
    expect(trailHTML(libraryTrail([], { inServer: false }))).not.toContain('lib2-back');
  });
});

describe('the speaker is named', () => {
  const item = { id: 't1', title: 'Blue "Hour"', streamURL: 'http://192.0.2.60/1.flac' };

  it('on a track row, with the track, in both actions', () => {
    const html = trackRowHTML(item, { key: 'u:x', attr: 'iid', value: 't1', speaker: 'Kitchen', meta: 'A · B', duration: '3:05' });
    expect(html).toContain('aria-label="Play “Blue &quot;Hour&quot;” on Kitchen"');
    expect(html).toContain('aria-label="Put “Blue &quot;Hour&quot;” on a key of Kitchen"');
    expect(html).toContain('data-iid="t1"');
    expect(html).toContain('class="lib2-status" role="status" data-skey="u:x"');
    expect(html).toContain('library-row-track');
  });

  it('and the row says what to do when no speaker is chosen', () => {
    const html = trackRowHTML(item, { key: 'k', attr: 'idx', value: 3 });
    expect(html).toContain('aria-label="Play “Blue &quot;Hour&quot;”"');
    expect(html).toContain('data-idx="3"');
    expect(headerHTML({ speaker: '' })).toContain('No speaker chosen.');
  });

  it('on the folder actions', () => {
    const html = folderActionsHTML({ speaker: 'Kitchen', shuffle: true, repeat: 'one' });
    expect(html).toContain('Play folder on Kitchen');
    expect(html).toContain('Put folder on a key of Kitchen');
    expect(html).toContain('aria-pressed="true"');
    expect(html).toContain('Repeat: one track');
    expect(html).toContain('data-skey="folder"');
  });

  it('in the head of every level', () => {
    expect(headerHTML({ speaker: 'Kitchen' })).toContain('Plays on Kitchen.');
  });
});

describe('the pieces of a level', () => {
  it('names the repeat mode in words', () => {
    expect(repeatLabelKey('off')).toBe('ui2.library.repeatOff');
    expect(repeatLabelKey('all')).toBe('ui2.library.repeatAll');
    expect(repeatLabelKey('one')).toBe('ui2.library.repeatOne');
    expect(repeatLabelKey(undefined)).toBe('ui2.library.repeatOff');
  });

  it('counts only what there is', () => {
    expect(countLine(0, 0)).toBe('');
    expect(countLine(6, 0)).toBe('Folders: 6');
    expect(countLine(2, 30)).toBe('Folders: 2 · Tracks: 30');
  });

  it('opens a folder with one button that keeps the classic row class', () => {
    const html = folderRowHTML({ id: 'c"1', title: 'Music', childCount: 6 });
    expect(html).toContain('class="lib2-row lib2-folder library-row-folder" data-cid="c&quot;1"');
    expect(html).toContain('<span class="u-sr-only">Entries: </span>6');
  });

  it('marks a server the last scan did not hear in words', () => {
    const html = serverRowsHTML([{ udn: 'uuid:a', friendlyName: 'NAS', modelName: 'MinimServer', address: '192.0.2.40', notAnswering: true }]);
    expect(html).toContain('data-udn="uuid:a"');
    expect(html).toContain('MinimServer · 192.0.2.40');
    expect(html).toContain('u-pill u-pill--warn');
    expect(html).toContain('not answering');
  });

  it('announces an error at once and anything else politely', () => {
    expect(stateHTML({ tone: 'error', title: 'x' })).toContain('role="alert"');
    expect(stateHTML({ tone: 'info', title: 'x' })).toContain('role="status"');
    const html = stateHTML({ tone: 'error', title: 'T', body: 'B', detail: '<d>', actions: [{ id: 'lib2Retry', label: 'Try again', primary: true }] });
    expect(html).toContain('&lt;d&gt;');
    expect(html).toContain('class="u-btn u-btn--primary" id="lib2Retry"');
    expect(html).toContain('lib2-focus-target" tabindex="-1"');
  });

  it('keeps a failed add under its field', () => {
    expect(manualAddHTML({ error: 'No media server found at this address: x' })).toMatch(/<details class="lib2-manual" open>[\s\S]*role="alert"/);
    expect(manualAddHTML({ open: false })).not.toContain(' open>');
  });

  it('writes the tone of a feedback line into its class', () => {
    expect(statusHTML('k', { tone: 'ok', text: 'Playing on Kitchen' })).toContain('lib2-status lib2-status--ok');
    expect(statusHTML('k', null)).toContain('class="lib2-status" role="status" data-skey="k"></span>');
  });
});

describe('the strings', () => {
  const dir = new URL('../i18n/bundles/', import.meta.url);
  const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
    .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
  const used = new Set();
  for (const f of ['shell2/library.js', 'views/library.js']) {
    for (const m of fileSource(f).matchAll(/'(ui2\.library\.[a-zA-Z]+)'/g)) used.add(m[1]);
  }
  const ph = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

  it('finds the keys in the code', () => {
    expect(used.size).toBeGreaterThan(40);
  });

  for (const [code, b] of Object.entries(bundles)) {
    it(`${code} has every Library string, with the English placeholders and no dash for a pause`, () => {
      for (const k of used) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(ph(b[k]), `${code}: ${k}`).toEqual(ph(bundles.en[k]));
        expect(b[k], `${code}: ${k}`).not.toMatch(/[—–]/);
      }
    });
  }

  it('is translated, not copied, and German has umlauts and says du', () => {
    for (const [code, b] of Object.entries(bundles)) {
      if (code === 'en') continue;
      expect(b['ui2.library.noServersBody'], code).not.toBe(bundles.en['ui2.library.noServersBody']);
    }
    expect(bundles.de['ui2.library.emptyFolderBody']).toContain('wähle');
    expect(bundles.de['ui2.library.noSpeaker']).toContain('Wähle');
  });
});

describe('the styles', () => {
  it('only touch the preview Library', () => {
    const selectors = css.replace(/\/\*[\s\S]*?\*\//g, '').match(/[^{}]+(?=\{)/g)
      .map((s) => s.trim()).filter((s) => s && !s.startsWith('@') && !/^(to|from|\d+%)$/.test(s));
    for (const group of selectors) {
      for (const sel of group.split(',')) expect(sel.trim(), group).toMatch(/^html\.ui2 #view-library\b/);
    }
  });

  it('use only tokens shell2.css defines', () => {
    const tokens = new Set([...css.matchAll(/var\((--u-[a-z0-9-]+)\)/g)].map((m) => m[1]));
    expect(tokens.size).toBeGreaterThan(5);
    for (const tok of tokens) expect(shellCss, tok).toContain(`${tok}:`);
  });

  it('keep every control at least 40px', () => {
    expect(css).toMatch(/\.lib2-crumb \{[^}]*min-height: var\(--u-target\)/);
    expect(css).toMatch(/\.lib2-input,\s*html\.ui2 #view-library \.lib2-select \{[^}]*min-height: var\(--u-target\)/);
    expect(css).toMatch(/\.lib2-row,\s*html\.ui2 #view-library \.lib2-track \{[^}]*min-height: 56px/);
  });
});

describe('the view keeps one copy and both layouts', () => {
  const lib = fileSource('views/library.js');

  it('draws the preview from the same module, only with the shell up', () => {
    expect(srcOf('renderLibrary')).toMatch(/if \(ui2\(\)\) \{ renderLibraryUi2\(el\); return; \}/);
    expect(lib).toContain('const ui2 = () => !!state.ui2Shell;');
  });

  it('keeps the classic toasts and error window behind the same switch', () => {
    const play = srcOf('libraryPlay');
    expect(play).toContain("showToast(t('library.toastPlaying') + ': ' + (item.title || ''));");
    expect(play).toContain('else showError(`PlayURL: $' + '{e}`);');
    expect(srcOf('libraryPlayFolder')).toContain("showToast(t('library.folderQueued', { n: items.length }));");
    expect(srcOf('libraryBrowseCurrent')).toMatch(/if \(ui2\(\)\) \{[\s\S]*\} else if \(looksUnreachable\(e\)\) \{/);
  });

  it('says done only once the speaker plays', () => {
    const play = srcOf('libraryPlay');
    expect(play).toContain("t('ui2.library.starting', { name })");
    expect(play.indexOf("t('ui2.library.playingOn'")).toBeGreaterThan(play.indexOf('verifyLibraryPlayback(item, target, (r)'));
    expect(srcOf('libraryPlayFolder')).toContain("if (v === 'gone')");
    expect(srcOf('waitForPlayState')).toContain("if (ps === 'PLAY_STATE') return 'playing';");
  });

  it('follows a speaker chosen in the sidebar', () => {
    expect(srcOf('pickBoxFromShell')).toContain('libraryFollowSpeaker();');
    expect(srcOf('libraryFollowSpeaker')).toContain("if (!ui2() || state.view !== 'library') return;");
  });
});

describe('answers and failures stay where they were asked', () => {
  it('refreshes the server list without opening a server again', () => {
    expect(srcOf('wireLibraryUi2')).toContain('loadMediaServers({ autoOpen: false })');
    expect(srcOf('loadMediaServers')).toContain('const auto = !autoOpen ? null');
    // The classic refresh keeps opening the remembered server, as before.
    expect(srcOf('renderLibrary')).toContain('ref.onclick = () => loadMediaServers();');
  });

  it('drops to the root only for the remembered folder in the preview', () => {
    const browse = srcOf('libraryBrowseCurrent');
    expect(browse).toContain('(restoring || !ui2())');
    expect(browse.indexOf('libState.restoredPath = false;')).toBeLessThan(browse.indexOf('await BrowseLibrary('));
    expect(srcOf('loadMediaServers')).toContain('libState.restoredPath = !!restored;');
    expect(srcOf('libraryPickServer')).toContain('libState.restoredPath = !!restored;');
  });

  it('writes a folder play\'s verdict only into the folder it was for', () => {
    const play = srcOf('libraryPlayFolder');
    expect(play).toContain('const folder = openFolderId();');
    expect(play.indexOf('if (openFolderId() !== folder) return;')).toBeLessThan(play.indexOf("if (v === 'gone')"));
  });
});

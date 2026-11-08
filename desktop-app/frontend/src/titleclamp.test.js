// Long station and track names stop after two lines with "…" (#1783), in the
// desktop app (key tiles of both layouts, the UI 2.0 speaker lines) and on
// the phone remote (now-playing card, key tiles, Recently played), and the
// full name stays in the element's title.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { speakerStateHTML } from './shell2/speakers/speaker.js';

const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8');
const rule = (css, sel) => {
  const at = css.indexOf(`${sel} {`);
  return at < 0 ? '' : css.slice(at, css.indexOf('}', at));
};

describe('two-line names in the desktop app', () => {
  const css = read('./style.css');

  it('has one clamp rule with the word-break fallback', () => {
    const r = rule(css, '.clamp-2');
    expect(r).toMatch(/-webkit-line-clamp: 2;/);
    expect(r).toMatch(/\bline-clamp: 2;/);
    expect(r).toMatch(/-webkit-box-orient: vertical;/);
    expect(r).toMatch(/overflow: hidden;/);
    expect(r).toMatch(/overflow-wrap: anywhere;/);
  });

  it('clamps every key tile name and keeps the full name as its title', () => {
    const src = read('./views/music/presettiles.js');
    // The empty tile's own short word ("Empty") needs no clamp.
    const names = (src.match(/<div class="name[^"]*"[^\n]*?<\/div>/g) || []).filter((n) => !n.includes("'preset.empty'"));
    // An STR key, a key lost on the speaker, a key of the speaker's own.
    expect(names.length).toBe(3);
    for (const n of names) {
      expect(n).toContain('class="name clamp-2"');
      expect(n).toMatch(/title="\$\{escapeAttr\(/);
    }
  });

  it('clamps what a speaker plays on the UI 2.0 speaker page and list', () => {
    const long = `${'Top 100 DJ Charts, Remix Radio, '.repeat(6)}& "Live" <Sets>`;
    const html = speakerStateHTML({ host: '192.0.2.1' }, {}, long);
    expect(html).toContain('class="sps-now clamp-2"');
    expect(html).toContain('title="Top 100 DJ Charts');
    expect(html).toContain('&quot;Live&quot; &lt;Sets&gt;"');
    expect(read('./shell2/speakers/list.js')).toMatch(/class="spl-now clamp-2" title="\$\{escapeAttr\(row\.now\)\}"/);
  });
});

describe('two-line names on the phone remote', () => {
  const phone = read('../../../internal/webui/assets/index.html');

  it('has the clamp rule with the word-break fallback', () => {
    // The now-playing card's own rule sets display:block, so the clamp names
    // it too, or the card's title would never stop.
    const r = phone.match(/\.clamp2, \.nowcard \.now\.clamp2 \{[^}]*\}/);
    expect(r).not.toBeNull();
    expect(r[0]).toMatch(/-webkit-line-clamp:2;/);
    expect(r[0]).toMatch(/\bline-clamp:2;/);
    expect(r[0]).toMatch(/overflow:hidden;/);
    expect(r[0]).toMatch(/overflow-wrap:anywhere;/);
  });

  it('clamps the now-playing title, the key tiles and the Recently played names, each with its title', () => {
    expect(phone).toContain(`'<span class="now clamp2" title="' + escapeHtml(name) + '">'`);
    expect(phone).toContain(`<div class="name clamp2" title="' + escapeHtml(nm) + '">' + escapeHtml(nm) + '</div>`);
    expect(phone).toMatch(/nmT\.className = 'clamp2';\s*nmT\.textContent = e\.cardName \|\| e\.cardURL \|\| '';\s*nmT\.title = nmT\.textContent;/);
  });
});

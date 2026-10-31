// The one "Put on key" control of the UI 2.0 preview (#1286): the same icon,
// words and name pattern in Radio, Library and Recently played.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { KEYS_ICON, keyButtonHTML, keyButtonName } from './keybtn.js';
import { countText } from './library.js';
import { setLocale } from '../i18n/index.js';

setLocale('en');

const src = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8');

describe('Put on key', () => {
  it('starts its name with the words it shows, then what and where', () => {
    expect(keyButtonName('Jazz24', 'Kitchen')).toBe('Put on key: Jazz24 on Kitchen');
    expect(keyButtonName('Jazz24', '')).toBe('Put on key: Jazz24');
  });

  it('draws the six keys icon and the words, escaped, with the view hook', () => {
    const html = keyButtonHTML({ cls: 'rc-pick', attrs: 'id="recPick3"', name: 'A "B" <C>', speaker: 'Kitchen' });
    expect(html).toContain('class="u-btn u-btn--secondary u-keybtn rc-pick"');
    expect(html).toContain('id="recPick3"');
    expect(html).toContain('aria-label="Put on key: A &quot;B&quot; &lt;C&gt; on Kitchen"');
    expect(html).toContain(KEYS_ICON);
    expect(html).toContain('<span class="u-keybtn-text">Put on key</span>');
  });

  it('is the control all three views use', () => {
    expect(src('../views/music/ui2stations.js')).toContain('keyButtonHTML(');
    expect(src('./library.js')).toContain('keyButtonHTML(');
    expect(src('../views/recent.js')).toContain('keyButtonHTML(');
  });
});

describe('the Library count', () => {
  it('says what it counts', () => {
    expect(countText('3')).toBe('Entries: 3');
    expect(countText('1 / 3+')).toBe('Entries: 1 / 3+');
  });
});

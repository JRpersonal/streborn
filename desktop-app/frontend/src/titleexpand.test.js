// Discussion #1811: a click or tap on a cut-off title in a list opens that
// title to its full wrapped length, and the next one closes it. Only the text
// reacts and only when it is cut off, a title inside a control is left to the
// control, and nothing new is drawn. Desktop app and phone remote.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  CONTROL_SELECTOR, MAX_OPEN, OPEN_CLASS, TITLE_SELECTOR, isCutOff, openTitles, titlePress,
} from './titleexpand.js';

const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8').replace(/\r\n/g, '\n');
const phone = read('../../../internal/webui/assets/index.html');

function phoneFn(name) {
  const start = phone.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} not found on the phone page`);
  return phone.slice(start, phone.indexOf('\n}\n', start) + 2);
}

// [what, inControl, open, cutOff, selecting, the press]
const presses = [
  ['a cut-off title', false, false, true, false, 'open'],
  ['an opened title', false, true, false, false, 'close'],
  ['a title that fits', false, false, false, false, ''],
  ['a cut-off title inside a button or key tile', true, false, true, false, ''],
  ['an opened title that ended up inside a control', true, true, false, false, ''],
  ['a cut-off title the click finished selecting', false, false, true, true, ''],
  ['an opened title the click finished selecting', false, true, false, true, ''],
];

describe('one press on a title', () => {
  const phonePress = new Function(`${phoneFn('titlePress')}\nreturn titlePress;`)();

  for (const [what, inControl, open, cutOff, selecting, want] of presses) {
    it(`${what}: ${want || 'nothing'}`, () => {
      expect(titlePress({ inControl, open, cutOff, selecting })).toBe(want);
      // The phone remote keeps the same rule in its own page.
      expect(phonePress(inControl, open, cutOff, selecting)).toBe(want);
    });
  }

  it('does nothing when asked about nothing', () => {
    expect(titlePress()).toBe('');
  });
});

describe('cut off or not', () => {
  const phoneCutOff = new Function(`${phoneFn('titleCutOff')}\nreturn titleCutOff;`)();
  const box = (scrollWidth, clientWidth, scrollHeight, clientHeight) => ({ scrollWidth, clientWidth, scrollHeight, clientHeight });

  for (const [what, el, want] of [
    ['one line, wider than its room', box(400, 200, 18, 18), true],
    ['two lines, taller than its box', box(200, 200, 90, 36), true],
    ['fits', box(200, 200, 18, 18), false],
    ['one pixel of rounding', box(201, 200, 19, 18), false],
  ]) {
    it(`${what}: ${want}`, () => {
      expect(isCutOff(el)).toBe(want);
      expect(phoneCutOff(el)).toBe(want);
    });
  }
});

describe('the opened names are remembered for rows drawn again', () => {
  it('adds, forgets and answers', () => {
    const o = openTitles();
    o.add('Evening Sessions');
    expect(o.has('Evening Sessions')).toBe(true);
    expect(o.has('Jazz24')).toBe(false);
    expect(o.has('')).toBe(false);
    o.delete('Evening Sessions');
    expect(o.has('Evening Sessions')).toBe(false);
    expect(o.size).toBe(0);
  });

  it('keeps one entry per name and drops the oldest past the limit', () => {
    const o = openTitles(3);
    for (const n of ['a', 'b', 'a', 'c', 'd']) o.add(n);
    expect(o.size).toBe(3);
    expect(o.has('b')).toBe(false);
    expect(['a', 'c', 'd'].every((n) => o.has(n))).toBe(true);
    o.add('');
    expect(o.size).toBe(3);
    expect(MAX_OPEN).toBe(50);
  });

  it('the phone remote keeps the same limit', () => {
    expect(phone).toContain('var TITLE_MAX_OPEN = 50;');
  });
});

describe('where it applies in the desktop app', () => {
  const css = read('./style.css');
  const main = read('./main.js');

  it('names the cut-off titles of the lists and leaves every control alone', () => {
    expect(TITLE_SELECTOR.split(', ')).toEqual(['.result-name-text', '.rc-name', '.rc-tr-main', '.clamp-2']);
    // Each of them is cut by the stylesheet, or there would be nothing to open.
    expect(css).toContain('.result-name-text { overflow: hidden; text-overflow: ellipsis; }');
    expect(css).toMatch(/\.rc-name \{[^}]*white-space: nowrap; overflow: hidden; text-overflow: ellipsis;/);
    expect(css).toMatch(/\.rc-tr-main \{[^}]*white-space: nowrap; text-overflow: ellipsis;/);
    for (const c of ['button', 'a', 'label', 'summary', '[role="button"]', '.preset']) {
      expect(CONTROL_SELECTOR.split(', ')).toContain(c);
    }
  });

  it('opens them with one rule that beats each cut', () => {
    const at = css.indexOf(`:is(.result-name-text, .rc-name, .rc-tr-main, .clamp-2).${OPEN_CLASS} {`);
    expect(at).toBeGreaterThan(-1);
    const rule = css.slice(at, css.indexOf('}', at));
    for (const d of ['white-space: normal;', 'overflow: visible;', 'line-clamp: unset;', '-webkit-line-clamp: unset;', 'overflow-wrap: anywhere;']) {
      expect(rule).toContain(d);
    }
  });

  it('is mounted once for the whole window, in both layouts', () => {
    expect((main.match(/mountTitleExpand\(\);/g) || []).length).toBe(1);
    // Right after the UI 2.0 shell is built, and not inside its branch.
    expect(main).toMatch(/\nif \(getUi2Preview\(\)\) safeMountShell2\(\);\n[^\n]*\n[^\n]*\nmountTitleExpand\(\);\n/);
  });

  it('adds no control, no tab stop and no string', () => {
    const src = read('./titleexpand.js');
    expect(src).not.toMatch(/tabindex|tabIndex|createElement|innerHTML|\bt\(/);
  });
});

describe('where it applies on the phone remote', () => {
  it('names the two-line names and the one-line server names, and leaves every control alone', () => {
    expect(phone).toContain("var TITLE_SEL = '.clamp2, .cut1';");
    expect(phone).toContain(`var TITLE_CTRL = '${CONTROL_SELECTOR}';`);
  });

  it('opens them with rules that beat each cut', () => {
    expect(phone).toContain('.clamp2.title-open, .nowcard .now.clamp2.title-open { -webkit-line-clamp:unset; line-clamp:unset; overflow:visible; }');
    expect(phone).toContain('.cut1.title-open { overflow:visible; text-overflow:clip; white-space:normal; overflow-wrap:anywhere; }');
    // The server name is cut by a class now, so the open rule can reach it.
    expect(phone).toContain("name.className = 'cut1';");
    expect(phone).not.toContain("name.style.cssText = 'flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap'");
  });

  it('opens a remembered title again on what the page draws again', () => {
    expect(phone).toMatch(/if \(openTitles\.indexOf\(titleName\(el\)\) >= 0 && !el\.closest\(TITLE_CTRL\)\) el\.classList\.add\('title-open'\);/);
  });
});

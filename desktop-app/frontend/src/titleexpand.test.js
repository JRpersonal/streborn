// Discussion #1811: a click or tap on a cut-off title in a list opens that
// title to its full wrapped length, and the next one closes it. Only the text
// reacts and only when it is cut off, a title inside a control is left to the
// control. A title that opens carries a small arrow, down while closed and up
// while open, and a title that fits carries nothing. Desktop app and phone
// remote.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  CONTROL_SELECTOR, CUT_CLASS, MAX_OPEN, OPEN_CLASS, TITLE_SELECTOR, isCutOff, markCutOff, openTitles, titleMark, titlePress,
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

// A title as the marking sees it: its classes, whether it is on the page,
// whether it sits in a control, and its text against its room. The arrow takes
// `room` pixels from a marked title, as the stylesheet's end padding does.
function fakeTitle({ text, width, classes = [], inControl = false, connected = true, room = 18 }) {
  const set = new Set(classes);
  const el = {
    measured: 0,
    isConnected: connected,
    classList: {
      contains: (c) => set.has(c),
      add: (c) => set.add(c),
      remove: (c) => set.delete(c),
    },
    closest: () => (inControl ? {} : null),
    get clientWidth() { return width; },
    get scrollWidth() {
      el.measured += 1;
      const inner = set.has(CUT_CLASS) ? width - room : width;
      return text > inner ? text + (width - inner) : width;
    },
    scrollHeight: 18,
    clientHeight: 18,
    has: (c) => set.has(c),
  };
  return el;
}

describe('the arrow on a title that opens', () => {
  const phoneMark = new Function(`${phoneFn('titleMark')}\nreturn titleMark;`)();
  // The phone page's marking, run on the same fake titles.
  const phoneMarkCut = new Function('document', 'TITLE_CTRL', 'titleCutOff', 'titleMark',
    `${phoneFn('titleMarkCut')}\nreturn titleMarkCut;`)(
    { documentElement: { contains: (el) => el.isConnected } }, CONTROL_SELECTOR, isCutOff, phoneMark,
  );

  // [what, inControl, open, cutOff, the arrow]
  for (const [what, inControl, open, cutOff, want] of [
    ['a cut-off title', false, false, true, true],
    ['a title that fits', false, false, false, false],
    ['an opened title', false, true, false, true],
    ['a cut-off title inside a button or key tile', true, false, true, false],
    ['an opened title that ended up inside a control', true, true, false, false],
  ]) {
    it(`${what}: ${want ? 'arrow' : 'nothing'}`, () => {
      expect(titleMark({ inControl, open, cutOff })).toBe(want);
      expect(phoneMark(inControl, open, cutOff)).toBe(want);
    });
  }

  it('shows nothing when asked about nothing', () => {
    expect(titleMark()).toBe(false);
  });

  for (const [who, mark] of [['desktop app', markCutOff], ['phone remote', phoneMarkCut]]) {
    describe(who, () => {
      it('marks the row that is cut off and leaves the row that fits', () => {
        const long = fakeTitle({ text: 400, width: 200 });
        const short = fakeTitle({ text: 120, width: 200 });
        mark([long, short]);
        expect(long.has(CUT_CLASS)).toBe(true);
        expect(short.has(CUT_CLASS)).toBe(false);
      });

      it('takes the arrow away when the window grew and the title fits again', () => {
        const el = fakeTitle({ text: 190, width: 150 });
        mark([el]);
        expect(el.has(CUT_CLASS)).toBe(true);
        const wide = fakeTitle({ text: 190, width: 200, classes: [CUT_CLASS] });
        mark([wide]);
        // It fits the line, but not the line less the arrow's room: measured
        // with the arrow still on, it would never lose it.
        expect(wide.has(CUT_CLASS)).toBe(false);
      });

      it('gives the same answer every time, so the arrow cannot flicker', () => {
        const el = fakeTitle({ text: 400, width: 200 });
        for (let i = 0; i < 3; i += 1) {
          mark([el]);
          expect(el.has(CUT_CLASS)).toBe(true);
        }
      });

      it('leaves an opened title, a title in a control and a row that is gone alone', () => {
        const open = fakeTitle({ text: 400, width: 200, classes: [OPEN_CLASS, CUT_CLASS] });
        const key = fakeTitle({ text: 400, width: 200, inControl: true });
        const gone = fakeTitle({ text: 400, width: 200, connected: false });
        mark([open, key, gone]);
        // The opened one keeps its mark for when it closes, and is not measured.
        expect(open.has(CUT_CLASS)).toBe(true);
        expect(open.has(OPEN_CLASS)).toBe(true);
        expect(key.has(CUT_CLASS)).toBe(false);
        expect(gone.has(CUT_CLASS)).toBe(false);
        expect(open.measured + key.measured + gone.measured).toBe(0);
      });

      it('measures a long list once per row', () => {
        const rows = Array.from({ length: 500 }, (_, i) => fakeTitle({ text: i % 2 ? 400 : 100, width: 200 }));
        mark(rows);
        expect(rows.every((r) => r.measured === 1)).toBe(true);
        expect(rows.filter((r) => r.has(CUT_CLASS)).length).toBe(250);
      });
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

  it('draws the arrow as a shape on the cut-off and the opened title, down and up', () => {
    expect(CUT_CLASS).toBe('title-cut');
    const sel = ':is(.result-name-text, .rc-name, .rc-tr-main, .clamp-2)';
    const rule = (head) => {
      const at = css.indexOf(`${head} {`);
      expect(at, head).toBeGreaterThan(-1);
      return css.slice(at, css.indexOf('}', at));
    };
    // Its room exists only on a marked title, and it is out of the flow, so a
    // row that fits and a closed row keep their size.
    const box = rule(`${sel}:is(.${CUT_CLASS}, .${OPEN_CLASS})`);
    expect(box).toContain('padding-inline-end: 18px;');
    expect(box).toContain('position: relative;');
    const arrow = rule(`${sel}:is(.${CUT_CLASS}, .${OPEN_CLASS})::after`);
    for (const d of ["content: '';", 'position: absolute;', 'inset-inline-end: 5px;', 'border: solid var(--c-muted);', 'transform: rotate(45deg);']) {
      expect(arrow).toContain(d);
    }
    // No glyph (a Debian desktop lacks many) and no colour of its own.
    expect(arrow).not.toMatch(/content: '[^']+'|#[0-9a-f]{3,6}|\bleft:|\bright:/i);
    expect(rule(`${sel}.${OPEN_CLASS}::after`)).toContain('transform: rotate(-135deg);');
  });

  it('keeps the arrow true with a ResizeObserver, measures on the next frame, and has no timer', () => {
    const src = read('./titleexpand.js');
    expect(src).toContain("watch = (el) => { sizes.observe(el, { box: 'border-box' }); measureSoon(el); };");
    // Never inside the observer's own report, which only notes what is due.
    expect(src).toContain('new win.ResizeObserver((entries) => entries.forEach((e) => measureSoon(e.target)))');
    expect(src).toContain('win.requestAnimationFrame(() => { const els = [...due]; due = null; markCutOff(els); });');
    expect(src).toContain("if (press === 'close') markCutOff([el]);");
    expect(src).not.toMatch(/setInterval|setTimeout/);
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

  it('draws the same arrow, with more room for a finger', () => {
    expect(phone).toContain('.clamp2.title-cut, .cut1.title-cut, .clamp2.title-open, .cut1.title-open { position:relative; padding-inline-end:26px; cursor:pointer; }');
    expect(phone).toContain(".clamp2.title-cut::after, .cut1.title-cut::after, .clamp2.title-open::after, .cut1.title-open::after { content:''; position:absolute; inset-inline-end:8px;");
    expect(phone).toMatch(/\.title-open::after, \.cut1\.title-open::after \{[^}]*transform:rotate\(-135deg\); \}/);
    expect(phone).toContain("titleWatch = function (el) { titleSizes.observe(el, { box: 'border-box' }); titleMeasureSoon(el); };");
    expect(phone).toContain('window.requestAnimationFrame(function () { var els = titleDue; titleDue = null; titleMarkCut(els); });');
    expect(phone).toContain("if (press === 'close') titleMarkCut([el]);");
  });

  it('opens a remembered title again on what the page draws again', () => {
    expect(phone).toMatch(/if \(openTitles\.indexOf\(titleName\(el\)\) >= 0 && !el\.closest\(TITLE_CTRL\)\) el\.classList\.add\('title-open'\);/);
  });
});

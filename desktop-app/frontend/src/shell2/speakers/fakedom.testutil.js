// A small stand-in DOM for the vitest specs of the phase 5 section messages
// (sectionmsg.test.js, views/settings/shared.test.js). vitest runs without a
// DOM on purpose (vitest.config.js: modules must stay import-safe without a
// document), so the tests build the few nodes they need from this.
//
// Supported: createElement, getElementById, appendChild, append,
// insertBefore, remove, contains, closest, matches, querySelector(All) with
// compound selectors (tag, #id, .class, [attr], [attr="v"]), comma lists and
// a leading ":scope > ", classList, dataset, attributes, textContent,
// addEventListener and click(). Not a test file itself (no .test.js suffix).

function parseCompound(sel) {
  const out = { tag: '', id: '', classes: [], attrs: [] };
  const re = /(^[a-zA-Z][\w-]*)|#([\w-]+)|\.([\w-]+)|\[([\w-]+)(?:="([^"]*)")?\]/g;
  let m;
  while ((m = re.exec(sel))) {
    if (m[1]) out.tag = m[1].toUpperCase();
    else if (m[2]) out.id = m[2];
    else if (m[3]) out.classes.push(m[3]);
    else if (m[4]) out.attrs.push([m[4], m[5]]);
  }
  return out;
}

function matchesCompound(el, c) {
  if (c.tag && el.tagName !== c.tag) return false;
  if (c.id && el.id !== c.id) return false;
  for (const k of c.classes) if (!el.classList.contains(k)) return false;
  for (const [k, v] of c.attrs) {
    const got = el.getAttribute(k);
    if (got === null) return false;
    if (v !== undefined && got !== v) return false;
  }
  return true;
}

function matchesSelector(el, sel) {
  return sel.split(',').map((s) => s.trim()).some((s) => matchesCompound(el, parseCompound(s)));
}

class FakeClassList {
  constructor(el) { this.el = el; }
  get list() { return (this.el.className || '').split(/\s+/).filter(Boolean); }
  contains(c) { return this.list.includes(c); }
  add(...cs) { const l = this.list; for (const c of cs) if (!l.includes(c)) l.push(c); this.el.className = l.join(' '); }
  remove(...cs) { this.el.className = this.list.filter((c) => !cs.includes(c)).join(' '); }
  toggle(c, on) {
    const want = on === undefined ? !this.contains(c) : !!on;
    if (want) this.add(c); else this.remove(c);
    return want;
  }
}

const DATA_ATTR = (k) => 'data-' + k.replace(/[A-Z]/g, (ch) => '-' + ch.toLowerCase());

export class FakeElement {
  constructor(doc, tag) {
    this.ownerDocument = doc;
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.attrs = {};
    this.className = '';
    this.id = '';
    this.hidden = false;
    this.disabled = false;
    this._text = '';
    this.handlers = {};
    this.classList = new FakeClassList(this);
    this.focused = false;
    const self = this;
    this.dataset = new Proxy({}, {
      get: (_, k) => (typeof k === 'string' ? (self.getAttribute(DATA_ATTR(k)) ?? undefined) : undefined),
      set: (_, k, v) => { self.setAttribute(DATA_ATTR(k), v); return true; },
      deleteProperty: (_, k) => { self.removeAttribute(DATA_ATTR(k)); return true; },
    });
  }
  get textContent() { return this._text + this.children.map((c) => c.textContent).join(''); }
  set textContent(v) { this._text = String(v); this.children.forEach((c) => { c.parentNode = null; }); this.children = []; }
  get nextSibling() {
    if (!this.parentNode) return null;
    const i = this.parentNode.children.indexOf(this);
    return this.parentNode.children[i + 1] || null;
  }
  get nextElementSibling() { return this.nextSibling; }
  get firstChild() { return this.children[0] || null; }
  setAttribute(k, v) {
    if (k === 'id') { this.id = String(v); return; }
    if (k === 'class') { this.className = String(v); return; }
    this.attrs[k] = String(v);
  }
  getAttribute(k) {
    if (k === 'id') return this.id || null;
    if (k === 'class') return this.className || null;
    return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null;
  }
  removeAttribute(k) { delete this.attrs[k]; }
  hasAttribute(k) { return this.getAttribute(k) !== null; }
  appendChild(c) {
    if (c.parentNode) c.remove();
    c.parentNode = this;
    this.children.push(c);
    return c;
  }
  append(...cs) { cs.forEach((c) => this.appendChild(c)); }
  insertBefore(c, ref) {
    if (!ref) return this.appendChild(c);
    if (c.parentNode) c.remove();
    const i = this.children.indexOf(ref);
    c.parentNode = this;
    this.children.splice(i < 0 ? this.children.length : i, 0, c);
    return c;
  }
  remove() {
    if (!this.parentNode) return;
    const p = this.parentNode;
    p.children = p.children.filter((x) => x !== this);
    this.parentNode = null;
  }
  contains(o) { return o === this || this.children.some((c) => c.contains(o)); }
  matches(sel) { return matchesSelector(this, sel); }
  closest(sel) {
    let n = this;
    while (n && n instanceof FakeElement) {
      if (n.matches(sel)) return n;
      n = n.parentNode;
    }
    return null;
  }
  descendants() { return this.children.flatMap((c) => [c, ...c.descendants()]); }
  querySelectorAll(sel) {
    const scoped = sel.trim().startsWith(':scope >');
    if (scoped) {
      const rest = sel.trim().slice(':scope >'.length).trim();
      return this.children.filter((c) => c.matches(rest));
    }
    return this.descendants().filter((c) => c.matches(sel));
  }
  querySelector(sel) { return this.querySelectorAll(sel)[0] || null; }
  addEventListener(type, fn) { (this.handlers[type] = this.handlers[type] || []).push(fn); }
  removeEventListener(type, fn) { this.handlers[type] = (this.handlers[type] || []).filter((f) => f !== fn); }
  dispatch(type, ev = {}) { (this.handlers[type] || []).forEach((fn) => fn({ target: this, preventDefault() {}, ...ev })); }
  click() { this.dispatch('click'); if (typeof this.onclick === 'function') this.onclick({ target: this }); }
  focus() { this.focused = true; this.ownerDocument.activeElement = this; }
  scrollIntoView() { this.scrolled = (this.scrolled || 0) + 1; }
}

export class FakeDocument {
  constructor() {
    this.body = new FakeElement(this, 'body');
    this.activeElement = null;
  }
  createElement(tag) { return new FakeElement(this, tag); }
  getElementById(id) { return this.body.descendants().find((e) => e.id === id) || null; }
  querySelector(sel) { return this.body.querySelector(sel); }
  querySelectorAll(sel) { return this.body.querySelectorAll(sel); }
}

// el builds a node: el(doc, 'div', { id, class, attrs, text, hidden }, children)
export function el(doc, tag, props = {}, children = []) {
  const e = doc.createElement(tag);
  if (props.id) e.id = props.id;
  if (props.class) e.className = props.class;
  if (props.text) e.textContent = props.text;
  if (props.hidden) e.hidden = true;
  for (const [k, v] of Object.entries(props.attrs || {})) e.setAttribute(k, v);
  children.forEach((c) => e.appendChild(c));
  return e;
}

// speakerPage builds the bit of the preview's speaker page the reports look
// at: #ui2SpkPanels with the open tab, a wrapper per tab and the cards in it.
// tabs: { overview: [card, ...], sound: [...] }; open: the open tab.
export function speakerPage(doc, tabs, open = 'overview') {
  const panels = el(doc, 'div', { id: 'ui2SpkPanels', attrs: { 'data-tab': open } });
  const panel = el(doc, 'div', { class: 'sps-panel', id: `ui2SpkPanel-${open}` });
  const body = el(doc, 'div', { id: 'settingsBody' });
  for (const [tab, cards] of Object.entries(tabs)) {
    body.appendChild(el(doc, 'div', { class: 'sps-sec', attrs: { 'data-ui2-tab': tab } }, cards));
  }
  panel.appendChild(body);
  panels.appendChild(panel);
  doc.body.appendChild(panels);
  return panels;
}

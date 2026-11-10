// Every field of the shared `state` object is declared in state.js.
//
// Half of the fields used to exist only because some module assigned them
// (state.zoneLive, state.spotifyNow, state.otaTargetHost, ...), so the one
// place that describes the app's state described half of it, and a typo in a
// field name made a second field instead of an error. This test parses every
// source file and fails on a write to `state.<field>` that the literal in
// state.js does not name.
//
// It goes by the NAME `state`: every module calls the shared object that
// (import { state }, or a `state` parameter as in speakerPurge.js). A module
// that wants a private object of its own must call it something else.
import { describe, expect, it } from 'vitest';
import * as acorn from 'acorn';
import { state } from './state.js';
import { fileSource, sourceFiles } from './test/source.js';

// rootField returns X for an expression rooted at state.X (state.X,
// state.X.timer, state.X[id]), else ''.
function rootField(node) {
  let n = node;
  while (n && n.type === 'MemberExpression') {
    const o = n.object;
    if (o.type === 'Identifier' && o.name === 'state') {
      return !n.computed && n.property.type === 'Identifier' ? n.property.name : '';
    }
    n = o;
  }
  return '';
}

// targets lists the expressions an assignment target writes to, through
// destructuring patterns as well.
function targets(node, out = []) {
  if (!node) return out;
  switch (node.type) {
    case 'MemberExpression': out.push(node); break;
    case 'ArrayPattern': node.elements.forEach((e) => targets(e, out)); break;
    case 'ObjectPattern': node.properties.forEach((p) => targets(p.type === 'RestElement' ? p.argument : p.value, out)); break;
    case 'AssignmentPattern': targets(node.left, out); break;
    case 'RestElement': targets(node.argument, out); break;
    default: break;
  }
  return out;
}

// stateWrites returns the fields of `state` that code writes: assignments of
// every kind, ++ and --, delete, and a destructuring that lands in a field.
function stateWrites(code) {
  const found = new Set();
  const note = (node) => { const f = rootField(node); if (f) found.add(f); };
  (function walk(node) {
    if (!node || typeof node !== 'object') return;
    if (Array.isArray(node)) { node.forEach(walk); return; }
    if (typeof node.type !== 'string') return;
    if (node.type === 'AssignmentExpression') targets(node.left).forEach(note);
    else if (node.type === 'UpdateExpression') note(node.argument);
    else if (node.type === 'UnaryExpression' && node.operator === 'delete') note(node.argument);
    else if (node.type === 'ForInStatement' || node.type === 'ForOfStatement') targets(node.left).forEach(note);
    for (const key of Object.keys(node)) {
      if (key === 'type' || key === 'start' || key === 'end') continue;
      walk(node[key]);
    }
  })(acorn.parse(code, { ecmaVersion: 'latest', sourceType: 'module' }));
  return found;
}

describe('the scanner', () => {
  it('sees every kind of write', () => {
    const code = `
      state.a = 1; state.b += 1; state.c ||= {}; state.d++; --state.e;
      delete state.f; state.g.timer = 2; state.h[id] = true; delete state.i[id];
      [state.j, state.k = 3] = pair; ({ x: state.l, ...state.m } = obj);
      for (state.n of list) {}
    `;
    expect([...stateWrites(code)].sort()).toEqual('abcdefghijklmn'.split(''));
  });

  it('leaves reads, other objects, comments and strings alone', () => {
    const code = `
      // state.z = 1
      const s = 'state.y = 2';
      if (state.a === 1 || state.b == null) run(state.c, mstate.d, x.state.e);
      mstate.f = 1; other.state.g = 2; state[key] = 3; const v = state.h.i;
    `;
    expect([...stateWrites(code)]).toEqual([]);
  });
});

describe('state.js', () => {
  const declared = new Set(Object.keys(state));
  const writers = new Map(); // field -> [files]
  for (const rel of sourceFiles()) {
    for (const field of stateWrites(fileSource(rel))) {
      if (!writers.has(field)) writers.set(field, []);
      writers.get(field).push(rel);
    }
  }

  it('is actually scanned', () => {
    // Not vacuous: the scan finds the writes this test was written for.
    expect(writers.size).toBeGreaterThan(90);
    for (const known of ['boxes', 'currentBox', 'zoneLive', 'spotifyNow', 'otaTargetHost', 'speakerIssues']) {
      expect(writers.has(known), known).toBe(true);
    }
  });

  it('declares every field any module writes', () => {
    const missing = [...writers]
      .filter(([field]) => !declared.has(field))
      .map(([field, files]) => `state.${field} (written in ${files.join(', ')})`);
    expect(missing, 'declare these in src/state.js with the default the readers expect').toEqual([]);
  });

  it('keeps the defaults a missing field used to have', () => {
    // The fields that were declared late keep the value their readers saw
    // before: undefined unless every reader treats the default the same way.
    expect(state.zoneLive).toBeUndefined();
    expect(state.zoneMaster).toBe(state.zoneMasterHandPick);
    expect(state.spotifyNowGen).toBe(0);
    expect(state.staleStereo).toEqual([]);
    expect(state.discovery).toBeNull();
  });
});

// Source-text access for tests that cannot import the module they pin.
//
// main.js and the views run top-level code that needs a DOM and the Wails
// runtime, so vitest (environment: 'node', see vitest.config.js) cannot import
// them. The tests that pin their wiring therefore read the files as text. Until
// this helper existed every such test named the FILE it read, so moving a
// function from main.js into a module of its own turned dozens of tests red
// without any behaviour changing. Everything here looks code up by NAME across
// all of src/ instead, so a move between files is invisible to the tests.
//
// Three ways in, from coarse to fine:
//   allSource()          every non-test .js under src/, concatenated, for
//                        "this wiring exists somewhere" and for negatives that
//                        must hold everywhere;
//   srcOf(name)          one declaration by name, cut with a real parser
//                        (acorn, a transitive devDependency through eslint),
//                        so the cut ends at the declaration's own closing
//                        brace and not at the first "\n}" that happens to
//                        follow;
//   srcContaining(mark)  the top-level statement that holds a marker, for the
//                        few anchors that are not a declaration name;
//   srcBetween(a, b)     a marker-to-marker slice inside the one file that
//                        holds the first marker.
// fileSource(rel) stays for the tests whose subject IS a particular file (an
// import that must live in a given module, a module that must stay DOM-free).
//
// Line endings are normalised to "\n": a Windows checkout has CRLF and a Linux
// one LF, and slices keyed on a newline cut different text on each.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import * as acorn from 'acorn';

const HELPER_DIR = dirname(fileURLToPath(import.meta.url));
const SRC_DIR = join(HELPER_DIR, '..');

let files = null; // Map relPath -> text, in sorted order
let joined = null;
const parsed = new Map(); // relPath -> { body, decls: Map name -> [cand] }

function normalise(text) {
  return text.replace(/\r\n/g, '\n');
}

function walkDir(dir, out) {
  for (const name of readdirSync(dir).sort()) {
    const full = join(dir, name);
    if (full === HELPER_DIR) continue;
    const st = statSync(full);
    if (st.isDirectory()) {
      walkDir(full, out);
    } else if (name.endsWith('.js') && !name.endsWith('.test.js')) {
      out.push(full);
    }
  }
}

function load() {
  if (files) return files;
  const paths = [];
  walkDir(SRC_DIR, paths);
  files = new Map();
  for (const p of paths) {
    files.set(relative(SRC_DIR, p).split('\\').join('/'), normalise(readFileSync(p, 'utf8')));
  }
  return files;
}

// sourceFiles lists every file allSource() is built from, as paths relative to
// src/ with forward slashes ('main.js', 'views/settings.js').
export function sourceFiles() {
  return [...load().keys()];
}

// fileSource returns one file's text. Only for tests whose subject is the file
// itself; everything else should look code up by name.
export function fileSource(relPath) {
  const key = relPath.split('\\').join('/');
  const text = load().get(key);
  if (text === undefined) {
    throw new Error(`fileSource: no such source file ${relPath} (known: ${sourceFiles().join(', ')})`);
  }
  return text;
}

// allSource returns every source file concatenated, each preceded by a line
// naming it, so a failing assertion's context still says where the text is.
export function allSource() {
  if (joined === null) {
    joined = [...load()].map(([rel, text]) => `\n// ===== ${rel} =====\n${text}`).join('\n');
  }
  return joined;
}

function lineOf(text, offset) {
  let n = 1;
  for (let i = 0; i < offset; i++) if (text.charCodeAt(i) === 10) n++;
  return n;
}

function isFn(node) {
  return !!node && (node.type === 'FunctionExpression' || node.type === 'ArrowFunctionExpression');
}

// collect walks one file's AST and records every named declaration: function
// declarations, variable declarators, and object methods. depth counts the
// enclosing functions, so a top-level declaration (depth 0) beats a nested
// helper of the same name.
function collect(node, depth, out) {
  if (!node || typeof node !== 'object') return;
  if (Array.isArray(node)) {
    for (const n of node) collect(n, depth, out);
    return;
  }
  if (typeof node.type !== 'string') return;

  const add = (name, start, end, kind) => {
    if (!out.has(name)) out.set(name, []);
    out.get(name).push({ name, start, end, depth, kind });
  };

  let inner = depth;
  switch (node.type) {
    case 'FunctionDeclaration':
      if (node.id) add(node.id.name, node.start, node.end, 'function');
      inner = depth + 1;
      break;
    case 'FunctionExpression':
    case 'ArrowFunctionExpression':
      inner = depth + 1;
      break;
    case 'VariableDeclaration':
      for (const d of node.declarations) {
        if (d.id && d.id.type === 'Identifier') {
          const single = node.declarations.length === 1;
          add(d.id.name, single ? node.start : d.start, single ? node.end : d.end, node.kind);
        }
      }
      break;
    case 'Property':
    case 'MethodDefinition':
      if (!node.computed && isFn(node.value)) {
        const key = node.key.type === 'Identifier' ? node.key.name : String(node.key.value);
        // A method sits inside its object, so it ranks below a free function.
        if (!out.has(key)) out.set(key, []);
        out.get(key).push({ name: key, start: node.start, end: node.end, depth: depth + 1, kind: 'method' });
      }
      break;
    default:
      break;
  }

  for (const key of Object.keys(node)) {
    if (key === 'type' || key === 'start' || key === 'end' || key === 'loc') continue;
    collect(node[key], inner, out);
  }
}

function parseFile(rel) {
  if (parsed.has(rel)) return parsed.get(rel);
  const text = fileSource(rel);
  let ast;
  try {
    ast = acorn.parse(text, { ecmaVersion: 'latest', sourceType: 'module' });
  } catch (e) {
    throw new Error(`source helper: cannot parse ${rel}: ${e.message}`, { cause: e });
  }
  const decls = new Map();
  collect(ast.body, 0, decls);
  const entry = { body: ast.body, decls };
  parsed.set(rel, entry);
  return entry;
}

function candidatesFor(name) {
  const out = [];
  for (const rel of sourceFiles()) {
    // A cheap text check first: parsing every file for every lookup would cost
    // each test worker a few hundred milliseconds.
    if (!fileSource(rel).includes(name)) continue;
    const { decls } = parseFile(rel);
    for (const c of decls.get(name) || []) out.push({ ...c, file: rel });
  }
  return out;
}

function describe(c) {
  return `${c.file}:${lineOf(fileSource(c.file), c.start)} (${c.kind}, nesting ${c.depth})`;
}

function pick(name, opts) {
  let cands = candidatesFor(name);
  if (cands.length === 0) {
    throw new Error(`srcOf: no declaration named ${name} in any file under src/`);
  }
  if (opts.file) {
    const key = opts.file.split('\\').join('/');
    cands = cands.filter((c) => c.file === key);
    if (cands.length === 0) throw new Error(`srcOf: ${name} is not declared in ${opts.file}`);
  }
  if (opts.within) {
    const outer = pick(opts.within, {});
    cands = cands.filter((c) => c.file === outer.file && c.start >= outer.start && c.end <= outer.end);
    if (cands.length === 0) throw new Error(`srcOf: no ${name} inside ${opts.within} (${describe(outer)})`);
  }
  if (opts.has) {
    cands = cands.filter((c) => fileSource(c.file).slice(c.start, c.end).includes(opts.has));
    if (cands.length === 0) throw new Error(`srcOf: no ${name} whose text contains ${JSON.stringify(opts.has)}`);
  }
  // A module-level declaration is the canonical one; nested helpers that
  // happen to share its name (a local `mine`, a local `play`) rank below it.
  // Several nested ones with nobody at module level is a real ambiguity.
  if (cands.some((c) => c.depth === 0)) cands = cands.filter((c) => c.depth === 0);
  if (cands.length > 1) {
    throw new Error(`srcOf: ${name} is declared more than once; pass { within }, { has } or { file }:\n  ${cands.map(describe).join('\n  ')}`);
  }
  return cands[0];
}

// srcOf returns the source text of a declaration found by name in any file:
// `function name(`, `async function name(`, `const name = (`, `let name =`, a
// `const NAME = {...}` table, or an object method `name() {`. A module-level
// declaration wins over a nested one of the same name; anything still
// ambiguous is an error, resolved with { within: 'enclosingFunction' }, with
// { has: 'a string only the wanted one contains' } or, as a last resort, with
// { file: 'views/settings.js' }.
export function srcOf(name, opts = {}) {
  const c = pick(name, opts);
  return fileSource(c.file).slice(c.start, c.end);
}

// declaredIn names the file that srcOf(name) would read from.
export function declaredIn(name, opts = {}) {
  return pick(name, opts).file;
}

// fileWith names the one file whose text contains marker. More than one file
// is an error: the caller meant a specific place and must narrow the anchor.
export function fileWith(marker) {
  const hits = sourceFiles().filter((rel) => fileSource(rel).includes(marker));
  if (hits.length === 0) throw new Error(`no source file contains ${JSON.stringify(marker)}`);
  if (hits.length > 1) throw new Error(`${JSON.stringify(marker)} occurs in more than one file: ${hits.join(', ')}`);
  return hits[0];
}

// srcContaining returns the top-level statement that holds the first
// occurrence of marker: the whole enclosing function for a marker inside one,
// the whole `setInterval(...)` call for a marker inside a top-level callback.
export function srcContaining(marker) {
  const rel = fileWith(marker);
  const text = fileSource(rel);
  const at = text.indexOf(marker);
  const { body } = parseFile(rel);
  const node = body.find((n) => n.start <= at && at < n.end);
  if (!node) {
    throw new Error(`${JSON.stringify(marker)} sits between statements in ${rel} (line ${lineOf(text, at)}), not inside one`);
  }
  return text.slice(node.start, node.end);
}

// srcBetween returns the text from the first occurrence of start up to the
// next occurrence of end after it, inside the one file that holds start.
export function srcBetween(start, end) {
  const rel = fileWith(start);
  const text = fileSource(rel);
  const from = text.indexOf(start);
  const to = text.indexOf(end, from + start.length);
  if (to < 0) throw new Error(`${JSON.stringify(end)} does not follow ${JSON.stringify(start)} in ${rel}`);
  return text.slice(from, to);
}

// Parses every ```mermaid block in the given markdown files with mermaid
// itself, so a diagram that GitHub silently refuses to render fails the build
// instead of shipping as a grey error box in the docs.
//
// Written after eight of the fourteen diagrams in docs/ARCHITECTURE.md turned
// out to be broken at once (2026-09-08): mermaid 11 made `box` a keyword in
// sequence diagrams, so `participant Box as ...` stopped parsing, and a `;`
// in message or note text is a parse error too. Both had been in the file for
// months without anyone noticing, because a broken block just renders as an
// error box on github.com.
//
// Usage: node .github/scripts/check-mermaid.mjs <file.md> [file.md ...]

import fs from 'node:fs';
import { JSDOM } from 'jsdom';

const dom = new JSDOM('<!doctype html><html><body></body></html>');
global.window = dom.window;
global.document = dom.window.document;
global.Element = dom.window.Element;
global.HTMLElement = dom.window.HTMLElement;
global.Node = dom.window.Node;
global.DOMParser = dom.window.DOMParser;
global.SVGElement = dom.window.SVGElement;

const mermaid = (await import('mermaid')).default;

const files = process.argv.slice(2);
if (files.length === 0) {
  console.error('usage: check-mermaid.mjs <file.md> [file.md ...]');
  process.exit(2);
}

let blocks = 0;
let failed = 0;

for (const file of files) {
  const md = fs.readFileSync(file, 'utf8');
  const lines = md.split('\n');
  let start = -1;
  let buf = [];
  for (let i = 0; i < lines.length; i++) {
    if (start < 0) {
      if (lines[i].trim() === '```mermaid') {
        start = i + 1;
        buf = [];
      }
      continue;
    }
    if (lines[i].trim() === '```') {
      const source = buf.join('\n');
      const kind = (buf.find((l) => l.trim()) || '').trim();
      blocks++;
      try {
        await mermaid.parse(source);
      } catch (err) {
        failed++;
        const msg = String(err && err.message ? err.message : err)
          .split('\n')
          .slice(0, 3)
          .join(' ');
        console.log(`::error file=${file},line=${start}::mermaid block (${kind}) does not parse: ${msg}`);
      }
      start = -1;
      continue;
    }
    buf.push(lines[i]);
  }
  if (start >= 0) {
    failed++;
    console.log(`::error file=${file},line=${start}::unterminated mermaid block`);
  }
}

console.log(`${blocks} mermaid block(s) checked, ${failed} broken`);
process.exit(failed === 0 ? 0 : 1);

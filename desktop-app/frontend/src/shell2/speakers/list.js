// shell2/speakers/list.js: Speakers > list, the default place of the UI 2.0
// Speakers area (phase 3, #1287; docs/design/ui-2.0/speakers.png, middle
// column).
//
// SKELETON. What is here: one row per speaker that opens its page, a Bose
// original opening Add speaker instead. Enough for the router and the
// navigation spec; the real list is built on top of it.
//
// TODO(phase 3, list) contract for the part that fills this in:
//   receives  renderSpeakerList(root, ctx), called by the router (index.js)
//             on every entry to the list and on every sidebar repaint
//             (renderShellSpeakers), so it must be cheap and repaint only on
//             a change (keep a signature like mount.js paintSpeakers does).
//               root  #view-speakers, a view of its own that exists only in
//                     the preview (markup.js); nothing else writes into it.
//               ctx   { boxes, currentBox, label(box), open(route, opts),
//                       needsUpdate(box), cannotReachInternet(box),
//                       zoneLive, boxPlaying, offlineTitle, pairName }
//                     (index.js listCtx; add what the rows need there, from
//                     state or the mount deps, never from main.js directly).
//   renders   one row per speaker with ONE status pill per state (pill.js:
//             playing, update ready, standby, starting up, not reachable,
//             stereo pair, Bose original), the model and how it is connected
//             under the name; a stereo pair as one row, as in the sidebar.
//             Status in words, never colour alone. Footer as in the mockup:
//             "Stereo pairs and groups: Set up" (open('groups')) and "Update
//             every speaker: n waiting" (the existing update-all path,
//             updateAllBoxes in main.js, handed in through the mount deps).
//   reuses    rooms.js roomItems (the sidebar's rows and their flags) and
//             pill.js statusPillHTML / speakerPillKind, so the sidebar and
//             this list never name a state differently. Derive "update ready"
//             from ctx.needsUpdate and "starting up" from the record
//             (strSilent: the Bose side answers, the agent not yet; the
//             fixtures in testkit/demo-data.mjs SPEAKERS3 carry both).
//   keeps     a row opens open('speaker', { host }); a Bose original opens
//             open('add', { host }) (the setup on that speaker); an offline
//             speaker still opens its page, whose Overview explains it
//             (errors where they happen). Rows are buttons in a list, the
//             current speaker is marked (aria-current), 40px targets, and the
//             list works with 8+ speakers at Large/XL text in a 1000px window.
//
// Pure parts (rows, markup) are exported for vitest; the DOM work happens only
// inside renderSpeakerList, never at import (main.js imports this before the
// view containers exist).

import { t } from '../../i18n/index.js';
import { escapeAttr, escapeHtml } from '../../utils.js';
import { pillLabel } from '../pill.js';
import './list.css';

// speakerListRows turns the speaker records into the rows the list draws, in
// the app's own order (state.boxes, the order every speaker picker uses).
export function speakerListRows(boxes, ctx = {}) {
  const label = typeof ctx.label === 'function' ? ctx.label : (b) => b.friendlyName || b.name || b.host;
  const cur = ctx.currentBox;
  return (boxes || []).filter((b) => b && b.host).map((b) => ({
    host: b.host,
    port: b.port,
    name: label(b) || b.host,
    model: b.model || '',
    stock: b.kind === 'stock',
    offline: !!b.offline,
    current: !!cur && cur.host === b.host,
  }));
}

export function speakerListHTML(rows) {
  if (!rows.length) return `<p class="spl-empty">${escapeHtml(t('ui2.side.empty'))}</p>`;
  const items = rows.map((r) => {
    // A Bose original opens the setup, and its name says so (the sidebar's
    // own wording for the same row).
    const aria = r.stock
      ? ` aria-label="${escapeAttr(t('ui2.side.rowLabelSetUp', { name: r.name, state: pillLabel('boseOriginal') }))}"`
      : '';
    return `<li class="spl-item"><button type="button" class="spl-open u-row${r.current ? ' is-selected' : ''}"`
      + ` data-host="${escapeAttr(r.host)}" data-route="${r.stock ? 'add' : 'speaker'}"${r.current ? ' aria-current="true"' : ''}${aria}>`
      + `<span class="spl-name">${escapeHtml(r.name)}</span>`
      + (r.model ? `<span class="spl-meta">${escapeHtml(r.model)}</span>` : '')
      + '</button></li>';
  }).join('');
  return `<ul class="spl-list" aria-labelledby="ui2SpkAreaTitle">${items}</ul>`;
}

let lastSig = '';
let lastCtx = {};
const wired = new WeakSet();

// renderSpeakerList paints the list into root (#view-speakers). Delegated
// handler, wired once per root, so a repaint never loses it.
export function renderSpeakerList(root, ctx = {}) {
  if (!root) return;
  lastCtx = ctx;
  if (!wired.has(root)) {
    wired.add(root);
    root.addEventListener('click', (e) => {
      const btn = e.target.closest('.spl-open');
      if (!btn || typeof lastCtx.open !== 'function') return;
      lastCtx.open(btn.dataset.route, { host: btn.dataset.host });
    });
  }
  const rows = speakerListRows(ctx.boxes, ctx);
  const sig = JSON.stringify(rows);
  if (sig === lastSig && root.firstChild) return;
  lastSig = sig;
  const active = document.activeElement;
  const keepHost = active && root.contains(active) && active.dataset ? active.dataset.host : '';
  root.innerHTML = speakerListHTML(rows);
  if (keepHost) {
    const again = [...root.querySelectorAll('.spl-open')].find((b) => b.dataset.host === keepHost);
    if (again) again.focus();
  }
}

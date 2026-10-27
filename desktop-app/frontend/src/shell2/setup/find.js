// shell2/setup/find.js: step 1 of the UI 2.0 setup wizard, Find (phase 4,
// #1288; docs/UI-2.0.md "Navigation, current and new": Set up speaker becomes
// Speakers > Add speaker).
//
// Find chooses exactly ONE speaker to put STR on:
//   - every Bose original discovery sees (kind 'stock'), as one radio each; an
//     offline one is listed but cannot be chosen, it says why;
//   - "A speaker that is not on the network yet", which opens the existing
//     Wi-Fi onboarding of views/setup.js (the Bose app links with their QR
//     codes, the setup mode hint, the setup network push) below the list;
//   - every speaker that already runs STR, as such, with the way to its own
//     page. It is never offered for an install.
//
// The choice itself goes through the classic picker's own cards (the wizard
// clicks the hidden card), so state.setupTarget, the network install hero and
// the stick section react exactly as they do in the classic layout.
//
// findModel is pure (vitest); paintFind renders into the panel and keeps the
// focus when nothing changed (it runs on every discovery refresh).

import { t } from '../../i18n/index.js';
import { escapeHtml, escapeAttr, boxModelSupport } from '../../utils.js';

// FACTORY is the radio value of "not on the network yet".
export const FACTORY = '__factory';

const isStock = (b) => b && b.kind === 'stock';

// findModel sorts what discovery sees into the three groups of the step.
//   boxes     state.boxes
//   label     (box) => the name the app shows for it
//   selected  the host chosen in the wizard, or FACTORY, or ''
export function findModel(boxes, { label, selected = '' } = {}) {
  const name = (b) => (typeof label === 'function' ? label(b) : '') || b.friendlyName || b.name || b.host;
  const seen = new Set();
  const candidates = [];
  const onStr = [];
  for (const b of boxes || []) {
    if (!b || !b.host || seen.has(b.host)) continue;
    seen.add(b.host);
    const row = {
      host: b.host,
      name: name(b),
      model: b.model || b.type || '',
      offline: !!b.offline,
    };
    if (isStock(b)) {
      candidates.push({
        ...row,
        // Soundbars, home cinema systems and adapters install fine but have no
        // keys 1 to 6: the classic card says so, and so does this row.
        limited: boxModelSupport(b.model) === 'limited',
        // STR is on it but does not answer: installing again repairs it.
        strNotRunning: !!b.strNotRunning,
        selected: selected === b.host && !b.offline,
      });
    } else {
      onStr.push(row);
    }
  }
  const byName = (a, b) => a.name.localeCompare(b.name);
  candidates.sort(byName);
  onStr.sort(byName);
  return {
    candidates,
    onStr,
    factory: selected === FACTORY,
    // No Bose original to choose: the step explains the usual reason.
    empty: candidates.length === 0,
  };
}

// selectedCandidate is the chosen row of a model, or null.
export function selectedCandidate(model) {
  return (model && model.candidates.find((c) => c.selected)) || null;
}

function candidateHTML(c) {
  const pill = c.offline
    ? `<span class="u-pill u-pill--danger"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t('ui2.pill.offline'))}</span>`
    : c.strNotRunning
      ? `<span class="u-pill u-pill--warn"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t('speaker.strNotRunningBadge'))}</span>`
      : `<span class="u-pill u-pill--accent"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t('speaker.needsInstallBadge'))}</span>`;
  const meta = [c.model, c.host].filter(Boolean).join(' · ');
  const notes = [];
  if (c.offline) notes.push(t('ui2.setup.wizard.find.offlineNote', { name: c.name }));
  if (c.strNotRunning) notes.push(t('ui2.setup.wizard.find.repairNote'));
  if (c.limited) notes.push(t('setup.limitedNote'));
  return `<li class="w2-row${c.selected ? ' is-selected' : ''}${c.offline ? ' is-offline' : ''}" data-host="${escapeAttr(c.host)}">`
    + '<label class="w2-choice">'
    + `<input type="radio" name="w2Target" class="w2-radio" value="${escapeAttr(c.host)}"${c.selected ? ' checked' : ''}${c.offline ? ' disabled' : ''} />`
    + '<span class="w2-choice-text">'
    + `<span class="w2-name">${escapeHtml(c.name)}</span>`
    + (meta ? `<span class="w2-meta">${escapeHtml(meta)}</span>` : '')
    + '</span>'
    + pill
    + '</label>'
    + notes.map((n) => `<p class="w2-row-note">${escapeHtml(n)}</p>`).join('')
    + '</li>';
}

function factoryHTML(on) {
  return `<li class="w2-row w2-row--factory${on ? ' is-selected' : ''}">`
    + '<label class="w2-choice">'
    + `<input type="radio" name="w2Target" class="w2-radio" value="${FACTORY}"${on ? ' checked' : ''} />`
    + '<span class="w2-choice-text">'
    + `<span class="w2-name">${escapeHtml(t('ui2.setup.wizard.find.factory'))}</span>`
    + `<span class="w2-meta">${escapeHtml(t('ui2.setup.wizard.find.factoryMeta'))}</span>`
    + '</span>'
    + '</label>'
    + '</li>';
}

function strRowHTML(r) {
  const meta = [r.model, r.host].filter(Boolean).join(' · ');
  return `<li class="w2-row w2-row--str" data-host="${escapeAttr(r.host)}">`
    + '<span class="w2-choice-text">'
    + `<span class="w2-name">${escapeHtml(r.name)}</span>`
    + (meta ? `<span class="w2-meta">${escapeHtml(meta)}</span>` : '')
    + '</span>'
    + `<span class="u-pill u-pill--ok"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t('ui2.setup.wizard.find.onStr'))}</span>`
    + `<button type="button" class="u-btn u-btn--secondary w2-open" data-host="${escapeAttr(r.host)}"`
    + ` aria-label="${escapeAttr(t('ui2.setup.wizard.find.openLabel', { name: r.name }))}">`
    + `${escapeHtml(t('ui2.setup.wizard.find.open'))}</button>`
    + '</li>';
}

// findHTML is the whole list of the step.
export function findHTML(model) {
  const legend = t('ui2.setup.wizard.find.legend');
  let html = '';
  if (model.empty) {
    html += '<div class="w2-empty" role="status">'
      + `<p>${escapeHtml(t('ui2.setup.wizard.find.empty'))}</p>`
      + `<p class="w2-muted">${escapeHtml(t('setup.targetEmptyBoseApp'))}</p>`
      + '</div>';
  }
  html += '<fieldset class="w2-group">'
    + `<legend class="w2-legend">${escapeHtml(legend)}</legend>`
    + '<ul class="w2-list">'
    + model.candidates.map(candidateHTML).join('')
    + factoryHTML(model.factory)
    + '</ul></fieldset>';
  if (model.onStr.length) {
    html += '<details class="w2-onstr" id="w2OnStr">'
      + `<summary class="w2-onstr-sum">${escapeHtml(t('ui2.setup.wizard.find.onStrGroup', { count: model.onStr.length }))}</summary>`
      + `<p class="w2-muted">${escapeHtml(t('ui2.setup.wizard.find.onStrHint'))}</p>`
      + '<ul class="w2-list">'
      + model.onStr.map(strRowHTML).join('')
      + '</ul></details>';
  }
  return html;
}

// paintFind renders the list into host, but only when it changed: discovery
// repaints every few seconds, and a rebuild under the pointer would eat a
// click and drop the keyboard focus.
export function paintFind(host, model) {
  if (!host) return false;
  const html = findHTML(model);
  if (host.dataset.sig === html) return false;
  const details = host.querySelector('#w2OnStr');
  const open = !!(details && details.open);
  const focused = host.contains(document.activeElement) ? document.activeElement : null;
  const focusKey = focused ? (focused.value || (focused.dataset && focused.dataset.host) || '') : '';
  const focusCls = focused ? (focused.classList.contains('w2-open') ? 'w2-open' : 'w2-radio') : '';
  host.innerHTML = html;
  host.dataset.sig = html;
  const d2 = host.querySelector('#w2OnStr');
  if (d2 && open) d2.open = true;
  if (focusKey) {
    const again = [...host.querySelectorAll(`.${focusCls}`)]
      .find((el) => (el.value || el.dataset.host) === focusKey);
    if (again) again.focus();
  }
  return true;
}

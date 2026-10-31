// shell2/markup.js: the empty frame of the UI 2.0 preview.
//
// Only the frame is new: the sidebar, the main pane and the persistent player
// are containers, and mount.js MOVES the existing nodes into them (the views,
// the status bar, the transport, the volume). Nothing in here duplicates a
// control the views already own, so both layouts share one DOM and one state.
//
// Pure: a string, so vitest can read it without a DOM.

import { t } from '../i18n/index.js';
import { escapeAttr, escapeHtml } from '../utils.js';
import { NAV } from './nav.js';
import { SUBNAV } from './speakers/routes.js';

function navItemHTML(item) {
  const badge = item.badgeId
    ? `<span class="u-badge" id="${item.badgeId}" hidden></span>`
    : '';
  // An entry for an area (Speakers) names every view it stands for, so the
  // marking (syncShellNav) and the new-feature dots (main.js renderNavDots)
  // find it from any of them.
  const views = item.area ? ` data-views="${item.area.join(' ')}"` : '';
  return `<button type="button" class="ui2-nav-item${item.small ? ' ui2-nav-item--small' : ''}" data-view="${item.view}"${views}${item.id ? ` id="${item.id}"` : ''}>`
    + `<span class="ui2-nav-text">${escapeHtml(t(item.labelKey))}</span>${badge}</button>`;
}

function popHead(titleId, titleKey) {
  return '<div class="u-pop-head">'
    + `<span class="u-pop-title" id="${titleId}">${escapeHtml(t(titleKey))}</span>`
    + `<button type="button" class="u-btn u-btn--icon u-pop-close" aria-label="${escapeAttr(t('ui2.popover.close'))}" title="${escapeAttr(t('ui2.popover.close'))}">&times;</button>`
    + '</div>';
}

// speakersAreaHTML is the head of the Speakers area (speakers/index.js): the
// title, the row of its places and the slot of one speaker's page. It sits
// above the views in the main pane and shows only on the area's routes.
function speakersAreaHTML() {
  const subs = SUBNAV.map((s) => {
    const badge = s.badgeId ? `<span class="u-badge" id="${s.badgeId}" hidden></span>` : '';
    return `<button type="button" class="spa-sub" data-route="${s.route}">`
      + `<span class="spa-sub-text">${escapeHtml(t(s.labelKey))}</span>${badge}</button>`;
  }).join('');
  return '<div class="spa" id="ui2SpkArea" hidden>'
    + '<div class="spa-head">'
    + `<h2 class="spa-title" id="ui2SpkAreaTitle">${escapeHtml(t('ui2.speakers.title'))}</h2>`
    + `<nav class="spa-subnav" id="ui2SpkSubnav" aria-labelledby="ui2SpkAreaTitle">${subs}</nav>`
    + '</div>'
    + '<div class="sps" id="ui2SpkPage" hidden></div>'
    + '</div>';
}

export function shellMarkup() {
  const groups = NAV.groups.map((g) => {
    const hid = `ui2NavHead-${g.id}`;
    return `<div class="ui2-nav-group" role="group" aria-labelledby="${hid}">`
      + `<div class="ui2-nav-heading" id="${hid}">${escapeHtml(t(g.labelKey))}</div>`
      + g.items.map(navItemHTML).join('')
      + '</div>';
  }).join('');
  return `
<button type="button" class="u-skip" id="ui2Skip">${escapeHtml(t('ui2.skip'))}</button>
<div class="ui2-shell" id="ui2Shell">
  <div class="ui2-banners" id="ui2Banners"></div>
  <div class="ui2-sidebar" id="ui2Sidebar">
    <div class="ui2-side-top">
      <div class="ui2-brand"></div>
    </div>
    <section class="ui2-spk" id="ui2Speakers" aria-labelledby="ui2SpkHead">
      <h2 class="u-sr-only" id="ui2SpkHead">${escapeHtml(t('ui2.side.speakers'))}</h2>
      <div class="ui2-spk-lists" id="ui2SpkLists"></div>
    </section>
    <nav class="ui2-side-nav" aria-label="${escapeAttr(t('ui2.nav.label'))}">
      <div class="ui2-side-scroll">${groups}</div>
      <div class="ui2-side-small">${NAV.bottom.map(navItemHTML).join('')}</div>
    </nav>
    <div class="ui2-side-bottom">
      <div class="ui2-app-settings" id="ui2AppSettings" role="group" aria-label="${escapeAttr(t('ui2.nav.appSettings'))}"></div>
      <div class="ui2-side-version" id="ui2Version"></div>
    </div>
  </div>
  <main class="ui2-main" id="ui2Main" tabindex="-1">
    <div class="ui2-main-inner" id="ui2MainInner">${speakersAreaHTML()}<div id="view-speakers" class="view hidden"></div></div>
  </main>
  <section class="ui2-player" id="ui2Player" aria-label="${escapeAttr(t('ui2.player.label'))}">
    <div class="ui2-player-now"></div>
    <div class="ui2-player-transport"></div>
    <div class="ui2-player-more u-pop-anchor">
      <button type="button" class="u-btn u-btn--secondary" id="ui2MoreBtn" aria-haspopup="dialog" aria-expanded="false" aria-controls="ui2MorePop">${escapeHtml(t('ui2.player.more'))}</button>
      <div class="u-popover ui2-player-pop" id="ui2MorePop" role="dialog" aria-labelledby="ui2MoreTitle" hidden>
        ${popHead('ui2MoreTitle', 'ui2.player.more')}
        <div class="ui2-more-body" id="ui2MoreBody"></div>
      </div>
    </div>
    <div class="ui2-player-sound">
    <div class="ui2-player-volume"></div>
    <div class="ui2-player-groupvol" id="ui2GroupVolSlot"></div>
    <div class="ui2-player-levels u-pop-anchor">
      <button type="button" class="u-btn u-btn--secondary ui2-levels-btn" id="ui2LevelsBtn" aria-haspopup="dialog" aria-expanded="false" aria-controls="ui2LevelsPop">
        <span class="ui2-output-kicker">${escapeHtml(t('ui2.player.levels'))}</span>
        <span class="ui2-output-text" id="ui2LevelsText"></span>
      </button>
      <div class="u-popover ui2-player-pop" id="ui2LevelsPop" role="dialog" aria-labelledby="ui2LevelsTitle" hidden>
        ${popHead('ui2LevelsTitle', 'ui2.player.levelsTitle')}
        <div class="ui2-levels-body" id="ui2LevelsBody"></div>
      </div>
    </div>
    </div>
    <div class="ui2-player-empty" hidden><p class="ui2-player-state-text">${escapeHtml(t('ui2.player.empty'))}</p></div>
    <p class="ui2-player-status" id="ui2PlayerStatus" role="alert"></p>
  </section>
</div>`;
}

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

function navItemHTML(item) {
  const badge = item.badgeId
    ? `<span class="u-badge" id="${item.badgeId}" hidden></span>`
    : '';
  return `<button type="button" class="ui2-nav-item${item.small ? ' ui2-nav-item--small' : ''}" data-view="${item.view}"${item.id ? ` id="${item.id}"` : ''}>`
    + `<span class="ui2-nav-text">${escapeHtml(t(item.labelKey))}</span>${badge}</button>`;
}

function popHead(titleId, titleKey) {
  return '<div class="u-pop-head">'
    + `<span class="u-pop-title" id="${titleId}">${escapeHtml(t(titleKey))}</span>`
    + `<button type="button" class="u-btn u-btn--icon u-pop-close" aria-label="${escapeAttr(t('ui2.popover.close'))}" title="${escapeAttr(t('ui2.popover.close'))}">&times;</button>`
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
      <div class="ui2-rooms u-pop-anchor" id="ui2Rooms" role="group" aria-labelledby="ui2RoomsLabel">
        <div class="ui2-rooms-label" id="ui2RoomsLabel">${escapeHtml(t('ui2.rooms.label'))}</div>
        <button type="button" class="ui2-room-btn" id="ui2RoomBtn" aria-haspopup="dialog" aria-expanded="false" aria-controls="ui2RoomPop" aria-label="${escapeAttr(t('ui2.rooms.none'))}">
          <span class="ui2-room-btn-body" id="ui2RoomBtnBody"></span>
          <span class="ui2-caret" aria-hidden="true">&#9662;</span>
        </button>
        <div class="u-popover ui2-room-pop" id="ui2RoomPop" role="dialog" aria-labelledby="ui2RoomPopTitle" hidden>
          ${popHead('ui2RoomPopTitle', 'ui2.rooms.listLabel')}
          <div class="ui2-room-list" id="ui2RoomList"></div>
          <button type="button" class="u-btn u-btn--secondary ui2-room-add" id="ui2RoomAdd">${escapeHtml(t('nav.setupStick'))}</button>
        </div>
      </div>
    </div>
    <nav class="ui2-side-nav" aria-label="${escapeAttr(t('ui2.nav.label'))}">
      <div class="ui2-side-scroll">${groups}</div>
      <div class="ui2-side-small">${NAV.bottom.map(navItemHTML).join('')}</div>
    </nav>
    <div class="ui2-side-bottom">
      <div class="ui2-app-settings" id="ui2AppSettings" role="group" aria-label="${escapeAttr(t('ui2.nav.appSettings'))}"></div>
    </div>
  </div>
  <main class="ui2-main" id="ui2Main" tabindex="-1">
    <div class="ui2-main-inner" id="ui2MainInner"></div>
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
    <div class="ui2-player-output u-pop-anchor">
      <button type="button" class="u-btn u-btn--secondary ui2-output-btn" id="ui2OutputBtn" aria-haspopup="dialog" aria-expanded="false" aria-controls="ui2OutputPop">
        <span class="ui2-output-kicker">${escapeHtml(t('ui2.player.output'))}</span>
        <span class="ui2-output-text" id="ui2OutputText"></span>
      </button>
      <div class="u-popover ui2-player-pop" id="ui2OutputPop" role="dialog" aria-labelledby="ui2OutputTitle" hidden>
        ${popHead('ui2OutputTitle', 'ui2.player.outputTitle')}
        <p class="ui2-output-summary" id="ui2OutputSummary"></p>
        <div class="ui2-output-body" id="ui2OutputBody"></div>
      </div>
    </div>
    <div class="ui2-player-volume"></div>
    <p class="ui2-player-empty" hidden>${escapeHtml(t('ui2.player.empty'))}</p>
  </section>
</div>`;
}

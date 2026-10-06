// The popup after a new highscore on one of the hidden display games: the
// final screen, the score, and the same buttons as the Arcade section in the
// speaker settings (copy the screenshot, copy the score, open the thread).
// Built on demand and removed again on close; Escape and a click on the
// backdrop close it too.
import { t } from './i18n/index.js';
import { escapeHtml, escapeAttr, showToast, showError } from './utils.js';
import { ClipboardSetText, OpenArcadeThread, SaveArcadeScreenshot } from './api.js';
import { copyScreenshot, scoreText } from './arcadeshare.js';

const ID = 'arcadeHighscoreModal';

export function closeHighscorePopup() {
  const el = document.getElementById(ID);
  if (el) el.remove();
}

// openHighscorePopup shows game (one entry of arcadeState) for box.
export function openHighscorePopup(game, box) {
  closeHighscorePopup();
  const name = box.friendlyName || box.name || box.host;
  const wrap = document.createElement('div');
  wrap.className = 'modal';
  wrap.id = ID;
  wrap.innerHTML = `<div class="modal-content" role="dialog" aria-modal="true" aria-labelledby="${ID}Title" style="max-width:440px">
      <h3 id="${ID}Title">${escapeHtml(t('settingsView.arcadePopupTitle'))}</h3>
      <p>${escapeHtml(t('settingsView.arcadePopupText', { game: game.title, name, best: game.best }))}</p>
      ${game.screenshot ? `<img src="${escapeAttr(game.screenshot)}" alt="${escapeAttr(t('settingsView.blockfallScreenAlt'))}" style="display:block;width:100%;image-rendering:pixelated;margin:8px 0">` : ''}
      <div class="setting-row" style="flex-wrap:wrap;gap:6px">
        ${game.screenshot ? `<button class="btn btn-mini" data-act="shot">${escapeHtml(t('settingsView.blockfallCopyShot'))}</button>` : ''}
        <button class="btn btn-mini" data-act="score">${escapeHtml(t('settingsView.blockfallCopyScore'))}</button>
        <button class="btn btn-mini btn-primary" data-act="thread">${escapeHtml(t('settingsView.blockfallOpenThread'))}</button>
      </div>
      <div class="share-foot">
        <button class="btn" data-act="close" type="button">${escapeHtml(t('common.close'))}</button>
      </div>
    </div>`;
  wrap.onclick = async (ev) => {
    if (ev.target === wrap) { closeHighscorePopup(); return; }
    const act = ev.target.closest('[data-act]')?.dataset.act;
    switch (act) {
      case 'close':
        closeHighscorePopup();
        break;
      case 'shot':
        // the clipboard write starts first thing in the click
        if (await copyScreenshot(game.screenshot)) { showToast(t('settingsView.blockfallShotCopiedToast')); break; }
        try {
          const path = await SaveArcadeScreenshot(box.host, box.port, game.id);
          if (path) showToast(t('settingsView.blockfallShotSavedToast', { path }));
        } catch (e) { showError(e); }
        break;
      case 'score':
        try {
          await ClipboardSetText(scoreText(game, box.model || ''));
          showToast(t('settingsView.blockfallScoreCopiedToast'));
        } catch (e) { showError(e); }
        break;
      case 'thread':
        OpenArcadeThread();
        break;
    }
  };
  wrap.onkeydown = (e) => { if (e.key === 'Escape') { e.preventDefault(); closeHighscorePopup(); } };
  document.body.appendChild(wrap);
  wrap.querySelector('[data-act="thread"]')?.focus();
}

// The firmware update guide: ONE set of steps, rendered on every screen that
// says a speaker's Bose firmware is too old for STR (the stick wait, the
// install result screens, and the firmware row in the speaker settings).
//
// Why it exists. An ST20 owner on firmware 5.2.0 (2026-10-10) was told to use
// "Bose's USB update tool" from btu.bose.com, to write the firmware to another
// stick, and to look in a settings section a stock speaker can never open.
// None of that is Bose's procedure. Bose's own support articles ("Updating the
// software or firmware of your product", one per model) say: download the update
// file and do not rename it, put the speaker into setup mode, join its own Wi-Fi
// network, open http://192.168.1.1:17008/update.html (some articles print
// http://192.0.2.1:17008/update.html), Choose File, Upload, wait for the
// restart. A USB cable to the computer with http://203.0.113.1:17008/update.html
// is Bose's second route. That is what this guide says, with the setup-mode
// buttons only where a Bose article for that model names them.
//
// The download button opens the exact file on Bose's own host
// (downloads.bose.com). STR never mirrors or redistributes a firmware image:
// GetFirmwareGuide in the Go backend reads Bose's catalogue and returns links.
//
// Rendering decisions are pure functions (fwGuidePlan, firmwareGuideHtml) so
// they can be tested without a DOM; wireFirmwareGuides does the clicking and the
// one backend call, with the Wails functions passed in.
import { t } from './i18n/index.js';
import { escapeHtml } from './utils.js';
import { LATEST_BOSE_FIRMWARE, boseFwArticles, firmwareOlderThanLatest } from './firmware.js';

// The speaker's update page on its own setup network. Bose's articles disagree
// on the address: some print 192.168.1.1 (SoundTouch 20, Portable, 30 Series
// II, Wave), others 192.0.2.1 (SoundTouch 10, 30 Series III, SA-5, 300,
// Wireless Link). 192.168.1.1 is the one STR has seen on every setup network so
// far (SetupAPHost in setup_ap_probe.go), so it comes first and the other is
// offered as the second try.
export const UPDATE_PAGE = 'http://192.168.1.1:17008/update.html';
export const UPDATE_PAGE_ALT = 'http://192.0.2.1:17008/update.html';
// Bose's USB cable route: the address only answers once the speaker is
// connected to the computer by USB.
export const UPDATE_PAGE_USB = 'http://203.0.113.1:17008/update.html';
// Last resort for a model that has neither a matched file nor an article.
export const BOSE_SUPPORT_URL = 'https://support.bose.com/';

// setupModeKey picks the setup-mode instruction for a model. Each specific one
// is quoted from Bose's update article for that model; everything else gets the
// generic sentence rather than invented buttons.
//   2 + Volume minus: SoundTouch 10, 20 (Series II and III), 30 (Series II and
//     III), Portable
//   SA-5: Control button about 3 seconds
//   SoundTouch 300: SoundTouch on the remote, then hold 9
//   Wave SoundTouch (and Series IV): Control on the back of the pedestal, 3 s
//   Wireless Link adapter: the button on the back, about 10 seconds
export function setupModeKey(model) {
  const m = String(model || '').trim().toLowerCase();
  if (['soundtouch 10', 'soundtouch 20', 'soundtouch 30', 'soundtouch portable'].includes(m)) {
    return 'fwGuide.setupTwoVol';
  }
  if (m === 'soundtouch sa-5') return 'fwGuide.setupSa5';
  if (m === 'soundtouch 300') return 'fwGuide.setupSt300';
  if (m === 'wave soundtouch' || m.startsWith('wave soundtouch ')) return 'fwGuide.setupWave';
  if (m.includes('wireless link')) return 'fwGuide.setupLink';
  return 'fwGuide.setupGeneric';
}

// firmwareGuideNeeded is the one gate: a firmware we can read and that is older
// than Bose's last one. An unreadable version gets no guide, because telling an
// owner to reflash a speaker on the strength of a missing field is worse than
// saying nothing.
export function firmwareGuideNeeded(current) {
  return !!current && firmwareOlderThanLatest(current);
}

// fwGuidePlan is every decision the guide makes, as data.
//   ident: { model, moduleType, variant, current, context }
//   guide: the backend's FirmwareGuide, or null while it is being looked up
export function fwGuidePlan(ident, guide) {
  const id = ident || {};
  const files = guide && Array.isArray(guide.files) ? guide.files.filter(f => f && f.url) : null;
  let download = 'loading';
  if (files) download = files.length === 0 ? 'none' : (files.length === 1 ? 'one' : 'two');
  return {
    current: shortVersion(id.current) || '?',
    required: (guide && guide.required) || LATEST_BOSE_FIRMWARE,
    download,
    files: files || [],
    setupKey: setupModeKey(id.model),
    articles: boseFwArticles(String(id.model || '').trim()),
    afterKey: id.context === 'settings' ? 'fwGuide.afterInstalled' : 'fwGuide.after',
  };
}

function shortVersion(v) {
  const m = String(v || '').match(/^(\d+\.\d+\.\d+)/);
  return m ? m[1] : String(v || '').trim();
}

function btn(label, url, extraClass) {
  return `<a href="#" class="btn btn-mini ${extraClass || ''}" data-fwopen="${escapeHtml(url)}">${escapeHtml(label)}</a>`;
}

function addressRow(url) {
  return `<div class="fw-guide-addr"><code>${escapeHtml(url)}</code> `
    + `<button type="button" class="btn btn-mini" data-fwcopy="${escapeHtml(url)}">${escapeHtml(t('common.copy'))}</button> `
    + `<a href="#" class="btn btn-mini" data-fwopen="${escapeHtml(url)}">${escapeHtml(t('fwGuide.open'))}</a></div>`;
}

function downloadHtml(plan) {
  const p = (s) => `<div class="fw-guide-dl">${s}</div>`;
  switch (plan.download) {
    case 'loading':
      return p(`<span class="muted small">${escapeHtml(t('fwGuide.loading'))}</span>`);
    case 'one':
      return p(btn(t('fwGuide.downloadBtn'), plan.files[0].url, 'btn-primary fw-guide-download'));
    case 'two':
      return p(btn(t('fwGuide.downloadOlder'), plan.files[0].url, 'btn-primary fw-guide-download') + ' '
        + btn(t('fwGuide.downloadNewer'), plan.files[1].url, 'fw-guide-download'))
        + `<div class="muted small">${escapeHtml(t('fwGuide.twoFiles'))}</div>`;
    default:
      if (plan.articles.length) {
        return `<div class="muted small">${escapeHtml(t('fwGuide.noFile'))}</div>`;
      }
      return `<div class="muted small">${escapeHtml(t('fwGuide.noFileNoArticle'))}</div>`
        + p(btn(t('fwGuide.supportBtn'), BOSE_SUPPORT_URL, ''));
  }
}

// firmwareGuideHtml renders the guide body for a plan.
export function firmwareGuideHtml(ident, guide) {
  const plan = fwGuidePlan(ident, guide);
  const articles = plan.articles.map(([series, url]) =>
    btn(series ? `${t('fw.boseGuideLink')} (${series})` : t('fw.boseGuideLink'), url, 'fw-guide-link')).join(' ');
  return `<b>${escapeHtml(t('fwGuide.title'))}</b>`
    + `<div>${escapeHtml(t('fwGuide.versions', { current: plan.current, required: plan.required }))}</div>`
    + `<div class="muted small">${escapeHtml(t('fwGuide.why'))}</div>`
    + '<ol class="fw-guide-steps">'
    + `<li>${escapeHtml(t('fwGuide.step1'))}${downloadHtml(plan)}</li>`
    + `<li>${escapeHtml(t(plan.setupKey))}</li>`
    + `<li>${escapeHtml(t('fwGuide.step3'))}</li>`
    + `<li>${escapeHtml(t('fwGuide.step4'))}${addressRow(UPDATE_PAGE)}`
    + `<div class="muted small">${escapeHtml(t('fwGuide.step4Alt', { url: UPDATE_PAGE_ALT }))}</div></li>`
    + `<li>${escapeHtml(t('fwGuide.step5'))}</li>`
    + '</ol>'
    + `<div>${escapeHtml(t(plan.afterKey))}</div>`
    + `<div class="muted small fw-guide-usb">${escapeHtml(t('fwGuide.usb', { url: UPDATE_PAGE_USB }))}</div>`
    + (articles ? `<p>${articles}</p>` : '');
}

// Answers from the backend, per speaker identity, for this session. The stick
// wait re-renders its status every few seconds; without this the guide would
// flash back to "looking up" each time.
const guideCache = new Map();

function identKey(id) {
  return [id.model, id.moduleType, id.variant, id.current].map(v => String(v || '')).join('|');
}

// firmwareGuideMount is the placeholder a view puts into its HTML. It renders
// the guide at once (download step "looking up") and wireFirmwareGuides fills in
// Bose's link.
export function firmwareGuideMount(ident) {
  const id = {
    model: (ident && ident.model) || '',
    moduleType: (ident && ident.moduleType) || '',
    variant: (ident && ident.variant) || '',
    current: (ident && ident.current) || '',
    context: (ident && ident.context) || '',
  };
  const cached = guideCache.get(identKey(id)) || null;
  return `<div class="fw-guide" data-fwguide="${escapeHtml(JSON.stringify(id))}">${firmwareGuideHtml(id, cached)}</div>`;
}

// wireFirmwareGuides wires every guide under root.
//   deps.getGuide(model, moduleType, variant, current) -> Promise<FirmwareGuide>
//   deps.openURL(url), deps.copy(text) -> Promise
export function wireFirmwareGuides(root, deps) {
  if (!root || !root.querySelectorAll) return;
  root.querySelectorAll('.fw-guide[data-fwguide]').forEach((el) => {
    if (el.dataset.fwWired) return;
    el.dataset.fwWired = '1';
    el.addEventListener('click', (e) => {
      const open = e.target.closest('[data-fwopen]');
      if (open) {
        e.preventDefault();
        try { deps.openURL(open.dataset.fwopen); } catch {}
        return;
      }
      const copy = e.target.closest('[data-fwcopy]');
      if (copy) {
        e.preventDefault();
        Promise.resolve()
          .then(() => deps.copy(copy.dataset.fwcopy))
          .then(() => { copy.textContent = t('common.copied'); })
          .catch(() => {});
      }
    });
    let id;
    try { id = JSON.parse(el.dataset.fwguide); } catch { return; }
    const key = identKey(id);
    if (guideCache.has(key)) return;
    Promise.resolve()
      .then(() => deps.getGuide(id.model, id.moduleType, id.variant, id.current))
      .then((g) => {
        guideCache.set(key, g || { files: [] });
        if (el.isConnected !== false) el.innerHTML = firmwareGuideHtml(id, guideCache.get(key));
      })
      .catch(() => {
        // Backend unreachable: no download link can be named, so the guide
        // falls back to Bose's article or support site.
        el.innerHTML = firmwareGuideHtml(id, { files: [] });
      });
  });
}

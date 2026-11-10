// appupdate.js: the update of the APP itself: the check (checkAppUpdate), the
// notice in #appUpdateBanner with its quiet way back, and the install paths
// (in-app download and apply, winget, the macOS and .deb hand-offs). Extracted
// from the main.js monolith. The 8 s and twelve-hour timers that call
// checkAppUpdate stay in main.js.

import {
  ApplyUpdate,
  BrowserOpenURL,
  CheckAppUpdate,
  ClipboardSetText,
  DownloadUpdate,
  EventsOn,
  NewerCopyNextToThisOne,
  RevealUpdateFile,
  UpdateViaWinget,
} from './api.js';
import { maybeShowSpeakerUpdateCard } from './banners/speakerupdate.js';
import { fmtRate } from './format.js';
import { t } from './i18n/index.js';
import { state } from './state.js';
import { noteCheckFailed, noteCheckSucceeded, shouldSayChecksAreFailing } from './updatecheckhealth.js';
import {
  $,
  clearNoticeDismissal,
  dismissNotice,
  escapeAttr,
  escapeHtml,
  noticeDismissed,
  showError,
  showToast,
} from './utils.js';
import { wingetInstallLabel, wingetOutcomeView } from './wingetupdate.js';

// Injected main.js helpers (see initAppUpdate); called as deps.<name>. The
// default is a no-op so the module stays importable before main.js has run.
let deps = {
  // A newer app mutes the speaker update prompts, so the per-speaker update
  // check is re-run once the answer is in.
  checkBoxUpdate() {},
};
export function initAppUpdate(d) {
  deps = { ...deps, ...d };
}

// renderAppUpdateCheckLink leaves a quiet way back to the update notice in the
// place the banner would occupy.
//
// Dismissing the notice used to be one-way. The version check ran 8 s after
// start and then every twelve hours, and a dismissal was remembered for that
// version, so somebody who clicked it away, or who went back to an older
// build, had nothing left to press and no way to be offered the update again
// (discussion #597: "How do I force it to check for updates?"). A manual
// check also ignores the stored dismissal, because a person pressing this has
// just answered the question the dismissal was standing in for.
export function renderAppUpdateCheckLink(banner, note) {
  if (!banner) return;
  banner.classList.add('app-update-quiet');
  banner.classList.remove('hidden');
  // Name the version next to the link. On its own the link was a stray line
  // above everything else with nothing to explain why it was there; with the
  // installed version in front of it, the line says what it is about.
  const cur = (state.appInfo && state.appInfo.version) ? String(state.appInfo.version) : '';
  banner.innerHTML = (cur ? `<span class="app-update-quiet-ver">${escapeHtml(t('setup.versionLabel', { version: cur }))}</span>` : '')
    + `<a href="#" id="appUpdateCheckLink" class="footer-link">${escapeHtml(t('banner.checkForAppUpdate'))}</a>`
    + (note ? `<span class="app-update-quiet-note">${escapeHtml(note)}</span>` : '');
  const link = $('appUpdateCheckLink');
  if (link) link.onclick = (e) => {
    e.preventDefault();
    link.textContent = t('banner.checkingForAppUpdate');
    try { clearNoticeDismissal('appUpdate'); } catch {}
    checkAppUpdate(true);
  };
}

// Resolves to what the check found ({ outcome: 'current' | 'available' |
// 'failed', version }), so a button that asked can say it next to itself
// (Help and info in the UI 2.0 preview); the classic callers ignore it.
export async function checkAppUpdate(manual) {
  // Entirely best-effort: an unreachable endpoint or a garbage payload
  // must never break the UI. Validate every field defensively and stay
  // silent on any failure.
  const banner = $('appUpdateBanner');
  try {
    const m = await CheckAppUpdate();
    // The question was reachable, whatever the answer. That is what the run
    // counts, not whether an update exists.
    noteCheckSucceeded();
    if (!m || typeof m !== 'object' || typeof m.version !== 'string' || !m.version) {
      // Nothing newer. Say so when the user asked; otherwise just leave the
      // way back on screen.
      renderAppUpdateCheckLink(banner, manual ? t('banner.appIsCurrent') : '');
      return { outcome: 'current' };
    }
    if (!banner) return { outcome: 'available', version: m.version };
    // An app update outranks the speaker updates and hides their prompts (see
    // speakerUpdateCardMuted). Set before the dismissal check: the ORDER
    // holds either way, and a user who clicked the app notice away has not
    // stopped needing the app first.
    state.appUpdateVersion = m.version;
    // This check is async and often resolves after discovery already showed
    // the speaker card or the Speaker Settings banner. Re-evaluate both now so
    // they follow the app-first rule instead of sitting under the app notice
    // as a second update prompt (#1154).
    maybeShowSpeakerUpdateCard();
    deps.checkBoxUpdate();
    if (noticeDismissed('appUpdate', m.version)) {
      renderAppUpdateCheckLink(banner, '');
      return { outcome: 'available', version: m.version };
    }
    banner.classList.remove('app-update-quiet');
    // Keep the banner discreet: version, a single "What's new" LINK to
    // the release notes (not the notes inline, which took too much space
    // and does not interest every user), and the download button.
    // Only treat a real http(s) URL as a download link; anything else is
    // ignored so we never hand junk to the system browser.
    const dlUrl = (typeof m.downloadUrl === 'string' && /^https?:\/\//i.test(m.downloadUrl)) ? m.downloadUrl : '';
    // Link target for the notes: an explicit notesUrl from the server if
    // present, else the matching GitHub release page for the new version,
    // else the releases list.
    const repo = ((state.appInfo && state.appInfo.githubUrl) || 'https://github.com/JRpersonal/streborn').replace(/\/+$/, '');
    // /releases/latest always resolves to the newest PUBLISHED release, so it
    // never 404s on a draft/just-published tag (the "page not found" a user hit).
    const latestUrl = `${repo}/releases/latest`;
    const notesUrl = (typeof m.notesUrl === 'string' && /^https?:\/\//i.test(m.notesUrl))
      ? m.notesUrl
      : latestUrl;
    // The banner itself only shows for a genuinely newer version (CheckAppUpdate
    // returns nothing otherwise), and the download button only when the manifest
    // carries a real download URL. The button is a primary button so it stands
    // out in the notice instead of reading as a faint secondary control.
    // In-app update (#71): download the matching asset, verify its SHA256, then
    // install. Linux/Windows self-replace and relaunch; macOS downloads+verifies
    // and opens the .dmg (replacing a running .app bundle is unwritten work, not
    // a Gatekeeper problem: the build is notarized). The button
    // always shows now: the asset URL + hash are resolved from the release
    // manifest in the backend, so it no longer depends on the manifest carrying a
    // downloadUrl. notesUrl / releases page stays as the manual fallback.
    const isMacOS = /Mac|iPhone|iPad|iPod/i.test(navigator.platform || navigator.userAgent || '');
    // Label makes the target unambiguous: this updates THE APP itself, not the
    // speaker (a non-technical user clicked the speaker-update expecting the app
    // to update, and ended up with two .exe copies downloaded by hand).
    // A copy winget installed updates through winget (see wingetupdate.js), and
    // the button says so before it is pressed.
    const viaWinget = !!(state.appInfo && state.appInfo.installedViaWinget);
    const installLabel = viaWinget
      ? wingetInstallLabel(t)
      : (isMacOS ? t('banner.downloadAppUpdate') : t('banner.installAppNow'));
    banner.innerHTML = `
      <div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#8593;</span><span><b>${escapeHtml(t('banner.appUpdateAvail'))}</b> ${escapeHtml(m.version)} &middot; <a href="#" id="appUpdateNotes" class="footer-link">${escapeHtml(t('banner.whatsNew'))}</a></span></div>
      <button class="btn btn-primary app-update-btn" id="appUpdateBtn">${escapeHtml(installLabel)}</button>
      <button class="banner-close" id="appUpdateDismiss" aria-label="${escapeAttr(t('banner.dismiss'))}" title="${escapeAttr(t('banner.dismissTitle'))}">&times;</button>
    `;
    banner.classList.remove('hidden');
    // The winget upgrade the previous run started came back with an error:
    // say so above the button, with the command to run by hand.
    if (viaWinget && state.wingetFailure) {
      banner.insertAdjacentHTML('afterbegin', wingetNoteHTML(state.wingetFailure));
      wireWingetCopy(banner, state.wingetFailure.command);
    }
    // There is no Windows installer: the release is a bare exe (versioned
    // STR-Windows-vX.Y.Z.exe up to v1.0.1, STR-Windows.exe since), so updating
    // by hand means downloading a second file into the same folder and
    // nothing replaces anything. The Go side reads a sibling's version from
    // its Windows version resource, the old name being only the fallback. A reporter spent
    // three rounds of mail on that (2026-10-01), downloading the new version
    // again and again while starting the old one, until he deleted the old
    // file by hand. If the newer file is already sitting next to this one,
    // say so, because downloading it a fourth time will not help.
    //
    // The hint is added, and the Install button STAYS. It used to be hidden,
    // which made a file name the only thing standing between somebody and
    // their update: for files published up to v1.0.1 the name was the only
    // evidence about a sibling, a person may rename these files for their own
    // reasons (one does, to see the version on their desktop), and a file
    // called STR-Windows-v9.9.9.exe that is actually ancient would have left
    // the banner pointing at it with no way forward. The update check itself reads the version compiled into
    // the running build, so it is never fooled; only this hint can be, so this
    // hint may inform and must not block.
    NewerCopyNextToThisOne().then((file) => {
      if (!file) return;
      const line = document.createElement('div');
      line.className = 'app-update-text';
      line.textContent = t('banner.appUpdateAlreadyHere', { file });
      banner.insertBefore(line, banner.firstChild);
    }).catch(() => {});
    const notesLink = $('appUpdateNotes');
    if (notesLink) notesLink.onclick = (e) => { e.preventDefault(); BrowserOpenURL(notesUrl); };
    const dl = $('appUpdateBtn');
    if (dl) dl.onclick = viaWinget
      ? () => runWingetUpdate(m.version, dl, installLabel)
      : () => runAppUpdate(m.version, dl, installLabel, isMacOS, dlUrl || latestUrl);
    // Clicking it away is allowed and remembered, for THIS version only. The
    // next release is news again and says so; the user dismisses that one in
    // turn or installs it.
    const x = $('appUpdateDismiss');
    if (x) x.onclick = () => {
      dismissNotice('appUpdate', m.version);
      // Not hidden: the quiet link stays so the notice can be brought back.
      renderAppUpdateCheckLink(banner, '');
      // The speaker prompts stay muted: the app is still the thing to do first.
      // They return once the app is current.
      maybeShowSpeakerUpdateCard();
    };
    return { outcome: 'available', version: m.version };
  } catch (e) {
    try { console.warn('checkAppUpdate failed', e); } catch {}
    // A check that could not run must not swallow the control that triggers
    // it, and a person who pressed it is owed the failure: with the link
    // quietly resetting, a machine on which the request always fails (AV
    // proxy, firewall, DNS) looks exactly like "no update exists", and one
    // user pressed through several releases believing that.
    try { renderAppUpdateCheckLink(banner, manual ? t('banner.appCheckFailed') : ''); } catch {}
    // A silent automatic check that ALWAYS fails looks exactly like "you are up
    // to date", forever. One user sat on a June build until the end of
    // September, 142 releases behind, and nothing on his screen was ever about
    // the app itself. After a run long enough to rule out a bad afternoon, say
    // it once, quietly, with the way out beside it.
    if (!manual) {
      try {
        if (shouldSayChecksAreFailing(noteCheckFailed())) {
          showToast(t('banner.appCheckKeepsFailing'));
        }
      } catch { /* never let the notice break the startup */ }
    }
    return { outcome: 'failed' };
  }
}

// runAppUpdate downloads + verifies the new version and installs it (#71). On
// Linux/Windows the backend replaces the running binary and relaunches, so the
// app quits mid-call and the code after ApplyUpdate only runs on macOS (assisted:
// the verified .dmg is opened for the user to drag into Applications). On any
// failure the button becomes a "download from the website" fallback so the user
// is never stuck.
// downloadWasInterrupted separates "the connection went away" from a genuine
// failure of the update itself.
//
// Everything here is a transport fault: the machine lost its network, the
// connection was cut, the server never answered, or the download stopped short.
// None of them says anything is wrong with the app, the release or the file, so
// none of them earns an error the user is invited to copy and send on. A hash
// mismatch or a file that cannot be written is the opposite and is left to the
// full error path.
//
// Matching on the message rather than a typed error because these arrive from
// the Go side already flattened into a string.
export function downloadWasInterrupted(err) {
  const s = String(err || '').toLowerCase();
  return [
    'network is unreachable', 'unreachable network', // the machine has no route
    'no route to host', 'no such host', 'dns',        // gateway or resolver gone
    'connection reset', 'connection refused', 'broken pipe',
    'eof', 'unexpected eof',                          // cut mid-transfer
    'timeout', 'deadline exceeded', 'timed out',
    'canceled', 'cancelled', 'aborted',
    'i/o error', 'connection was aborted',
  ].some((m) => s.includes(m));
}

// showMacHandoff turns the update banner into the standing instruction for the
// one step macOS leaves to the user: drag the new app out of the mounted .dmg.
//
// It replaces the banner rather than adding a second notice, because at this
// point the "install now" button has done everything it can and re-pressing it
// only downloads the same file again. The button here is short on purpose (Jens'
// rule: a button carries a few words, never a status message) and re-opens the
// downloaded file for anyone who closed the Finder window before reading it.
//
// Nothing dismisses this by itself. The banner is rebuilt from scratch by the
// next update check, which finds the app already current once the user has
// dragged it across, so the instruction disappears exactly when it is obsolete.
function showMacHandoff(path) {
  const banner = $('appUpdateBanner');
  if (!banner) { showToast(t('banner.macDownloaded')); return; }
  banner.innerHTML = `
    <div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#10003;</span><span><b>${escapeHtml(t('banner.macReadyTitle'))}</b> ${escapeHtml(t('banner.macDownloaded'))}</span></div>
    <button class="btn btn-primary app-update-btn" id="appUpdateReveal">${escapeHtml(t('banner.macShowFile'))}</button>
  `;
  banner.classList.remove('hidden');
  const rv = $('appUpdateReveal');
  if (rv && path) rv.onclick = () => { RevealUpdateFile(path).catch(() => {}); };
}

function showDebHandoff() {
  const banner = $('appUpdateBanner');
  if (!banner) { showToast(t('banner.debHandoff'), 12000); return; }
  banner.innerHTML = `
    <div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#10003;</span><span>${escapeHtml(t('banner.debHandoff'))}</span></div>
  `;
  banner.classList.remove('hidden');
}

// wingetNoteHTML renders a winget note for the update banner: the text and,
// when there is one, the command to run by hand with a Copy button.
function wingetNoteHTML(view) {
  const cmd = view.command
    ? `<code class="app-update-cmd">${escapeHtml(view.command)}</code><button class="btn app-update-copy" data-winget-copy>${escapeHtml(t('banner.wingetCopy'))}</button>`
    : '';
  return `<div class="app-update-text app-update-winget"><span>${escapeHtml(view.text)}</span>${cmd}</div>`;
}

function wireWingetCopy(root, command) {
  const b = root.querySelector('[data-winget-copy]');
  if (b && command) b.onclick = () => { ClipboardSetText(command).catch(() => {}); };
}

// runWingetUpdate is runAppUpdate for a copy winget installed. Nothing is
// downloaded here: the backend asks winget whether it offers the version,
// starts `winget upgrade` in a hidden helper and quits, and the helper starts
// the app again when winget is done. When it cannot, the banner shows the
// command to run by hand.
async function runWingetUpdate(version, btn, installLabel) {
  btn.disabled = true;
  btn.textContent = t('banner.connecting');
  try {
    const res = await UpdateViaWinget(version);
    const view = wingetOutcomeView(res, version, t);
    const banner = $('appUpdateBanner');
    if (!banner) { showToast(view.text, 15000); return; }
    banner.innerHTML = wingetNoteHTML(view);
    banner.classList.remove('hidden');
    wireWingetCopy(banner, view.command);
  } catch (e) {
    btn.disabled = false;
    btn.textContent = installLabel;
    showError(t('banner.updateFailed', { err: String(e) }));
  }
}

async function runAppUpdate(version, btn, installLabel, isMacOS, fallbackUrl) {
  btn.disabled = true;
  const off = EventsOn('app:update:progress', (p) => {
    const pct = (p && typeof p === 'object') ? p.pct : p;
    const rate = (p && typeof p === 'object' && p.bytesPerSec) ? ' (' + fmtRate(p.bytesPerSec) + ')' : '';
    btn.textContent = t('banner.downloadingPct', { pct }) + rate;
  });
  try {
    // Not "Downloading 0%". Nothing is downloading yet: the release
    // manifest still has to be looked up and the connection opened, which is
    // a minute of legitimate waiting on a slow link, and the first progress
    // event only fires once body bytes arrive. Showing 0% for all of it made
    // a reporter watch a frozen percentage and take a diagnostic (#935).
    btn.textContent = t('banner.connecting');
    const path = await DownloadUpdate(version);
    btn.textContent = t('banner.installing');
    await ApplyUpdate(path);
    // Reached only on macOS and for a Linux .deb copy (Linux/Windows otherwise
    // relaunch+quit inside ApplyUpdate).
    //
    // The last step is the user's: drag the app out of the mounted .dmg. Saying
    // that in a toast did not work, because ApplyUpdate opens the .dmg and
    // Finder's window comes up over ours, exactly where the toast sits, and the
    // toast is gone by the time the user looks back. Reported 2026-08-06 with a
    // screenshot showing the Finder window on top of it: "I feel that this
    // notice is easy to miss". So the instruction takes over the update banner
    // and stays there until the new version is actually running.
    if (isMacOS) {
      btn.disabled = false;
      btn.textContent = installLabel;
      showMacHandoff(path);
    } else if (state.appInfo && state.appInfo.linuxPackage === 'deb') {
      // A .deb copy: ApplyUpdate opened the package installer, which does the
      // rest. The user confirms there and starts the new version.
      showDebHandoff();
    }
  } catch (e) {
    btn.disabled = false;
    // A download the network cut off is not a fault anybody needs to report,
    // and it is not a reason to send the user to a web page either: the same
    // button, pressed again once the connection is back, is the whole fix.
    // Reported by shorty310 (#916) after pulling his Wi-Fi mid-download: he got
    // the copyable error modal, which reads as "send this to the developer",
    // and a button that had turned into "Get it from the downloads page".
    if (downloadWasInterrupted(e)) {
      btn.textContent = t('banner.retryAppUpdate');
      btn.onclick = () => runAppUpdate(version, btn, installLabel, isMacOS, fallbackUrl);
      showToast(t('banner.updateInterrupted'), 9000);
    } else {
      // A real failure: a hash that did not match, a file that could not be
      // written. That one IS worth showing in full and worth the web fallback.
      showError(t('banner.updateFailed', { err: String(e) }));
      btn.textContent = t('banner.getFromReleases');
      if (fallbackUrl) btn.onclick = () => BrowserOpenURL(fallbackUrl);
      // Reassure the non-technical user: the app replaces itself, so any .exe
      // they downloaded by hand earlier can simply be deleted (the
      // duplicate-copies confusion that prompted this).
      showToast(t('banner.manualHint'));
    }
  } finally {
    if (typeof off === 'function') off();
  }
}

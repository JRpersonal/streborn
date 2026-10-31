// views/settings/status.js: the two sections pinned outside the accordion
// groups. The Status block (software version, USB stick, the update banner)
// stays at the top of Speaker settings; the phone-control card is a headline
// feature and sits below the groups rather than buried in one.

import { state } from '../../state.js';
import { $, escapeHtml, escapeAttr, compareVerBuild } from '../../utils.js';
import { t } from '../../i18n/index.js';
import { BoxAgentVersion, PhoneQR, PhoneAddressFor, RebootBox, boxFetch } from '../../api.js';
import { deps, confirmFor, reportError, reportDone } from './shared.js';
import { inLiveGroup, anyInLiveGroup } from '../../groups.js';

// renderStatusSection renders the Status block; it is filled by wireStatusSections.
export function renderStatusSection() {
  return {
    group: 'top',
    html: `
    <div data-ui2-sec="status" class="settings-section" id="stickInfoSection">
      <h3>${escapeHtml(t('settingsView.statusHeading'))}</h3>
      <div id="stickInfoBody"><span class="muted small">${escapeHtml(t('common.loading'))}</span></div>
    </div>
  `,
  };
}

// renderPhoneSection renders the phone-control card with the QR code slot.
export function renderPhoneSection() {
  return {
    group: 'bottom',
    html: `
    <div data-ui2-sec="phone" class="settings-section phone-feature" id="phoneCardSection">
      <h3><span class="phone-feature-icon">&#128241;</span> ${escapeHtml(t('settingsView.phoneHeading'))}</h3>
      <div class="phone-card">
        <img id="phoneQrImg" class="phone-qr" alt="${escapeAttr(t('settingsView.phoneHelp'))}" />
        <div class="phone-url-col">
          <div class="phone-url-row">
            <code id="phoneUrl" class="phone-url"></code>
            <button class="btn btn-mini" id="phoneUrlCopy">${escapeHtml(t('common.copy'))}</button>
          </div>
          <p class="phone-scan-hint">${escapeHtml(t('settingsView.phoneHelp'))}</p>
        </div>
        <small class="muted small phone-url-alt" id="phoneUrlAlt" hidden></small>
      </div>
      ${phoneHomeScreenBlock()}
    </div>
  `,
  };
}

export function wireStatusSections(ctx) {
  const { box } = ctx;

  // Phone control: build this speaker's web-remote URL from its reachable
  // host:port (probeSTR records the right port: 8888 direct or 17008 redirect)
  // and render a locally generated QR (no external service). Pinned below the
  // accordion groups (see renderBoxSettings).
  (async () => {
    const urlEl = $('phoneUrl');
    if (!urlEl || !box || !box.host) return;
    // The address is what the code used to carry, and an address is the thing
    // that changes: a new DHCP lease and the page somebody has on their home
    // screen points at nothing, with no clue as to why. So the app asks for a
    // NAME that it has resolved back to this speaker and reached the speaker
    // through, and falls back to the address when there is no such name.
    //
    // This PC resolving a name says nothing about the phone, though: a phone
    // with a private-DNS setting, or on a guest network, resolves neither the
    // router's name nor a .local one. So the address stays one tap away below
    // the code rather than being replaced by it.
    const addr = `http://${box.host}:${box.port || 8888}/`;
    let picked = { url: addr, addressUrl: addr, source: 'address' };
    try {
      const r = await PhoneAddressFor(box.host, box.port || 8888);
      if (r && r.url) picked = r;
    } catch { /* the address form is already in place */ }
    let url = picked.url;
    urlEl.textContent = url;
    const copyBtn = $('phoneUrlCopy');
    if (copyBtn) {
      copyBtn.onclick = async () => {
        try {
          await navigator.clipboard.writeText(url);
          copyBtn.textContent = t('common.copied');
          setTimeout(() => { copyBtn.textContent = t('common.copy'); }, 1500);
        } catch { /* clipboard blocked: the URL is still selectable */ }
      };
    }
    const renderQR = async (target) => {
      const data = await PhoneQR(target);
      const img = $('phoneQrImg');
      if (img && data) img.src = data;
    };
    // One line under the code, offering whichever form is NOT on it. Named
    // rather than hidden behind a settings switch: a person standing in front
    // of a phone that will not open the name needs the address now.
    const alt = $('phoneUrlAlt');
    const paintAlt = () => {
      if (!alt) return;
      const onName = url !== picked.addressUrl;
      if (!picked.name) { alt.hidden = true; return; }
      alt.hidden = false;
      const key = onName ? 'settingsView.phoneUseAddress' : 'settingsView.phoneUseName';
      alt.innerHTML = `<a href="#" id="phoneUrlSwap">${escapeHtml(t(key))}</a>`;
      const a = $('phoneUrlSwap');
      if (a) a.onclick = async (e) => {
        e.preventDefault();
        url = onName ? picked.addressUrl : picked.url;
        urlEl.textContent = url;
        try { await renderQR(url); } catch {}
        paintAlt();
      };
    };
    paintAlt();
    try {
      await renderQR(url);
      const img = $('phoneQrImg');
      // Tapping the code opens the instructions underneath. People point a
      // phone at it first and look for the next step second, and the summary
      // line was the only way in.
      if (img) {
        img.style.cursor = 'pointer';
        img.onclick = () => {
          const how = $('phoneHowto');
          if (!how) return;
          how.open = true;
          how.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
        };
      }
    } catch (e) { try { console.warn('phone QR failed', e); } catch {} }
    // The home-screen sketches show the icon the phone will actually end up
    // with: the agent serves it at /icon.png, the same file the manifest points
    // at. A speaker that is asleep or gone simply leaves the tile blank rather
    // than showing a broken image.
    document.querySelectorAll('[data-role=phoneShotIcon]').forEach((el) => {
      el.onerror = () => { el.classList.add('hs-ico-missing'); el.removeAttribute('src'); };
      el.src = url + 'icon.png';
    });
  })();

  // Status block: software version + USB stick mount.
  (async () => {
    const body = $('stickInfoBody');
    if (!body) return;
    const app = state.appInfo || {};

    // Software version, three tiers:
    //   - Version + build match -> up to date (green)
    //   - Version matches, build differs -> update available (yellow)
    //   - Version differs -> outdated (red)
    let softwareLine = `<span class="muted small">${escapeHtml(t('common.unknown'))}</span>`;
    let softwareBtn = '';
    try {
      const v = await BoxAgentVersion(box.host, box.port);
      const boxVer = v.version || '?';
      const boxBuild = v.build || '';
      const appVer = app.version || '?';
      const appBuild = app.build || '';
      // Direction-aware (see compareVerBuild): only offer the OTA when the app
      // is newer than the box. When the box is newer than the app, an OTA would
      // downgrade it, so show "update the app" with no button (#105).
      const cmp = compareVerBuild(appVer, appBuild, boxVer, boxBuild);
      // Three states, not two. The third is "the update running right now is
      // THIS speaker's": this panel is rendered from an async fetch chain, so it
      // regularly lands seconds after doBoxUpdate has already started, and a
      // fresh ENABLED "Update" button with an empty status line over a speaker
      // that is at that moment being flashed reads as "the click did nothing"
      // and invites a second press (Jens, 2026-08-22).
      const otaHere = state.otaInProgress && state.otaTargetHost === box.host;
      const otaElsewhere = state.otaInProgress && state.otaTargetHost && !otaHere;
      const otaBtn = () => (otaHere || otaElsewhere)
        ? `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn" disabled>${escapeHtml(t('update.runningBtn'))}</button><div class="op-status" id="stickInfoUpdateStatus">${escapeHtml(otaHere ? t('update.uploading') : t('update.otherBoxRunning', { name: state.otaTargetName || '...' }))}</div>`
        : `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn">${escapeHtml(t('update.refreshBtn'))}</button><div class="op-status" id="stickInfoUpdateStatus"></div>`;
      if (cmp === 0) {
        const buildSuffix = boxBuild ? ` (Build ${escapeHtml(boxBuild)})` : '';
        if (v.goLibrespot === 'missing') {
          // The agent is current but the Spotify engine sidecar is absent (an
          // interrupted engine delivery, discussion #406). A bare green "up to
          // date" here hid that; say it and offer the update button, which
          // re-delivers the engine.
          softwareLine = `<span class="fw-warn">${escapeHtml(t('settingsView.swCurrentNoEngine'))}</span> <span class="muted small">${escapeHtml(boxVer)}${buildSuffix}</span>`;
          softwareBtn = otaBtn();
        } else {
          softwareLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.swCurrent'))}</span> <span class="muted small">${escapeHtml(boxVer)}${buildSuffix}</span>`;
        }
      } else if (cmp > 0) {
        // When only the build stamp differs (same version string), show the
        // build on both sides so the line is not the confusing "v0.8.1 -> v0.8.1"
        // (Jens, 2026-06-17). A real release bumps the version, so production
        // never hits the same-version case; this is mainly dev builds.
        const sameVer = boxVer === appVer;
        const instDisp = sameVer && boxBuild ? `${boxVer} (Build ${boxBuild})` : boxVer;
        const nextDisp = sameVer && appBuild ? `${appVer} (Build ${appBuild})` : appVer;
        softwareLine = `<span class="fw-pending">${escapeHtml(t('settingsView.swUpdateAvail'))}</span> <span class="muted small">${escapeHtml(t('update.versionLine', { installed: instDisp, next: nextDisp }))}</span>`;
        softwareBtn = otaBtn();
      } else {
        softwareLine = `<span class="fw-pending">${escapeHtml(t('update.appBehindShort', { appVersion: appVer }))}</span> <span class="muted small">${escapeHtml(boxVer)}</span>`;
      }
    } catch {}

    // USB stick mount status. Try /api/stick/status first (newer
    // agent); fall back to /api/debug/state.stick_listing for older
    // agent versions.
    let stickLine = `<span class="muted small">${escapeHtml(t('common.unknown'))}</span>`;
    let sshOpen = false;
    let sshPersistent = false;
    try {
      const r = await boxFetch(box, '/api/stick/status');
      const ct = r.headers.get('content-type') || '';
      if (r.ok && ct.includes('json')) {
        const data = await r.json();
        sshOpen = !!data.sshOpen;
        // Persistent SSH left open via a NAND marker (remote_services /
        // enable-ssh) is a deliberate stickless choice; a reboot does not close
        // it, so it must not be reported as "stick still inserted" (#381, #385).
        sshPersistent = !!data.sshPersistent;
        // Trust the agent's mounted flag (v0.7.33+ stickReallyMounted reports it
        // only for a real stick, not the leftover empty mountpoint, #105). Do NOT
        // also require data.version: the agent can report mounted without a
        // version, and requiring it wrongly showed an inserted stick as removed
        // (#105 follow-up).
        if (data.mounted) {
          stickLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.stickDetected'))}</span>` + (data.version ? ` <span class="muted small">${escapeHtml(data.version)}</span>` : '');
        } else if (sshOpen && sshPersistent) {
          // No stick, but SSH is deliberately kept open across reboots via a NAND
          // marker. Report it as an intentional, informational state, not as a
          // stuck stick (Jens, #381/#385 meierchen006).
          stickLine = `<span class="muted small">${escapeHtml(t('settingsView.sshPersistent'))}</span>`;
        } else if (sshOpen) {
          // Stick not mounted but SSH is open. On STR that only happens because a
          // stick was in at boot (it opens sshd via the remote_services marker;
          // pulling it out does not close sshd until the next reboot). Some boxes
          // (the Portable) never auto-mount the stick, so mounted=false even with
          // the stick physically in. Report it honestly as "still inserted"
          // instead of flatly "removed", which contradicted the remove-the-stick
          // recommendation right below it (Jens, 2026-06-17).
          stickLine = `<span class="fw-warn">${escapeHtml(t('settingsView.stickStillInserted'))}</span>`;
        } else {
          // No stick and SSH closed: the secure steady state after a clean install.
          stickLine = `<span class="muted small">${escapeHtml(t('settingsView.stickRemoved'))}</span>`;
        }
      } else {
        // Fallback: debug/state listing for older agents.
        const rd = await boxFetch(box, '/api/debug/state');
        if (rd.ok && (rd.headers.get('content-type') || '').includes('json')) {
          const d = await rd.json();
          const listing = d.stick_listing;
          if (Array.isArray(listing) && listing.length > 0 && !String(listing[0]).startsWith('ERR')) {
            stickLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.stickDetected'))}</span>`;
          } else {
            stickLine = `<span class="muted small">${escapeHtml(t('settingsView.stickRemoved'))}</span>`;
          }
        }
      }
    } catch {}

    // Show the warning banner only once the stick is no longer
    // mounted — while a stick is in the box, the agent is either
    // doing initial install or applying an update, SSH is expected
    // to be open in that window, and the banner is just noise.
    // Also suppress while an OTA update is in flight: the agent is
    // mid-restart and SSH state is transient; the banner's "Reboot
    // now" button would interrupt the update.
    // In Speaker Settings the detailed recommendation below (securityWarn) is the
    // richer version of the same warning, so hide the global top banner here to
    // avoid showing it twice. checkSshBanner drives the top banner in the other
    // views.
    const gb = $('globalSecurityBanner');
    if (gb) gb.classList.add('hidden');

    // Show the remove-the-stick + restart recommendation whenever SSH is open.
    // As of the pre-1.0 hardening (run.sh no longer force-opens sshd every boot),
    // SSH being open means a stick was in at boot (the stick is what opens sshd
    // via its remote_services marker) and a stickless reboot closes it again, so
    // sshOpen is now a self-clearing, accurate signal. This also fixes the case
    // where the stick is in but not mounted (the Portable): the old
    // `sshOpen && !stickMounted` happened to work there, but `mounted` boxes
    // showed nothing. Suppressed during the OTA window (the agent restarts and
    // the reboot button would interrupt it).
    // When SSH is open because the user set a persistent NAND marker, the
    // "reboot to close it" advice is wrong (the reboot keeps it open), so show a
    // calm informational note on how to actually close it, with no reboot button
    // (#381/#385). The transient stick-driven case keeps the reboot recommendation.
    let securityWarn = '';
    if (sshOpen && !state.otaInProgress) {
      securityWarn = sshPersistent ? `
      <div class="security-warn security-info">
        <div class="security-warn-text">
          ${escapeHtml(t('banner.sshPersistentNote'))}
        </div>
      </div>` : `
      <div class="security-warn">
        <div class="security-warn-title">${escapeHtml(t('banner.recommendationShort'))}</div>
        <div class="security-warn-text">
          ${escapeHtml(t('banner.sshRecommend'))}
        </div>
        <button class="btn btn-mini" id="securityRebootBtn">${escapeHtml(t('speaker.rebootNow'))}</button>
      </div>`;
    }

    // "Update all (N)" alternative to the single-speaker update, shown here in
    // Settings whenever 2+ speakers need an update, regardless of whether THIS
    // one does. Previously the button only appeared inside the selected box's
    // update banner, so a user whose selected speaker was already current could
    // not find it at all (Michal, 6 speakers, 2026-07-16).
    let updateAllBtn = '';
    let updateAllTargets = [];
    try {
      updateAllTargets = (state.boxes || []).filter(b => b && b.kind !== 'stock' && b.host && deps.boxNeedsUpdate && deps.boxNeedsUpdate(b));
      const n = updateAllTargets.length;
      if (n >= 2 && !state.otaInProgress && deps.updateAllBoxes) {
        updateAllBtn = `<button class="btn btn-mini btn-secondary" id="settingsUpdateAllBtn">${escapeHtml(t('updateAll.button', { count: n }))}</button>`;
      }
    } catch { /* box list not ready */ }

    // Prominent update banner at the very TOP of Speaker Settings whenever an
    // update is available (softwareBtn is only set then) OR two or more speakers
    // can be updated together (updateAllBtn). Moved here from the music view so
    // normal users see it where they manage the speaker. The update button lives
    // in the banner now; the software kv-row below keeps the version status text.
    const bannerMsg = softwareBtn
      ? `<b>${escapeHtml(t('update.speakerUpdateAvailFor', { name: box.friendlyName || box.name || box.host }))}</b>`
      : `<b>${escapeHtml(t('updateAll.title'))}</b>`;
    // A speaker in a live group leaves it while it restarts: said before the
    // tap (#1339). With "Update all" on offer the note speaks for the whole
    // run, because any grouped speaker in it breaks its group the same way;
    // otherwise for the single-speaker update.
    const groupNote = (updateAllBtn && anyInLiveGroup(updateAllTargets, state.zoneLive))
      ? `<br><small class="muted" id="updateAllGroupNote">${escapeHtml(t('updateAll.groupNote'))}</small>`
      : (softwareBtn && inLiveGroup(box, state.zoneLive))
        ? `<br><small class="muted" id="updateGroupNote">${escapeHtml(t('update.groupNote'))}</small>` : '';
    const updateBanner = (softwareBtn || updateAllBtn) ? `
      <div class="update-banner" style="margin-bottom:14px">
        <div class="update-msg">${bannerMsg}<br>
          <small class="muted">${escapeHtml(t('update.rebootNote'))}</small>${groupNote}</div>
        ${softwareBtn}${updateAllBtn}
      </div>` : '';
    body.innerHTML = updateBanner + `
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.softwareLabel'))}</span>
        <span class="kv-val">${softwareLine}</span></div>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.usbStickLabel'))}</span>
        <span class="kv-val">${stickLine}</span></div>
      ${securityWarn}
    `;
    const ub = $('stickInfoUpdateBtn');
    if (ub) {
      // The stick-in confirmation now lives inside doBoxUpdate (the single
      // OTA chokepoint), so both this button and the music-tab banner are
      // gated identically and Cancel always aborts before anything starts.
      // Pass the settings-selected box explicitly: every other handler here
      // targets settingsBox, and doBoxUpdate defaulting to currentBox was
      // OTA-ing the music-tab box instead of the one shown here (#105).
      ub.onclick = () => deps.doBoxUpdate(box);
    }
    const uaAll = $('settingsUpdateAllBtn');
    if (uaAll && deps.updateAllBoxes) uaAll.onclick = () => deps.updateAllBoxes();
    const sb = $('securityRebootBtn');
    if (sb) sb.onclick = async () => {
      const ok = await confirmFor(box, 
        t('speaker.rebootConfirmTitle'),
        t('speaker.rebootConfirmDetailedBody')
      );
      if (!ok) return;
      try {
        await RebootBox(box.host, box.port);
        reportDone(sb, t('speaker.rebootingToast'));
        setTimeout(deps.discoverBoxes, 35000);
      } catch (e) { reportError(sb, e); }
    };
  })();
}

// phoneHomeScreenBlock renders what the bare QR code never said out loud: the
// phone page IS an app, and here are the taps that put it on a home screen.
//
// A user asked for an iOS and Android app while his speakers were already
// serving one, which is the whole problem in one sentence: the capability
// shipped (manifest, icon, fullscreen flag), the invitation did not. A QR code
// with a single line under it leaves the reader at the door.
//
// Both platforms are shown side by side rather than behind a switch, because
// people hand the phone to someone else and a switch left on the wrong platform
// becomes a support message. The two sketches carry the part no sentence can:
// the speaker's own icon sitting on a home screen among the apps people already
// have. The icon is the REAL one the agent serves at /icon.png (painted in
// afterwards), never a drawing, so what is promised here cannot drift from what
// the phone ends up showing.
function phoneHomeScreenBlock() {
  const dummy = (cls) => `<span class="hs-ico ${cls}"></span>`;
  const step = (key) => `<li>${t(key)}</li>`;
  return `
    <details class="phone-howto" id="phoneHowto">
      <summary>${escapeHtml(t('settingsView.phoneHowtoSummary'))}</summary>
      <div class="phone-howto-body">
        <div class="phone-paths">
          <div class="phone-path phone-path-ios">
            <div class="phone-path-head">
              ${escapeHtml(t('settingsView.phoneIosTitle'))}
              <span class="phone-chip">${escapeHtml(t('settingsView.phoneIosChip'))}</span>
            </div>
            <ol>
              ${step('settingsView.phoneIos1')}
              ${step('settingsView.phoneIos2')}
              ${step('settingsView.phoneIos3')}
              ${step('settingsView.phoneIos4')}
            </ol>
            <p class="phone-path-note">${t('settingsView.phoneIosNote')}</p>
            <figure class="phone-shot">
              <div class="hs hs-ios">
                <div class="hs-time">9:41</div>
                <div class="hs-grid">
                ${dummy('')}${dummy('')}
                  <span class="hs-app"><span class="hs-icobox hs-ring"><img class="hs-ico hs-real" data-role="phoneShotIcon" alt="" /></span><span class="hs-name">ST Reborn</span></span>
                ${dummy('')}
                </div>
                <div class="hs-dock">${dummy('')}${dummy('')}${dummy('')}${dummy('')}</div>
              </div>
              <figcaption>${escapeHtml(t('settingsView.phoneShotIos'))}</figcaption>
            </figure>
          </div>
          <div class="phone-path phone-path-and">
            <div class="phone-path-head">
              ${escapeHtml(t('settingsView.phoneAndroidTitle'))}
              <span class="phone-chip">${escapeHtml(t('settingsView.phoneAndroidChip'))}</span>
            </div>
            <ol>
              ${step('settingsView.phoneAndroid1')}
              ${step('settingsView.phoneAndroid2')}
              ${step('settingsView.phoneAndroid3')}
              ${step('settingsView.phoneAndroid4')}
            </ol>
            <p class="phone-path-note">${t('settingsView.phoneAndroidNote')}</p>
            <figure class="phone-shot">
              <div class="hs hs-and">
                <div class="hs-time">9:41</div>
                <div class="hs-grid">
                ${dummy('rnd')}${dummy('rnd')}
                  <span class="hs-app"><span class="hs-icobox hs-ring rnd"><img class="hs-ico rnd hs-real" data-role="phoneShotIcon" alt="" /></span><span class="hs-name">ST Reborn</span></span>
                ${dummy('rnd')}
                </div>
                <div class="hs-pill"></div>
              </div>
              <figcaption>${escapeHtml(t('settingsView.phoneShotAndroid'))}</figcaption>
            </figure>
          </div>
        </div>
      </div>
    </details>`;
}

// views/settings/info.js: the Status & info group of Speaker settings. The
// sources the speaker knows, the music library servers, Pandora and
// iHeartRadio, what other tools left on the speaker, the model and firmware
// row with the update guide, and the two sections that were bucketed here by
// the old heading lookup and keep their place: the home theater levels and the
// SSH port.

import { state } from '../../state.js';
import { $, escapeHtml, escapeAttr, getBoxLabel } from '../../utils.js';
import { t, tLookup, getLocale } from '../../i18n/index.js';
// Firmware facts live in their own module: both views need them, and a view
// cannot be imported outside a browser (see firmware.js).
import {
  LATEST_FW,
  isFwOutdated,
} from '../../firmware.js';
import { firmwareGuideMount, wireFirmwareGuides } from '../../fwguide.js';
import { firmwareLadderDeps } from '../../fwladderdeps.js';
import {
  US_SERVICES, usServiceState, usServicesRegionIsUS, usServicesOptIn, usServiceStatusKey,
  usServiceCanDisconnect, usServiceNeedsLogin, agentErrorText, usDisconnectError, usConnectError,
} from '../../usservices.js';
import {
  RebootBox,
  BrowserOpenURL,
  ClipboardSetText,
  GetFirmwareGuide,
  FlashBoseFirmware,
  EventsOn,
  BoxSSHStatus,
  SetBoxSSHPersistent,
  BoxSpeakerLevels,
  SetBoxSpeakerLevel,
  BoxForeignInfluence,
  UndoForeignFinding,
  ListBoxMediaServers,
  EnableBoxMediaServer,
  DisableBoxMediaServer,
  USServicesStatus,
  SetUSServicesOptIn,
  LinkMusicService,
  UnlinkMusicService,
} from '../../api.js';
import { helpBlock, revealInSettings, formatRel, confirmFor, wireSettingToggle } from './shared.js';

export function renderInfoSections(ctx) {
  const { s, info, box } = ctx;
  const sources = rollupSources(s.sources || []);
  return {
    group: 'info',
    html: `
    <div data-ui2-sec="levels" class="settings-section" id="boxLevelsSection" hidden>
      <h3>${escapeHtml(t('settingsView.levelsHeading'))}</h3>
      <div class="setting-row" id="boxLevelCentreRow" hidden>
        <label for="boxLevelCentre">${escapeHtml(t('settingsView.levelCentre'))}</label>
        <input type="range" id="boxLevelCentre" />
        <span class="setting-value" id="boxLevelCentreVal"></span>
      </div>
      <div class="setting-row" id="boxLevelSurroundRow" hidden>
        <label for="boxLevelSurround">${escapeHtml(t('settingsView.levelSurround'))}</label>
        <input type="range" id="boxLevelSurround" />
        <span class="setting-value" id="boxLevelSurroundVal"></span>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.levelsHelp'))}</small>
      <div class="setting-row" id="boxLevelsNoteRow" hidden>
        <small class="muted small" id="boxLevelsNote"></small>
      </div>
    </div>

    <div data-ui2-sec="ssh" class="settings-section hidden" id="sshSection">
      <h3>${escapeHtml(t('settingsView.sshHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="sshOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="sshOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.sshHelp'))}</small>
      <small class="muted small hidden" id="sshBoseNote" style="display:block;margin-top:6px">${escapeHtml(t('settingsView.sshBoseMarker'))}</small>
    </div>

    <div data-ui2-sec="sources" class="settings-section">
      <h3>${escapeHtml(t('settingsView.sourcesHeading'))}</h3>
      <div class="sources-grid">
        ${sources.map(src => {
          const su = (src.source || '').toUpperCase();
          const ready = src.status === 'READY';
          // Sources STR revives even though the box reports them UNAVAILABLE (the
          // Bose cloud is gone): the box plays STR's stream over UPnP, Spotify runs
          // through STR's go-librespot, and the Library plays from a DLNA server
          // via the app. Showing those as "inactive" was misleading; mark them
          // "via STR". QPlay and the like stay inactive (STR does not revive them).
          const viaSTR = !ready && (su === 'UPNP' || su === 'SPOTIFY' || su.startsWith('STORED_MUSIC') || su === 'LOCAL_MUSIC');
          // AirPlay and Bluetooth are local firmware features that work without
          // the Bose cloud. The box's /sources READY flag is unreliable for them
          // post-cloud (it tracked the original Bose-account setup), so a working
          // AirPlay can report not-READY and was shown as "inactive" (#200). Label
          // these "available" instead, with the hint below for the nuance.
          const localFw = !ready && (su === 'AIRPLAY' || su === 'BLUETOOTH');
          const cls = ready ? 'src-ok' : (viaSTR ? 'src-ok src-via-str' : (localFw ? 'src-ok' : 'src-unav'));
          const label = sourceLabel(src.source);
          const statusLabel = ready ? t('settingsView.sourceActive') : (viaSTR ? t('settingsView.sourceViaSTR') : (localFw ? t('settingsView.sourceAvailable') : t('settingsView.sourceInactive')));
          // Name the socket when there is more than one of the same kind,
          // otherwise three identical "AUX" pills say nothing.
          const acc = physicalInputAccount(src);
          const shown = acc && acc.toUpperCase() !== String(src.source).toUpperCase() ? `${label} (${acc})` : label;
          return `<div class="source-pill ${cls}" title="${escapeAttr(sourceHint(src.source) || src.sourceAccount || '')}">${escapeHtml(shown)} <small>${escapeHtml(statusLabel)}</small></div>`;
        }).join('')}
      </div>
      <small class="muted small" style="display:block">${escapeHtml(t('settingsView.spotifyHint'))}</small>
      ${sources.some(x => x.source === 'AIRPLAY' && x.status !== 'READY') ? `<small class="muted small" style="display:block;margin-top:4px">${escapeHtml(t('settingsView.airplayHint'))}</small>` : ''}
    </div>

    <div data-ui2-sec="musicLib" class="settings-section" id="musicLibSection">
      <h3>${escapeHtml(t('settingsView.musicLibHeading'))}</h3>
      <div id="musicLibList" class="muted small">${escapeHtml(t('common.loading'))}</div>
      <div id="musicLibMsg"></div>
      <small class="muted small">${escapeHtml(t('settingsView.musicLibHelp'))}</small>
    </div>

    <div data-ui2-sec="usServices" class="settings-section" id="usServicesSection" style="display:none">
      <h3>Pandora &amp; iHeartRadio</h3>
      <div id="usServicesBody" class="muted small">${escapeHtml(t('common.loading'))}</div>
      <div id="usServicesMsg"></div>
      <small class="muted small">${escapeHtml(t('usServices.passwordNote'))}</small>
    </div>

    <div data-ui2-sec="foreign" class="settings-section" id="foreignSection" hidden>
      <h3>${escapeHtml(t('settingsView.foreignHeading'))}</h3>
      ${helpBlock(t('settingsView.foreignHelp'), 'p', 'muted small')}
      <div id="foreignList"></div>
      <div id="foreignResult" class="muted small" style="margin-top:8px"></div>
    </div>

    <div data-ui2-sec="speakerInfo" class="settings-section">
      <h3>${escapeHtml(t('settingsView.speakerInfoHeading'))}</h3>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.modelLabel'))}</span><span class="kv-val">${escapeHtml(info.type || '-')}</span></div>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.firmwareLabel'))}</span>
        <span class="kv-val">${fwStatusInline(info)}</span>
      </div>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.deviceIdLabel'))}</span><span class="kv-val small muted">${escapeHtml(info.deviceID || '-')}</span></div>
      ${fwUpdateHint(info, box)}
    </div>
  `,
  };
}

export function wireInfoSections(ctx) {
  const { box } = ctx;

  // Music library. Filled AFTER the markup exists, never from inside the
  // template literal: a call placed in there renders as literal text on the
  // page instead of running.
  fillMusicLib(box).catch(() => {});
  fillUSServices(box).catch(() => {});

  // The "Update" button next to the firmware version scrolls down
  // to the update banner.
  const fwBtn = $('fwUpdateBtn');
  if (fwBtn) {
    fwBtn.onclick = () => {
      revealInSettings($('fwUpdateBanner'));
    };
  }
  // The firmware update guide in the outdated-firmware banner: the Bose
  // download, Bose's article, and copy/open of the speaker's update page, all in
  // the user's browser (Wails BrowserOpenURL). Same guide as the setup screens.
  wireFirmwareGuides(document, {
    getGuide: GetFirmwareGuide,
    openURL: BrowserOpenURL,
    copy: ClipboardSetText,
    flash: FlashBoseFirmware,
    onProgress: (cb) => EventsOn('firmware:progress', cb),
    confirm: (title, body, o) => confirmFor(box, title, body, o),
    ladder: firmwareLadderDeps(),
  });
  const faq = $('fwFaqLink');
  if (faq) faq.onclick = (e) => { e.preventDefault(); try { BrowserOpenURL(faq.dataset.url); } catch {} };

  // The speaker's SSH port. Off by default since the opt-in landed, but two
  // users run it deliberately (#1061) and the only way to say so used to be a
  // file on the speaker reachable only over SSH, which is a circle. Hidden on an
  // agent too old to know the endpoint rather than shown dead.
  const sshBoseNote = $('sshBoseNote');
  // Bose's own marker keeps the port open whatever this switch says, and
  // STR does not delete a file it did not write. Say so instead of
  // promising to close something it cannot.
  const paintSSHNote = (r) => {
    if (sshBoseNote) sshBoseNote.classList.toggle('hidden', !r || r.boseMarker !== true);
  };
  // wireSettingToggle: togglesection.js with the outcome reported in the SSH
  // card in the preview (toggleDepsFor('sshSection')).
  const ssh = wireSettingToggle({
    sectionId: 'sshSection', onId: 'sshOn', offId: 'sshOff',
    get: () => BoxSSHStatus(box.host, box.port),
    stateFn: (r) => ({ show: !!(r && r.supported), enabled: !!r && r.persistent === true, answer: r }),
    onLoaded: (st) => { if (st.show) paintSSHNote(st.answer); },
    confirm: async (on) => {
      if (!on) return true;
      return confirmFor(box, 
        t('settingsView.sshConfirmTitle'),
        t('settingsView.sshConfirmBody'),
      );
    },
    set: (on) => SetBoxSSHPersistent(box.host, box.port, on),
    // The answer to the write is the state the speaker holds now.
    afterSet: (r) => { ssh.paint(!!r && r.persistent === true); paintSSHNote(r); },
    toastKey: (on) => (on ? 'settingsView.sshOnToast' : 'settingsView.sshOffToast'),
  });
}

// fillMusicLib lists the media servers this speaker can see and lets the user
// turn one into a source the SPEAKER plays by itself.
//
// The speaker finds DLNA/UPnP servers on the network on its own, but it will not
// play from one until that server is registered as a music account. Once it is,
// the speaker browses and plays it natively and the library also appears in the
// original Bose app. Verified against a FRITZ!Box and a Synology NAS.
//
// Enabling is NOT instant. The speaker accepts the registration at once and then
// confirms the account with STR before the source becomes usable, which took
// minutes on real hardware. So the row shows what the user asked for
// (`enabled`), with a separate note for "on the speaker" vs "being set up",
// rather than a state that would read as failure for the first few minutes.
async function fillMusicLib(box) {
  const list = $('musicLibList');
  const section = $('musicLibSection');
  if (!list) return;
  if (!box || !box.host || box.kind === 'stock') {
    if (section) section.style.display = 'none';
    return;
  }
  let servers = [];
  try {
    servers = (await ListBoxMediaServers(box.host, box.port)) || [];
  } catch {
    // An older agent has no such endpoint. Nothing useful to say, so say
    // nothing and leave the section out.
    if (section) section.style.display = 'none';
    return;
  }
  if (section) section.style.display = '';
  if (!servers.length) {
    list.innerHTML = `<div class="muted small">${escapeHtml(t('settingsView.musicLibNone'))}</div>`;
    return;
  }
  // The other STR speakers, for the "add to all" action. Stock speakers have no
  // agent to tell, and this box is handled separately so it is never asked twice.
  const otherBoxes = (state.boxes || []).filter(b =>
    b && b.kind !== 'stock' && b.host && b.host !== box.host);
  list.innerHTML = servers.map((srv, i) => {
    const name = srv.friendlyName || srv.modelName || srv.id;
    const where = srv.manufacturer ? `${srv.manufacturer}${srv.ip ? ' · ' + srv.ip : ''}` : (srv.ip || '');
    const stateLabel = srv.enabled
      ? (srv.registered ? t('settingsView.musicLibActive') : t('settingsView.musicLibPending'))
      : '';
    return `<div class="setting-row" style="justify-content:space-between;align-items:center;gap:10px">
      <div style="min-width:0">
        <div style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${escapeHtml(name)}</div>
        <small class="muted small">${escapeHtml(where)}${stateLabel ? ' · ' + escapeHtml(stateLabel) : ''}</small>
      </div>
      <div style="display:flex;gap:6px;flex:none">
        ${otherBoxes.length ? `<button class="btn btn-mini" data-mlall="${i}">${
          escapeHtml(t('settingsView.musicLibAddAll'))}</button>` : ''}
        <button class="btn btn-mini${srv.enabled ? '' : ' btn-primary'}" data-mlidx="${i}">${
          escapeHtml(srv.enabled ? t('settingsView.musicLibRemove') : t('settingsView.musicLibAdd'))}</button>
      </div>
    </div>`;
  }).join('');

  // "Add to all speakers". On real Bose a media server belonged to the ACCOUNT
  // and every speaker had it; in STR each speaker runs its own agent, so the
  // same setting has to be written once per speaker. Doing that by hand for a
  // household of eight is the kind of chore the app should absorb.
  //
  // Every speaker discovers the server itself, and the id is the server's UPnP
  // UDN, which is the same everywhere, so the entry is valid on all of them.
  // A speaker that already has it answers "nothing to push" and is left alone.
  list.querySelectorAll('[data-mlall]').forEach(btn => {
    btn.onclick = async () => {
      const srv = servers[Number(btn.getAttribute('data-mlall'))];
      if (!srv) return;
      const msg = $('musicLibMsg');
      btn.disabled = true;
      if (msg) msg.innerHTML = `<div class="muted small">${escapeHtml(t('common.loading'))}</div>`;
      let ok = 0;
      const failed = [];
      // A speaker the app lists as offline goes straight onto the not-reached
      // list without a network attempt: asking it anyway cost the full
      // transport timeouts mid-loop, and one unplugged box held this spinner
      // for over 20 seconds (field, 2026-08-29). The reachable ones are asked
      // in parallel for the same reason: the wait is the slowest answer, not
      // the sum of all of them.
      const targets = [box, ...otherBoxes];
      targets.filter(b => b.offline).forEach(b => failed.push(getBoxLabel(b)));
      const live = targets.filter(b => !b.offline);
      const results = await Promise.allSettled(
        live.map(b => EnableBoxMediaServer(b.host, b.port, srv.id, srv.friendlyName || '')));
      results.forEach((r, i) => {
        if (r.status === 'fulfilled') ok++;
        // Named, not swallowed: a speaker that was asleep or off is exactly
        // the one the user needs to know about, and the rest still succeed.
        else failed.push(getBoxLabel(live[i]));
      });
      if (msg) {
        msg.innerHTML = failed.length
          ? `<div class="setup-warn">${escapeHtml(t('settingsView.musicLibAllPartial', { ok, failed: failed.join(', ') }))}</div>`
          : `<div class="setup-ok">${escapeHtml(t('settingsView.musicLibAllDone', { ok }))}</div>`;
      }
      btn.disabled = false;
      fillMusicLib(box).catch(() => {});
    };
  });

  list.querySelectorAll('[data-mlidx]').forEach(btn => {
    btn.onclick = async () => {
      const srv = servers[Number(btn.getAttribute('data-mlidx'))];
      if (!srv) return;
      const msg = $('musicLibMsg');
      btn.disabled = true;
      try {
        if (srv.enabled) {
          await DisableBoxMediaServer(box.host, box.port, srv.id, srv.friendlyName || '');
          if (msg) msg.innerHTML = '';
        } else {
          await EnableBoxMediaServer(box.host, box.port, srv.id, srv.friendlyName || '');
          if (msg) msg.innerHTML = `<div class="setup-ok">${escapeHtml(t('settingsView.musicLibWait'))}</div>`;
        }
      } catch (e) {
        if (msg) msg.innerHTML = `<div class="setup-err">${escapeHtml(String(e))}</div>`;
      }
      btn.disabled = false;
      fillMusicLib(box).catch(() => {});
    };
  });
}

// fillUSServices draws the Pandora & iHeartRadio card: per service where it
// stands on this speaker, a login form or Disconnect, and outside the US the
// note that both only play from a US connection plus the opt-in switch.
//
// The speaker's own clients log in and play; the password goes from the form
// to the agent and on to the speaker, and nothing here keeps it. An agent too
// old for /api/us-services, or a build without it, leaves the card out.
async function fillUSServices(box) {
  const section = $('usServicesSection');
  const body = $('usServicesBody');
  if (!section || !body) return;
  if (!box || !box.host || box.kind === 'stock') {
    section.style.display = 'none';
    return;
  }
  let status;
  try {
    status = await USServicesStatus(box.host, box.port);
  } catch {
    section.style.display = 'none';
    return;
  }
  if (!$('usServicesBody')) return;
  section.style.display = '';
  const isUS = usServicesRegionIsUS(status);
  const optIn = usServicesOptIn(status);
  const enabled = isUS || optIn;
  const parts = [];
  if (!isUS) {
    parts.push(`<div class="setup-warn small">${escapeHtml(t('usServices.notUSNote'))}</div>
      <label style="display:block;margin:6px 0"><input type="checkbox" id="usServicesOptIn"${optIn ? ' checked' : ''} /> ${escapeHtml(t('usServices.optInLabel'))}</label>`);
  }
  if (enabled) {
    US_SERVICES.forEach(svc => {
      const st = usServiceState(status, svc.type);
      const line = t(usServiceStatusKey(st), { account: st.account, service: svc.label });
      const badge = svc.experimental ? ` <span class="exp-badge">${escapeHtml(t('settingsView.experimentalBadge'))}</span>` : '';
      const login = usServiceNeedsLogin(st)
        ? `<div class="wlan-row">
            <input type="email" autocomplete="off" id="usUser-${escapeAttr(svc.key)}" placeholder="${escapeAttr(t('usServices.emailPlaceholder'))}" />
          </div>
          <div class="wlan-row">
            <input type="password" autocomplete="off" id="usPass-${escapeAttr(svc.key)}" placeholder="${escapeAttr(t('usServices.passwordPlaceholder'))}" />
            <button class="btn btn-mini btn-primary" data-usconnect="${escapeAttr(svc.key)}">${escapeHtml(t('usServices.connect'))}</button>
          </div>`
        : '';
      const disconnect = usServiceCanDisconnect(st)
        ? `<button class="btn btn-mini" data-usdisconnect="${escapeAttr(svc.key)}">${escapeHtml(t('usServices.disconnect'))}</button>`
        : '';
      const action = login + disconnect;
      parts.push(`<div class="setting-row" style="display:block;margin-top:8px">
        <div><b>${escapeHtml(svc.label)}</b>${badge}</div>
        <small class="muted small" style="display:block;margin-bottom:4px">${escapeHtml(line)}</small>
        ${action}
      </div>`);
    });
  }
  body.className = '';
  body.innerHTML = parts.join('');

  const msg = $('usServicesMsg');
  const say = (cls, text) => { if (msg) msg.innerHTML = text ? `<div class="${cls}">${escapeHtml(text)}</div>` : ''; };
  const changed = () => {
    try { document.dispatchEvent(new CustomEvent('str:usservices-changed', { detail: { host: box.host } })); } catch {}
  };
  // The speaker logs in with the service after the account call returns and
  // registers the source with STR a little later, so the card is read again
  // twice instead of claiming "ready" straight away.
  const refreshSoon = () => {
    setTimeout(() => { fillUSServices(box).catch(() => {}); changed(); }, 6000);
    setTimeout(() => { fillUSServices(box).catch(() => {}); changed(); }, 30000);
  };

  const opt = $('usServicesOptIn');
  if (opt) {
    opt.onchange = async () => {
      opt.disabled = true;
      try {
        await SetUSServicesOptIn(box.host, box.port, opt.checked);
        say('', '');
        changed();
      } catch (e) {
        say('setup-err', agentErrorText(e));
      }
      fillUSServices(box).catch(() => {});
    };
  }
  body.querySelectorAll('[data-usconnect]').forEach(btn => {
    btn.onclick = async () => {
      const key = btn.getAttribute('data-usconnect');
      const userEl = $('usUser-' + key);
      const passEl = $('usPass-' + key);
      const user = userEl ? userEl.value.trim() : '';
      const pass = passEl ? passEl.value : '';
      if (!user || !pass) {
        say('setup-warn', t('usServices.needBoth'));
        return;
      }
      btn.disabled = true;
      say('muted small', t('usServices.connecting'));
      try {
        await LinkMusicService(box.host, box.port, key, user, pass);
        if (passEl) passEl.value = '';
        say('setup-ok', t('usServices.connectSent'));
        refreshSoon();
      } catch (e) {
        const [key, params] = usConnectError(e);
        say('setup-err', t(key, params));
      }
      btn.disabled = false;
    };
  });
  body.querySelectorAll('[data-usdisconnect]').forEach(btn => {
    btn.onclick = async () => {
      const key = btn.getAttribute('data-usdisconnect');
      const svc = US_SERVICES.find(s => s.key === key);
      const ok = await confirmFor(box, t('usServices.disconnectTitle', { service: svc ? svc.label : key }),
        escapeHtml(t('usServices.disconnectBody')));
      if (!ok) return;
      btn.disabled = true;
      try {
        await UnlinkMusicService(box.host, box.port, key);
        say('setup-ok', t('usServices.disconnected'));
        changed();
      } catch (e) {
        const [key, params] = usDisconnectError(e);
        say('setup-err', t(key, params));
      }
      fillUSServices(box).catch(() => {});
    };
  });
}

// Last known firmware major.minor.patch per SoundTouch model. Bose
// shipped the final wave in 2022; nothing more arrived after the
// cloud shutdown. Source: support.bose.com.
// The website FAQ, which carries the downgrade workaround for a firmware
// update that hangs. The German site is a first-class translation, everyone
// else gets the English page.
export function strFaqURL() {
  return getLocale() === 'de' ? 'https://st-reborn.de/de/#faq' : 'https://st-reborn.de/#faq';
}

// fwStatusInline renders the firmware row.
//   Up to date: green text + checkmark
//   Outdated:  red text + an Update button next to it
function fwStatusInline(info) {
  const v = escapeHtml(info.version || '-');
  if (!info.version) return v;
  if (isFwOutdated(info)) {
    return `<span class="fw-old">${v}</span> <button class="btn btn-mini btn-danger" id="fwUpdateBtn">${escapeHtml(t('fw.updateBtn'))}</button>`;
  }
  return `<span class="fw-ok">&#10003; ${v}</span>`;
}

function fwUpdateHint(info, box) {
  const want = LATEST_FW[info.type || ''];
  if (!isFwOutdated(info)) {
    return `<small class="muted small">${escapeHtml(t('fw.uptodate', { version: want || '27.0.6' }))}</small>`;
  }
  // The same step-by-step guide the setup screens show (fwguide.js), so a
  // speaker gets one set of instructions wherever its firmware is flagged.
  return `
    <div class="fw-update-banner" id="fwUpdateBanner">
      ${firmwareGuideMount({
    model: info.type || '',
    moduleType: info.moduleType || '',
    variant: info.variant || '',
    current: info.version || '',
    context: 'settings',
    host: (box && box.host) || '',
  })}
      <small class="muted small">${escapeHtml(t('fw.faqTip'))} <a href="#" class="link" id="fwFaqLink" data-url="${escapeHtml(strFaqURL())}">st-reborn.de</a></small>
    </div>`;
}

// Source labels and hints are resolved via the i18n bundle. Keys are
// `source.label.<UPPER>` and `source.hint.<UPPER>`. Falls back to the
// raw API enum value when no translation is registered.
function sourceLabel(key) {
  return tLookup('source.label', key) || key;
}
function sourceHint(key) {
  return tLookup('source.hint', key) || '';
}

// rollupSources removes NOTIFICATION (internal), collapses multiple
// entries of the same source type into a single pill and prefers
// READY over UNAVAILABLE. The result is then sorted with READY
// first.
// physicalInputAccount returns the account of a source when that account names
// a distinct physical socket rather than a linked service.
//
// An SA-5 has three line inputs and reports all of them as source "AUX",
// distinguished only by the account (Phono, AUX 2, AUX 3). Grouping by the
// source alone folded the three into one, so the app offered a single AUX while
// the phone remote listed all three and switching between them worked (#274).
// Services must NOT be split this way: their account is a hashed identifier or
// a "...UserName" placeholder and would produce one pill per stored login.
function physicalInputAccount(src) {
  const acc = String((src && src.sourceAccount) || '').trim();
  if (!acc) return '';
  if (/^ACCT#/i.test(acc) || /UserName$/i.test(acc)) return '';
  // A STORED_MUSIC account is the media server's UUID (e.g.
  // 00113251-28ed-...-1100/0), a service identifier, not a physical socket.
  // Left in, it printed one raw "STORED_MUSIC (uuid/0)" pill per server;
  // suppress it so those collapse into one clean "Media server" pill (the
  // Music library section below names the individual servers).
  if (/^[0-9a-f]{8}-[0-9a-f]{4}-/i.test(acc)) return '';
  return acc;
}

function rollupSources(raw) {
  const grouped = {};
  for (const src of raw) {
    if (!src || !src.source) continue;
    if (src.source === 'NOTIFICATION') continue;
    // Alexa on the SoundTouch ran entirely through the Bose/Amazon cloud, which
    // is gone, so the box still advertises ALEXA as READY but it cannot work.
    // Showing it as "active" only confuses users (Discussion #170), so hide the
    // dead source like NOTIFICATION rather than imply it is usable.
    if (src.source === 'ALEXA') continue;
    const acc = physicalInputAccount(src);
    const key = acc ? src.source + '|' + acc : src.source;
    const existing = grouped[key];
    if (!existing || (src.status === 'READY' && existing.status !== 'READY')) {
      grouped[key] = src;
    }
  }
  return Object.values(grouped).sort((a, b) => {
    if (a.status === 'READY' && b.status !== 'READY') return -1;
    if (a.status !== 'READY' && b.status === 'READY') return 1;
    return sourceLabel(a.source).localeCompare(sourceLabel(b.source));
  });
}

// The safe-haven view: what OTHER tools have done to this speaker.
//
// Users try several tools and keep trying them, and afterwards nobody can tell
// which one caused which effect. STR is the only thing in that picture that can
// look at the speaker and say so, so it shows every trace it can see and offers
// to take back the ones it can.
//
// Hidden when there is nothing, which is the normal case. A section that is
// always there, always empty, is a section nobody reads on the day it matters.
export async function refreshForeignInfluence(box) {
  const section = document.getElementById('foreignSection');
  const list = document.getElementById('foreignList');
  if (!section || !list) return;
  section.hidden = true;
  list.innerHTML = '';
  if (!box || box.kind === 'stock') return;
  let data;
  try {
    data = await BoxForeignInfluence(box.host, box.port);
  } catch (e) {
    // An older agent has no such route. Not worth a message: the section is
    // simply not there.
    return;
  }
  const findings = (data && data.findings) || [];
  if (!findings.length) return;
  findings.forEach((f) => list.appendChild(foreignRow(box, f)));
  section.hidden = false;
}

// foreignRow renders one finding. Every row says what it is and what STR can
// do about it, including the rows where the answer is "nothing, and here is
// why": a row with no explanation reads as a broken button.
function foreignRow(box, f) {
  const row = document.createElement('div');
  row.className = 'setting-row';
  row.style.flexDirection = 'column';
  row.style.alignItems = 'flex-start';

  const head = document.createElement('div');
  const tool = f.tool || t('settingsView.foreignUnknownTool');
  head.innerHTML = `<b>${escapeHtml(tool)}</b> &middot; `
    + escapeHtml(t(f.active ? 'settingsView.foreignActive' : 'settingsView.foreignLeftover'));
  row.appendChild(head);

  if (f.detail) {
    const d = document.createElement('div');
    d.className = 'muted small';
    d.textContent = f.detail;
    row.appendChild(d);
  }
  // The explanation for a row STR cannot act on, straight from the speaker.
  if (f.note) {
    const n = document.createElement('div');
    n.className = 'muted small';
    n.textContent = f.note;
    row.appendChild(n);
  }
  if (f.undo && f.undo !== 'none') {
    const b = document.createElement('button');
    b.className = 'btn btn-mini';
    b.style.marginTop = '6px';
    b.textContent = t('settingsView.foreignUndoBtn');
    b.addEventListener('click', () => undoForeign(box, f, b));
    row.appendChild(b);
  }
  return row;
}

async function undoForeign(box, f, btn) {
  const out = document.getElementById('foreignResult');
  const say = (msg) => { if (out) out.textContent = msg || ''; };
  // A restart is the cure for the runtime shape, so say so before doing it,
  // and say the thing that matters more: while the other app runs it sets its
  // address again the moment the speaker comes back.
  if (f.undo === 'reboot' || f.undo === 'rewrite') {
    const ok = await confirmFor(box, 
      t('speaker.cloudRestoreTitle'),
      `<p>${escapeHtml(t('speaker.cloudRestoreBody', { name: getBoxLabel(box), n: 1 }))}</p>`
      + `<p><b>${escapeHtml(t('speaker.cloudRestoreAppWarning'))}</b></p>`,
    );
    if (!ok) return;
  }
  btn.disabled = true;
  say(t('settingsView.foreignWorking'));
  try {
    const res = await UndoForeignFinding(box.host, box.port, f.id);
    if (res && res.status === 'use-endpoint') {
      // The older, more careful removal owns this one. Point at it rather
      // than doing half of it here.
      say(t('settingsView.foreignUseActions'));
      return;
    }
    if (res && res.rebootRequired) {
      await RebootBox(box.host, box.port);
      say(t('settingsView.foreignRestarting', { name: getBoxLabel(box) }));
      setTimeout(() => refreshForeignInfluence(box).catch(() => {}), 90000);
      return;
    }
    // Never claim more than happened: the speaker reports what is still left
    // and that is what the user is told.
    const remaining = (res && res.remaining) || '';
    say(remaining
      ? t('settingsView.foreignRemaining', { detail: remaining })
      : t('settingsView.foreignClean'));
    refreshForeignInfluence(box).catch(() => {});
  } catch (e) {
    say(String(e));
  } finally {
    btn.disabled = false;
  }
}

// The separate centre and surround levels of a home theater system.
//
// A SoundTouch 300 owner with Virtually Invisible 300 surrounds on two bars
// wrote on 2026-09-25 that the surrounds can only be turned up together with
// the bar. That was true of STR: the surrounds hang off the bar's own wireless
// link and never appear on the network, so the only thing to address is the
// bar. The bar has the knob though, and the agent now reads it.
//
// Hidden on every ordinary speaker, and that is the common case: supported is
// false unless the speaker itself advertises the capability. Each level is
// shown on its own, because a bar with surrounds but no separate centre
// reports only the one.
export async function refreshBoxLevelRows(box) {
  const section = document.getElementById('boxLevelsSection');
  if (!section) return;
  section.hidden = true;
  if (!box || box.kind === 'stock') return;
  let lv;
  try {
    lv = await BoxSpeakerLevels(box.host, box.port);
  } catch (e) {
    // An older agent has no such route. Not an error worth a message: the
    // section simply is not there.
    return;
  }
  if (!lv || !lv.supported) return;
  const any = wireLevelRow(box, 'frontCenter', lv.frontCenter, 'boxLevelCentre')
    | wireLevelRow(box, 'rearSurrounds', lv.rearSurrounds, 'boxLevelSurround');
  section.hidden = !any;
}

// wireLevelRow paints and wires one level, and reports whether it was shown.
function wireLevelRow(box, level, info, idBase) {
  const row = document.getElementById(idBase + 'Row');
  const slider = document.getElementById(idBase);
  const label = document.getElementById(idBase + 'Val');
  if (!row || !slider || !label) return 0;
  if (!info || !info.available) { row.hidden = true; return 0; }
  // Bounds and granularity from the speaker, never a constant: the firmware
  // refuses a write off its own grid.
  slider.min = String(info.min);
  slider.max = String(info.max);
  slider.step = String(info.step || 1);
  slider.value = String(info.value);
  label.textContent = formatRel(info.value);
  row.hidden = false;
  // While dragging only the label moves; the write goes out on release. One
  // write per pixel of travel would put a queue of them on the speaker.
  slider.oninput = () => { label.textContent = formatRel(slider.value); };
  slider.onchange = () => applySpeakerLevel(box, level, parseInt(slider.value, 10), slider, label, info);
  return 1;
}

// applySpeakerLevel writes one level and puts the value back if it was refused,
// so the slider never sits at a number the speaker does not hold.
async function applySpeakerLevel(box, level, value, slider, label, info) {
  const note = document.getElementById('boxLevelsNote');
  const noteRow = document.getElementById('boxLevelsNoteRow');
  const say = (msg) => {
    if (!note || !noteRow) return;
    note.textContent = msg || '';
    noteRow.hidden = !msg;
  };
  say('');
  try {
    await SetBoxSpeakerLevel(box.host, box.port, level, value);
  } catch (e) {
    slider.value = String(info.value);
    label.textContent = formatRel(info.value);
    say(t('settingsView.levelsFailed'));
    return;
  }
  info.value = value;
}

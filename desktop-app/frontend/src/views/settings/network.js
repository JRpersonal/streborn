// views/settings/network.js: the Network group of Speaker settings. The
// network the speaker is on with the switch form and the stored networks, and
// the radio search region.

import { state, saveSearchCountry } from '../../state.js';
import { $, escapeHtml, escapeAttr, showToast } from '../../utils.js';
import { t } from '../../i18n/index.js';
import { COUNTRIES, optFlag } from '../../localization.js';
import { storedNetworksView, storedNetworksHtml, forgetRefusalKey, forgetAllRefusalKey } from '../../wlanforget.js';
import {
  ListWiFiProfiles,
  BoxWifiScan,
  TryWiFiPassword,
  CurrentWiFi,
  KeepSpeakerWiFiCopy,
  StoreSpeakerWiFiCopy,
  boxFetch,
} from '../../api.js';
import { keepWifiCopy, noteWifiCopyResult, paintWifiCopyNote } from '../../wificopy.js';
import { deps, withButtonPending, revealInSettings, confirmFor, reportError, reportDone, reportFieldError, clearFieldErrors } from './shared.js';

// isMacOS is re-derived locally (the same pure check main.js uses) so the WLAN
// switch UI can skip the System-keychain admin prompt on macOS (#88) without
// expanding the injected-helper surface.
const isMacOS = typeof navigator !== 'undefined' && /Mac OS X|Macintosh/.test(navigator.userAgent);

export function renderNetworkSections(ctx) {
  const { nv } = ctx;
  const signalLabel = {
    'EXCELLENT_SIGNAL': t('signal.excellent'),
    'GOOD_SIGNAL': t('signal.good'),
    'MARGINAL_SIGNAL': t('signal.marginal'),
    'POOR_SIGNAL': t('signal.poor'),
    'NO_SIGNAL': t('signal.none'),
  };
  // A coprocessor speaker that names its network but no signal class: say so
  // instead of a bare "-" that reads like a failed reading (issue #90).
  const signalText = signalLabel[nv.signal] || nv.signal ||
    (nv.coprocessor ? t('signal.notReported') : '-');
  const kvRow = (key, val) =>
    `<div class="kv-row"><span class="kv-key">${escapeHtml(t(key))}</span><span class="kv-val">${escapeHtml(val)}</span></div>`;
  let netRows = '';
  switch (nv.kind) {
    case 'wired':
      netRows = kvRow('settingsView.wlanIp', nv.ip || '-')
        + `<div class="muted small">${escapeHtml(t('settingsView.wlanWired'))}</div>`;
      break;
    case 'wifi':
    case 'wifi-unnamed':
      netRows = kvRow('settingsView.wlanSsid', nv.ssid || t('settingsView.wlanSsidUnknown'))
        + kvRow('settingsView.wlanIp', nv.ip || '-')
        + kvRow('settingsView.wlanSignal', signalText)
        + (nv.frequencyKHz ? kvRow('settingsView.wlanFrequency', `${(nv.frequencyKHz / 1000).toFixed(0)} MHz`) : '');
      break;
    case 'unknown-link':
      netRows = kvRow('settingsView.netConnection', t('settingsView.netConnUnknown'))
        + kvRow('settingsView.wlanIp', nv.ip || '-');
      break;
    default:
      netRows = `<div class="muted small">${escapeHtml(t('settingsView.wlanNotConnected'))}</div>`;
  }
  if (nv.strSSID) {
    netRows += `<div class="muted small">${escapeHtml(t('settingsView.wlanStrConfigured', { ssid: nv.strSSID }))}</div>`;
  }
  const netHeadingKey = (nv.kind === 'wired' || nv.kind === 'unknown-link') ? 'settingsView.netHeading' : 'settingsView.wlanHeading';
  // STR has no copy of this speaker's Wi-Fi and could not keep one by itself
  // (wificopy.js). A quiet line with one sentence on what the copy is for,
  // never a banner: the speaker works without it. Rendered hidden and shown by
  // paintWifiCopyNote, which also runs when an attempt finishes later.
  const host = (ctx.box && ctx.box.host) || '';
  const copyNote = host
    ? `<div id="wlanCopyNote" class="muted small" data-host="${escapeAttr(host)}" style="margin-top:8px" hidden>
        <span>${escapeHtml(t('settingsView.wlanCopyNote'))}</span>
        <button class="btn btn-mini" id="wlanCopyBtn" style="margin-left:6px">${escapeHtml(t('settingsView.wlanCopySaveBtn'))}</button>
      </div>`
    : '';
  return {
    group: 'network',
    html: `
    <div data-ui2-sec="wifi" class="settings-section">
      <h3>${escapeHtml(t(netHeadingKey))}</h3>
      ${netRows}
      ${copyNote}
      <small class="muted small" style="display:block;margin-top:8px">${escapeHtml(t('settingsView.wlanSwitchHint'))}</small>
      <button class="btn" id="wlanSwitchToggle" style="margin-top:8px">${escapeHtml(t('settingsView.wlanSwitchToggle'))}</button>
      <div id="wlanSwitchForm" class="hidden" style="margin-top:8px">
        <div id="wlanStoreHint" class="muted small" style="margin-bottom:6px" hidden>${escapeHtml(t('settingsView.wlanStoreHint'))}</div>
        <div class="wlan-row">
          <select id="boxWlanSelect"><option value="">${escapeHtml(t('settingsView.wlanPickPlaceholder'))}</option></select>
          <button class="btn btn-icon-sm" id="boxWlanRefresh" title="${escapeAttr(t('settingsView.wlanRefreshTitle'))}">&#x21bb;</button>
        </div>
        <input type="text" id="boxWlanSSID" placeholder="${escapeAttr(t('settingsView.wlanSsidPlaceholder'))}" />
        <label style="display:block;margin:4px 0 0"><input type="checkbox" id="boxWlanHidden" /> ${escapeHtml(t('settingsView.wlanHiddenToggle'))}</label>
        <small class="muted small" style="display:block;margin-bottom:4px">${escapeHtml(t('settingsView.wlanHiddenHint'))}</small>
        <div class="wlan-row">
          <input type="password" id="boxWlanPass" placeholder="${escapeAttr(t('settingsView.wlanPassPlaceholder'))}" />
          <button class="btn btn-icon-sm" id="boxWlanShowPass" title="${escapeAttr(t('settingsView.wlanShowPass'))}">&#128065;</button>
        </div>
        <div id="boxWlanForgetOthersRow">
          <label style="display:block;margin:4px 0 0"><input type="checkbox" id="boxWlanForgetOthers" checked /> ${escapeHtml(t('settingsView.wlanForgetOthersToggle'))}</label>
          <small class="muted small" style="display:block;margin-bottom:4px">${escapeHtml(t('settingsView.wlanForgetOthersToggleHint'))}</small>
        </div>
        <button class="btn btn-danger btn-mini" id="boxWlanSave">${escapeHtml(t('settingsView.wlanSaveBtn'))}</button>
        <small class="muted small">${escapeHtml(t('settingsView.wlanWarn'))}</small>
      </div>
      <div id="wlanStoredBlock" class="wlan-stored" style="margin-top:12px">
        <div class="kv-key">${escapeHtml(t('settingsView.wlanStoredHeading'))}</div>
        <ul id="wlanStoredList" class="wlan-stored-list"><li class="muted small">${escapeHtml(t('settingsView.wlanStoredLoading'))}</li></ul>
        <button class="btn btn-mini" id="wlanForgetOthersBtn" hidden>${escapeHtml(t('settingsView.wlanForgetOthersBtn'))}</button>
        <small class="muted small" id="wlanForgetNote" style="display:block"></small>
        <div id="wlanForgetAllRow" hidden style="margin-top:8px">
          <small class="muted small" style="display:block;margin-bottom:4px">${escapeHtml(t('settingsView.wlanForgetAllHint'))}</small>
          <button class="btn btn-danger btn-mini" id="wlanForgetAllBtn">${escapeHtml(t('settingsView.wlanForgetAllBtn'))}</button>
        </div>
      </div>
    </div>

    <div data-ui2-sec="region" class="settings-section">
      <h3>${escapeHtml(t('settingsView.regionHeading'))}</h3>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.regionCurrent'))}</span><span class="kv-val" id="currentAppRegion">${escapeHtml(t('common.loading'))}</span></div>
      <div class="setting-row">
        <select id="appRegionSelect"></select>
        <button class="btn btn-mini" id="appRegionSave">${escapeHtml(t('common.save'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.regionHelp'))}</small>
    </div>
  `,
  };
}

export function wireNetworkSections(ctx) {
  const { box, nv } = ctx;

  // Wire the WLAN switch UI: collapsible form, PC-known SSIDs in
  // a dropdown with auto-password prefill, Save sends
  // PUT /api/box/wlan.
  wireWlanSwitch(box, nv.kind === 'wifi' ? nv.ssid : '');
  // The stored networks and "Forget the other networks" (wlanforget.js).
  wireWlanStored(box);

  // App Region dropdown fuellen + aktuelle Region selektieren
  const regSel = $('appRegionSelect');
  if (regSel) {
    regSel.innerHTML = COUNTRIES.filter(c => c.cc).map(c =>
      `<option value="${c.cc}">${optFlag(c.cc)}${escapeHtml(c.name)}</option>`
    ).join('');
    const updateCurrentDisplay = (cc) => {
      const el = $('currentAppRegion');
      if (!el) return;
      const c = COUNTRIES.find(x => x.cc === cc);
      el.innerHTML = c
        ? `${optFlag(cc)}${escapeHtml(c.name)} (${escapeHtml(cc)})`
        : escapeHtml(cc || t('common.unknown'));
    };
    boxFetch(box, '/api/region').then(r => r.ok ? r.json() : null).then(data => {
      if (data && data.country) {
        regSel.value = data.country;
        updateCurrentDisplay(data.country);
      } else {
        const el = $('currentAppRegion'); if (el) el.textContent = t('settingsView.langUnreachable');
      }
    }).catch(() => { const el = $('currentAppRegion'); if (el) el.textContent = t('settingsView.langUnreachable'); });
    $('appRegionSave').onclick = () => withButtonPending($('appRegionSave'), async () => {
      const cc = regSel.value;
      try {
        const r = await boxFetch(box, '/api/region', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ country: cc }),
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        const data = await r.json();
        state.searchCountry = data.country;
        state.searchLang = data.language;
        saveSearchCountry(state.searchCountry);
        const cs = $('searchCountry');
        if (cs) cs.value = data.country;
        deps.updateFilterIndicators();
        updateCurrentDisplay(data.country);
        reportDone('appRegionSave', t('settingsView.regionSavedToast', { name: (COUNTRIES.find(c => c.cc === cc) || {}).name || cc }));
      } catch (e) { reportError('appRegionSave', e); }
    });
  }
}

// wireWlanSwitch wires the WLAN switch UI in the Settings tab.
// Lists PC-known WLANs in a dropdown (no manual typing), prefills
// the password, and on Save sends PUT /api/box/wlan.
function wireWlanSwitch(box, currentSsid) {
  const toggle = $('wlanSwitchToggle');
  const form = $('wlanSwitchForm');
  if (!toggle || !form) return;
  // storeMode: the form was opened from the "no copy of the Wi-Fi" note, to
  // give STR a copy of the network the speaker is on, not to move it. Save
  // then writes the copy store-only (StoreSpeakerWiFiCopy) and the speaker's
  // Wi-Fi is not touched. Only an agent too old for that path falls back to
  // the ordinary save, with its own confirmation.
  let storeMode = false;
  // setStoreMode switches the form between the two jobs: the hint line and
  // the Save button's label say which one it does.
  const setStoreMode = (on) => {
    storeMode = !!on;
    const hint = $('wlanStoreHint');
    if (hint) hint.hidden = !storeMode;
    const save = $('boxWlanSave');
    if (save) save.textContent = t(storeMode ? 'settingsView.wlanCopySaveBtn' : 'settingsView.wlanSaveBtn');
  };
  // The note's button. First the same automatic copy the app tries by
  // itself (the computer may be on the speaker's Wi-Fi by now); when that
  // cannot work, the form opens on the current network in store mode. Since
  // 2026-11-15 this replaces the "Save Wi-Fi" banner, whose button led into
  // the switch form, read like a required action, and restarted the speaker.
  const copyBtn = $('wlanCopyBtn');
  if (copyBtn && box && box.host) {
    copyBtn.onclick = () => withButtonPending(copyBtn, async () => {
      const rec = await keepWifiCopy(box, 'button', KeepSpeakerWiFiCopy);
      if (rec && rec.stored) {
        paintWifiCopyNote();
        reportDone('wlanCopyBtn', t('settingsView.wlanCopySaved'), 7000);
        return;
      }
      await openForStore();
    });
  }
  paintWifiCopyNote();
  async function openForStore() {
    setStoreMode(true);
    form.classList.remove('hidden');
    // BCO chassis do not report the SSID, so the computer's own network is the
    // best guess there: the speaker was set up from it in nearly every case.
    let ssid = currentSsid || '';
    if (!ssid) { try { ssid = await CurrentWiFi() || ''; } catch {} }
    if (ssid && $('boxWlanSSID')) $('boxWlanSSID').value = ssid;
    if (ssid && !isMacOS) {
      try {
        const pw = await TryWiFiPassword(ssid);
        if (pw && $('boxWlanPass')) $('boxWlanPass').value = pw;
      } catch {}
    }
    // The Network section and its group start folded up: open every <details>
    // around the form, or the banner lands on a closed list (mail 2026-10-20).
    revealInSettings(form);
    const pass = $('boxWlanPass');
    if (pass && !pass.value) pass.focus();
  }
  toggle.onclick = () => {
    // The toggle is the switch form: moving the speaker, never the copy.
    setStoreMode(false);
    form.classList.toggle('hidden');
    if (!form.classList.contains('hidden')) {
      loadBoxWlanList();
    }
  };
  // Ask the SPEAKER what it can see, not the computer.
  //
  // These are different lists, and only the speaker's decides whether a switch
  // can work. Offering the computer's known networks meant a user picked one,
  // committed, and only then learned the speaker could not see it. A network
  // the speaker's own radio cannot pick up is simply not in its list, so the
  // switch is refused on that fact alone, with no claim about why.
  //
  // The computer's list stays as the fallback for a speaker that cannot
  // survey, and typing a name by hand still works for a hidden network.
  async function loadBoxWlanList() {
    const sel = $('boxWlanSelect');
    const rb = $('boxWlanRefresh');
    if (rb) rb.classList.add('spinning'); // visible feedback: the button spins while loading
    const fill = (names) => {
      sel.innerHTML = `<option value="">${escapeHtml(t('settingsView.wlanPickPlaceholder'))}</option>` +
        names.map(n => `<option value="${escapeAttr(n)}">${escapeHtml(n)}</option>`).join('');
    };
    try {
      const box = state.settingsBox;
      let seen = [];
      if (box && box.host) {
        // The survey holds the radio for about five seconds, which is why this
        // is tied to the refresh button rather than run on its own.
        try { seen = await BoxWifiScan(box.host, box.port) || []; } catch { seen = []; }
      }
      if (seen.length) {
        fill(seen);
        showToast(t('settingsView.wlanListFromSpeaker', { n: seen.length }));
        return;
      }
      const profiles = await ListWiFiProfiles() || [];
      fill(profiles.map(p => p.ssid));
      showToast(t('settingsView.wlanListRefreshed', { n: profiles.length }));
    } catch {
      sel.innerHTML = `<option value="">${escapeHtml(t('setup.wlanListUnavailable'))}</option>`;
    } finally {
      if (rb) rb.classList.remove('spinning');
    }
  }
  $('boxWlanRefresh').onclick = loadBoxWlanList;
  $('boxWlanSelect').onchange = async () => {
    const v = $('boxWlanSelect').value;
    if (!v) return;
    $('boxWlanSSID').value = v;
    if (isMacOS) return; // #88: don't trigger System-keychain admin prompt
    try {
      const pw = await TryWiFiPassword(v);
      if (pw) $('boxWlanPass').value = pw;
    } catch {}
  };
  $('boxWlanShowPass').onclick = () => {
    const i = $('boxWlanPass');
    const b = $('boxWlanShowPass');
    if (i.type === 'password') { i.type = 'text'; b.innerHTML = '&#128064;'; }
    else { i.type = 'password'; b.innerHTML = '&#128065;'; }
  };
  // saveCopyOnly writes the owner's network as STR's copy, store-only, and
  // reports whether it handled the save. false means the agent is too old for
  // the store-only path (or this build lacks the binding): the caller then
  // runs the ordinary save.
  async function saveCopyOnly(ssid, pass) {
    let res;
    try {
      res = await StoreSpeakerWiFiCopy(box.host, box.port || 0, ssid, pass);
    } catch {
      return false;
    }
    if (!res || res.reason === 'agent-too-old') return false;
    noteWifiCopyResult(box.host, res);
    if (res.stored) {
      for (const b of state.boxes || []) { if (b && b.host === box.host) b.wlanCredsMissing = false; }
      box.wlanCredsMissing = false;
      $('boxWlanPass').value = '';
      form.classList.add('hidden');
      storeMode = false;
      paintWifiCopyNote();
      reportDone('boxWlanSave', t('settingsView.wlanCopySaved'), 7000);
      return true;
    }
    const why = String(res.reason || '');
    if (why === 'refused:other-network') {
      reportFieldError('boxWlanSSID', t('settingsView.wlanCopyOtherNetwork'));
    } else if (why === 'no-password' || why === 'password-unusable') {
      reportFieldError('boxWlanPass', t('settingsView.wlanCopyBadPassword'));
    } else {
      reportError('boxWlanSave', t('settingsView.wlanCopyFailed', { reason: why || '?' }));
    }
    return true;
  }
  $('boxWlanSave').onclick = () => withButtonPending($('boxWlanSave'), async () => {
    const ssid = $('boxWlanSSID').value.trim();
    const pass = $('boxWlanPass').value;
    // Hidden networks never appear in the speaker's site survey, so the flag
    // also implies force: the agent skips the visibility preflight for them.
    const hidden = !!($('boxWlanHidden') && $('boxWlanHidden').checked);
    // Default ON: a speaker that keeps its old networks falls back to them,
    // public hotspots included (field case 2026-10-14).
    const forgetOthers = !$('boxWlanForgetOthers') || $('boxWlanForgetOthers').checked;
    clearFieldErrors('boxWlanSSID', 'boxWlanPass');
    if (!ssid) { reportFieldError('boxWlanSSID', t('settingsView.wlanSsidEmpty')); return; }
    if (storeMode && await saveCopyOnly(ssid, pass)) return;
    const ok = await confirmFor(box,
      t('settingsView.wlanSwitchConfirmTitle'),
      t('settingsView.wlanConfirmBody', { ssid: escapeHtml(ssid) })
    );
    if (!ok) return;
    const putWlan = (force) => boxFetch(box, '/api/box/wlan', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ssid, password: pass, hidden, force: hidden || force, forgetOthers }),
    });
    try {
      let r = await putWlan(false);
      if (!r.ok) {
        const body = await r.text();
        // The agent's visibility preflight (422 ssid-not-visible) refused the
        // switch because the speaker's own scan cannot see the target SSID.
        // Surface it and offer a localized "switch anyway" that re-PUTs with
        // force:true instead of surfacing the raw JSON error.
        let refuse = null;
        if (r.status === 422) { try { refuse = JSON.parse(body); } catch {} }
        if (refuse && refuse.code === 'ssid-not-visible') {
          const visible = (Array.isArray(refuse.visible) ? refuse.visible : []).filter(Boolean);
          const goAnyway = await confirmFor(box, 
            t('settingsView.wlanNotVisibleTitle'),
            t('settingsView.wlanNotVisibleBody', {
              ssid: escapeHtml(ssid),
              visible: escapeHtml(visible.join(', ') || '-'),
            }),
            { confirmLabel: t('settingsView.wlanForceBtn') },
          );
          if (!goAnyway) return;
          r = await putWlan(true);
          if (!r.ok) throw new Error('HTTP ' + r.status + ': ' + await r.text());
        } else {
          throw new Error('HTTP ' + r.status + ': ' + body);
        }
      }
      // The agent applies the switch in the background and the box leaves the
      // current network, so we rediscover it rather than wait. BCO speakers
      // (Portable) reboot to apply, which takes a few minutes; wpa speakers
      // switch live and fall back to their old network if the new one fails.
      let info = {};
      try { info = await r.json(); } catch {}
      $('boxWlanPass').value = '';
      form.classList.add('hidden');
      // A speaker on a network cable keeps its Wi-Fi radio off while the
      // cable is in, so the agent stored the network instead of switching
      // (#1307). Nothing moves until the owner unplugs the cable and restarts
      // the speaker; the speaker stays reachable over the cable meanwhile.
      if (info.wiredUplink) {
        reportDone('boxWlanSave', t('settingsView.wlanWiredStoredToast', { ssid }), 20000);
        return;
      }
      reportDone('boxWlanSave',
        info.mechanism === 'bco'
          ? t('settingsView.wlanRebootingToast')
          : t('settingsView.wlanSwitchedToast'),
        7000,
      );
      // The speaker gets a new IP (or reboots). Retrigger discovery; a longer
      // delay for the BCO reboot so the rediscover lands after it is back up.
      // A forget that removed something restarts the speaker once, so it
      // needs the same long wait as the BCO reboot.
      setTimeout(deps.discoverBoxes, (info.mechanism === 'bco' || info.forgetOthers) ? 90000 : 12000);
    } catch (e) { reportError('boxWlanSave', e); }
  });
}

// wireWlanStored lists the speaker's stored networks with the current one
// marked, and offers "Forget the other networks" when the agent says it can.
// An agent without the endpoint answers 404 and the block says so.
function wireWlanStored(box) {
  const list = $('wlanStoredList');
  const btn = $('wlanForgetOthersBtn');
  const note = $('wlanForgetNote');
  if (!list || !btn || !note || !box || !box.host) return;
  let view = null;
  const allRow = $('wlanForgetAllRow');
  const allBtn = $('wlanForgetAllBtn');
  const load = async () => {
    try {
      const r = await boxFetch(box, '/api/box/wlan/networks', {}, 15000);
      if (!r.ok) throw new Error('HTTP ' + r.status);
      view = storedNetworksView(await r.json());
    } catch {
      list.innerHTML = `<li class="muted small">${escapeHtml(t('settingsView.wlanStoredUnavailable'))}</li>`;
      btn.hidden = true;
      note.textContent = '';
      if (allRow) allRow.hidden = true;
      return;
    }
    list.innerHTML = storedNetworksHtml(view, t, escapeHtml);
    btn.hidden = !view.canForget;
    // The change form's "forget the others" option does nothing on a
    // chassis that cannot forget, so it is not offered there.
    const forgetRow = $('boxWlanForgetOthersRow');
    const forgetBox = $('boxWlanForgetOthers');
    if (forgetRow) forgetRow.hidden = !view.forgetSupported;
    if (forgetBox && !view.forgetSupported) forgetBox.checked = false;
    // A cabled speaker gets "Forget Wi-Fi" instead, so the forget-others
    // refusal ("only works while it is on Wi-Fi") would contradict it.
    note.textContent = view.refusalKey && view.networks.length > 1 && !view.canForgetAll
      ? t(view.refusalKey)
      : '';
    if (allRow) allRow.hidden = !view.canForgetAll;
  };
  // "Forget Wi-Fi" (#969): a speaker on a network cable drops every stored
  // Wi-Fi network. The agent refuses unless the speaker is cabled AND this
  // request reached it over the cable, so the button can never cut it off.
  if (allBtn) allBtn.onclick = () => withButtonPending(allBtn, async () => {
    if (!view || !view.canForgetAll) return;
    const speaker = box.friendlyName || box.name || box.host;
    const ok = await confirmFor(box,
      t('settingsView.wlanForgetAllConfirmTitle'),
      t('settingsView.wlanForgetAllConfirmBody', { speaker: '<b>' + escapeHtml(speaker) + '</b>' }),
    );
    if (!ok) return;
    try {
      const r = await boxFetch(box, '/api/box/wlan', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reboot: true }),
      }, 30000);
      let res = {};
      try { res = await r.json(); } catch {}
      if (!r.ok || res.status !== 'ok') {
        const key = forgetAllRefusalKey(res.code);
        reportError(allBtn, key ? t(key) : t('settingsView.wlanForgetAllFailed', { reason: res.reason || res.error || ('HTTP ' + r.status) }));
        await load();
        return;
      }
      if (allRow) allRow.hidden = true;
      if (res.rebooting) {
        reportDone(allBtn, t('settingsView.wlanForgetAllDone'), 9000);
        setTimeout(deps.discoverBoxes, 90000);
      } else {
        reportDone(allBtn, t('settingsView.wlanForgetAllDoneNoRestart'), 7000);
        await load();
      }
    } catch (e) { reportError(allBtn, e); }
  });
  btn.onclick = () => withButtonPending(btn, async () => {
    if (!view || !view.canForget) return;
    const speaker = box.friendlyName || box.name || box.host;
    const ok = await confirmFor(box, 
      t('settingsView.wlanForgetConfirmTitle'),
      t('settingsView.wlanForgetConfirmBody', {
        speaker: '<b>' + escapeHtml(speaker) + '</b>',
        network: '<b>' + escapeHtml(view.keep) + '</b>',
        count: view.removable,
      }),
    );
    if (!ok) return;
    try {
      const r = await boxFetch(box, '/api/box/wlan/forget-others', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reboot: true }),
      }, 30000);
      let res = {};
      try { res = await r.json(); } catch {}
      if (!r.ok || res.status !== 'ok') {
        const key = forgetRefusalKey(res.code);
        reportError(btn, key ? t(key) : t('settingsView.wlanForgetFailed', { reason: res.reason || res.error || ('HTTP ' + r.status) }));
        await load();
        return;
      }
      if (!res.removed) {
        reportDone(btn, t('settingsView.wlanForgetNothing', { network: view.keep }), 7000);
        await load();
        return;
      }
      reportDone(btn, t('settingsView.wlanForgetDone', { count: res.removed, network: view.keep }), 9000);
      btn.hidden = true;
      if (res.rebooting) setTimeout(deps.discoverBoxes, 90000);
      else await load();
    } catch (e) { reportError(btn, e); }
  });
  load();
}

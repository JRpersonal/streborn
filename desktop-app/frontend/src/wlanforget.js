// Stored Wi-Fi networks of a speaker, and forgetting the ones it should no
// longer join. The decisions live here, away from the DOM, so vitest covers
// them; settings.js only wires the buttons.
//
// Field case 2026-10-14: speakers with two to four stored networks, one of
// them a public hotspot, kept falling back to the hotspot whenever the home
// network blinked. The agent's GET /api/box/wlan/networks lists what the
// speaker has stored, POST /api/box/wlan/forget-others removes all but the
// current one (internal/webui/wlanforget.go).

// forgetRefusalKey maps an agent refusal code to its sentence. Unknown codes
// fall back to the generic failure line with the agent's own reason.
export function forgetRefusalKey(code) {
  switch (code) {
    case 'not-associated': return 'settingsView.wlanForgetRefused.notAssociated';
    case 'ethernet': return 'settingsView.wlanForgetRefused.ethernet';
    case 'target-mismatch': return 'settingsView.wlanForgetRefused.mismatch';
    case 'on-hotspot': return 'settingsView.wlanForgetRefused.onHotspot';
    case 'keep-not-stored':
    case 'would-empty': return 'settingsView.wlanForgetRefused.keepNotStored';
    case 'chassis-unsupported': return 'settingsView.wlanForgetRefused.chassis';
    default: return '';
  }
}

// storedNetworksView turns the agent's answer into what the settings block
// shows: the list (current network first), whether the button is offered,
// the network that stays, how many would go, and a note when it is not.
export function storedNetworksView(resp) {
  const nets = Array.isArray(resp && resp.networks) ? resp.networks.filter((n) => n && n.ssid) : [];
  const sorted = [...nets].sort((a, b) => (b.current ? 1 : 0) - (a.current ? 1 : 0));
  const fo = (resp && resp.forgetOthers) || {};
  const current = sorted.find((n) => n.current);
  const removable = Number(fo.removable) || 0;
  const code = fo.code || '';
  return {
    networks: sorted,
    // Only the wpa chassis can forget a network today. The BCO chassis
    // (Portable, scm ST30, spotty ST20) keeps its list in BoseApp's own
    // settings, where STR cannot remove a single entry yet, so the "forget
    // the others" option of the Wi-Fi change form would do nothing there.
    forgetSupported: !resp || !resp.mechanism || resp.mechanism === 'wpa',
    keep: current ? current.ssid : (resp && resp.current) || '',
    removable,
    canForget: !code && removable > 0 && !!current,
    refusalKey: code ? forgetRefusalKey(code) : '',
    refusalReason: fo.reason || '',
  };
}

// storedNetworksHtml renders the list. esc is the HTML escaper and t the
// translator, handed in so this stays a pure function.
export function storedNetworksHtml(view, t, esc) {
  if (!view.networks.length) {
    return `<li class="muted small">${esc(t('settingsView.wlanStoredNone'))}</li>`;
  }
  return view.networks.map((n) => {
    const badges = [];
    if (n.current) badges.push(`<span class="wlan-badge wlan-badge-current">${esc(t('settingsView.wlanStoredCurrent'))}</span>`);
    if (n.publicHotspot) badges.push(`<span class="wlan-badge wlan-badge-warn">${esc(t('settingsView.wlanStoredHotspot'))}</span>`);
    else if (n.open) badges.push(`<span class="wlan-badge wlan-badge-warn">${esc(t('settingsView.wlanStoredOpen'))}</span>`);
    return `<li class="wlan-stored-item${n.current ? ' is-current' : ''}"><span class="wlan-stored-name">${esc(n.ssid)}</span>${badges.join('')}</li>`;
  }).join('');
}

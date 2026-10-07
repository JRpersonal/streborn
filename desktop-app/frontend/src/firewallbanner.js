// The "Windows Firewall blocks ST Reborn" banner.
//
// Cancelling the Windows first-run firewall prompt makes Windows write inbound
// block rules for the app and never ask again, so the speaker list stays empty
// with nothing to say why. The backend reads the rules (CheckFirewall) and can
// replace them in one administrator step (AllowThroughFirewall); these helpers
// decide what the banner and the toast say, so that can be tested without a DOM.

// firewallBannerView returns null when no banner belongs on screen, otherwise
// the texts and buttons. Only a check that actually FOUND a block shows it: an
// unreadable rule list, macOS/Linux (supported false) or a session dismissal
// keep it down, because sending somebody to change a firewall that is not the
// problem costs them more than a missing hint.
export function firewallBannerView(status, t, { dismissed = false } = {}) {
  if (dismissed || !status || !status.supported || !status.checked || !status.blocked) return null;
  const view = {
    title: t('firewall.bannerTitle'),
    text: t('firewall.bannerText'),
    note: '',
    primary: { label: t('firewall.allowButton'), includePublic: false },
    secondary: null,
  };
  // On a network Windows marks Public the allow rule has to cover Public too,
  // or the speakers stay out of reach here. That is a choice the user makes,
  // so the banner says so and offers the private-only alternative.
  if (status.publicNetwork) {
    view.note = t('firewall.publicNote');
    view.primary.includePublic = true;
    view.secondary = { label: t('firewall.allowPrivateOnly'), includePublic: false };
  }
  return view;
}

// firewallUnblockToast turns AllowThroughFirewall's answer into the toast text
// and whether it reports success.
export function firewallUnblockToast(result, t) {
  if (!result) return { ok: false, text: t('firewall.failed', { err: '?' }) };
  if (result.ok) return { ok: true, text: t('firewall.allowed') };
  if (result.declined) return { ok: false, text: t('firewall.declined') };
  return { ok: false, text: t('firewall.failed', { err: result.error || '?' }) };
}

// firewallSuspectGate allows ONE forced re-check per session for each indirect
// signal (an empty speaker list, an install waiting for a callback). Those
// signals repeat every minute while the cause persists, and each re-check
// starts a PowerShell on the backend; one look per signal is enough.
export function firewallSuspectGate() {
  const seen = new Set();
  return (signal) => {
    if (seen.has(signal)) return false;
    seen.add(signal);
    return true;
  };
}

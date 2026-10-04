// stereoinstall.js: what the setup wizard asks before installing STR on half
// of a stereo pair, and what it says afterwards.
//
// Owners of two SoundTouch 10s often paired them in the Bose app before the
// cloud shutdown. Installing STR on one half used to break that pair and leave
// the other half waiting for a partner that had let go (it then refused to play
// at all). The backend writes the pair down before the install
// (CheckStereoBeforeInstall); this module decides which choices to offer.
// Pure functions, so the decisions are tested without a DOM.

// stereoInstallChoices turns the backend's check into the dialog to show.
//   show:false            no pair (or unknown): install as usual, ask nothing
//   options ['both','only'] the partner answers: keep the pair (install STR on
//                         both) or dissolve it on both halves and install one
//   options ['dissolve']  the partner does not answer: the pair can only be
//                         dissolved on this half, so it does not wait forever
export function stereoInstallChoices(check) {
  if (!check || !check.known || !check.paired) return { show: false, options: [] };
  if (check.partnerIP && check.partnerOnline) {
    return { show: true, options: ['both', 'only'], partnerKnown: true, partnerHasSTR: !!check.partnerHasSTR };
  }
  return { show: true, options: ['dissolve'], partnerKnown: false, partnerHasSTR: false };
}

// dissolveHosts lists the speakers to dissolve the pair on for a choice.
export function dissolveHosts(choice, host, check) {
  if (choice === 'only') return [host, check && check.partnerIP].filter(Boolean);
  if (choice === 'dissolve') return [host];
  return [];
}

// restoreMessage maps RestorePairAfterInstall's answer to an i18n key and a
// CSS class, or null when there is nothing to say.
export function restoreMessage(res) {
  const status = res && res.status;
  switch (status) {
    case 'waitingForPartner': return { key: 'setup.pairInstallPartnerNext', cls: 'setup-warn' };
    case 'kept': return { key: 'setup.pairKept', cls: 'setup-ok' };
    case 'restored': return { key: 'setup.pairRestored', cls: 'setup-ok' };
    case 'failed': return { key: 'setup.pairRestoreFailed', cls: 'setup-err' };
    default: return null;
  }
}

// App update for a copy winget installed (JRpersonal.STReborn).
//
// Such a copy must not replace its own exe: winget's version record would stay
// on the old release. The backend (UpdateViaWinget) checks that winget offers
// the version, starts `winget upgrade` in a hidden helper and quits; the helper
// starts the app again once winget is done. Nothing is downloaded by the app.
//
// These helpers only decide what the banner says, so they run without a DOM.

// wingetInstallLabel is the banner button for a winget install, so the user
// knows before clicking that winget, not the app, does the update.
export function wingetInstallLabel(t) {
  return t('banner.updateViaWinget');
}

// wingetOutcomeView turns UpdateViaWinget's answer into what the banner shows:
//   { kind: 'started', text }            app is about to close
//   { kind: 'notYet',  text, command }   winget does not offer the version yet
//   { kind: 'manual',  text, command }   winget could not be started
export function wingetOutcomeView(result, version, t) {
  const r = (result && typeof result === 'object') ? result : {};
  const command = typeof r.command === 'string' && r.command
    ? r.command
    : 'winget upgrade --id JRpersonal.STReborn --exact --silent --accept-source-agreements --accept-package-agreements';
  if (r.started) return { kind: 'started', text: t('banner.wingetStarted') };
  if (r.notYet) return { kind: 'notYet', text: t('banner.wingetNotYet', { version: String(version || '') }), command };
  return { kind: 'manual', text: t('banner.wingetManual'), command };
}

// wingetFailureView is the note after a winget upgrade the previous run started
// came back with an error. null when there is nothing to say.
export function wingetFailureView(failure, t) {
  if (!failure || typeof failure !== 'object' || !failure.failed) return null;
  const command = typeof failure.command === 'string' && failure.command ? failure.command : '';
  return { kind: 'failed', text: t('banner.wingetFailed', { code: String(failure.exitCode || '?') }), command };
}

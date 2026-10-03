// conflictcleanup.js: removing a rival SoundTouch tool's leftovers.
//
// One flow, two buttons. The speaker's settings > Actions had the real
// cleanup; the yellow banner above the speaker list carried a "Remove
// leftovers" button that only switched to that settings page. Its label
// promised the removal, the switch was easy to miss, and an owner pressed it
// twice and saw nothing happen (#1083). Both buttons now run this.
//
// Pure apart from what the caller injects, so it runs under vitest without
// the Wails bindings.

// runConflictCleanup asks, removes, reports what actually happened and offers
// the restart that finishes the job.
//
// deps: { t, confirmWarn, showToast, showError, removeConflictingMod(host, port),
//   rebootBox(host, port), rediscover(afterMs), setBusy(busy) }
export async function runConflictCleanup(box, deps) {
  const { t, confirmWarn, showToast, showError } = deps;
  const mod = (box && box.conflictingMod) || '';
  const cloudOnly = !mod && !!(box && box.foreignCloudURL);
  const name = (box && (box.friendlyName || box.name || box.host)) || '';
  const ok = cloudOnly
    ? await confirmWarn(
      t('settingsView.healCloudURLBtn'),
      t('settingsView.healCloudURLHelp', { url: box.foreignCloudURL })
    )
    : await confirmWarn(
      t('settingsView.removeConflictBtn', { mod: mod || 'AfterTouch' }),
      t('settingsView.removeConflictConfirm', { mod: mod || 'AfterTouch', name })
    );
  if (!ok) return false;
  if (deps.setBusy) deps.setBusy(true);
  try {
    const raw = await deps.removeConflictingMod(box.host, box.port);
    let res = {};
    try { res = JSON.parse(raw); } catch { /* keep empty */ }
    const removed = res.removed || [];
    // Say what actually happened. Until #986 this always toasted success,
    // so a cleanup that matched no file at all reported "leftovers removed
    // (0)" and the reporter reasonably believed his speaker was clean while
    // it went on asking a dead server for every preset.
    const notes = [];
    // cloudURLRestartPending: the address on disk is already the standard
    // one and only the running firmware is still on the old one, because it
    // reads its config once, at boot. That is a pending restart, not a
    // failure, so it must not come out as "nothing found to remove".
    if (res.cloudURLHealed || res.cloudURLRestartPending) {
      notes.push(t('settingsView.removeConflictCloudToast'));
    } else if (!removed.length) {
      notes.push(t('settingsView.removeConflictNothingToast'));
    } else {
      notes.push(t('settingsView.removeConflictDoneToast', { mod: mod || 'AfterTouch', n: removed.length }));
    }
    if (res.stillDetected) {
      notes.push(t('settingsView.removeConflictStillToast', {
        detail: res.foreignCloudURL || res.cloudURLNote || res.mod || '',
      }));
    }
    // Bad news gets the modal the user has to dismiss, good news a toast.
    if (res.stillDetected || (!removed.length && !res.cloudURLHealed && !res.cloudURLRestartPending)) {
      showError(notes.join('\n'));
    } else {
      showToast(notes.join(' '));
    }
    // A reboot fully clears the rival tool's already-running processes, and
    // a healed cloud address only takes effect when the firmware re-reads
    // its config, which it does once, at boot.
    const wantReboot = await confirmWarn(
      t('settingsView.removeConflictRebootTitle'),
      t('settingsView.removeConflictRebootBody', { name })
    );
    if (wantReboot) {
      try {
        await deps.rebootBox(box.host, box.port);
        showToast(t('speaker.rebootingToast'));
        if (deps.rediscover) deps.rediscover(35000);
      } catch (e) { showError(e); }
    } else if (deps.rediscover) {
      deps.rediscover(0);
    }
    return true;
  } catch (e) {
    showError(e);
    return false;
  } finally {
    if (deps.setBusy) deps.setBusy(false);
  }
}

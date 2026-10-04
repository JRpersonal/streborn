// One-time note after an update moved the app to STR-Windows.exe.
//
// Windows Firewall ties its permission to the program's path, so the renamed
// file triggers the network-access question once more. Dismissed, it leaves
// the speaker list empty with nothing to say why. The backend hands out its
// marker exactly once (ConsumeStableNameNotice), so this only decides whether
// to show it and how long it stays up.
//
// deps: { consume: () => Promise<boolean>, showToast(msg, ms), t(key) }
export async function maybeShowStableNameNotice(deps) {
  const { consume, showToast, t } = deps;
  let show = false;
  try {
    show = await consume();
  } catch {
    return false;
  }
  if (!show) return false;
  // Long enough to read twice: this is the one moment the advice matters.
  showToast(t('update.stableNameNotice'), 20000);
  return true;
}

// update/preflight.js: the two reads both update runners make of a speaker
// before they touch it, and the number format both use for the storage figures.
//
// Only what was the same in single.js and all.js, statement for statement. Each
// function is the READ alone and throws when the speaker does not answer; what
// follows from it stays with the caller, because that is where the two paths
// differ: the single update stops and asks (confirmWarn), Update all collects a
// note for its one confirmation. The storage gate (BoxStoragePreflight) is one
// Go call and stays in each runner next to what it does with the verdict.

import { BoxRestartPlan, BoxSettings, boxFetch } from '../api.js';

// stickMounted: is a USB stick in the speaker right now? A stick left in means
// rc.local re-copies the stick's (older) version on the next boot and undoes
// the OTA.
export async function stickMounted(box) {
  const r = await boxFetch(box, '/api/stick/status');
  if (r.ok) {
    const data = await r.json();
    if (data && data.mounted) return true;
  }
  return false;
}

// weakWifiSignal: does the speaker report a marginal or poor Wi-Fi signal? A
// weak link can drop the OTA upload mid-transfer and leave the speaker
// half-updated, then rebooting. Ethernet/coprocessor boxes report no signal
// class and never count as weak.
export async function weakWifiSignal(box) {
  const s = await BoxSettings(box.host, box.port);
  const ifs = (s && s.network && s.network.interfaces) || [];
  // Only a real WIFI_INTERFACE reading counts: that class comes from the
  // firmware live. Coprocessor boxes (ETHERNET-presented Wi-Fi) carry only the
  // agent's last gabbo snapshot - a display hint, not a current measurement,
  // and it false-alarmed on a Portable sitting next to the router (2026-07-12).
  const conn = ifs.find(i => i.type === 'WIFI_INTERFACE' && i.state === 'NETWORK_WIFI_CONNECTED');
  const sig = conn && conn.signal;
  return sig === 'MARGINAL_SIGNAL' || sig === 'POOR_SIGNAL';
}

// mayStayDark: may this speaker stay dark once after the update, with nothing
// the app can do about it beforehand? The Go side decides (BoxRestartPlan): an
// STR older than 1.3.0 ends the update with a restart of the whole speaker,
// which the first-generation SoundTouch 20 and 30 (and the other coprocessor
// Wi-Fi speakers) often do not come back from until they are unplugged. The
// app routes around that restart over SSH where it can; "warn" is where it
// cannot (SSH cannot be opened, or the speaker type cannot be told). The
// owner is told, never stopped.
export async function mayStayDark(box) {
  const plan = await BoxRestartPlan(box.host, box.port);
  return !!plan && plan.mode === 'warn';
}

// fmtMB renders a byte count as megabytes with one decimal, for the free/need
// figures of the storage gate.
export const fmtMB = (n) => ((n || 0) / 1048576).toFixed(1);

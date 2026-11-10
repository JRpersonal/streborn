// banners/ssh.js: the global "remove the stick, SSH is open" reminder
// (#globalSecurityBanner). Extracted from the main.js monolith; its two buttons
// are still wired in main.js at the point of the boot order where the skeleton
// exists.

import { boxFetch } from '../api.js';
import { sameBoxIdentity } from '../groups.js';
import { sshBannerShow } from '../sshbanner.js';
import { state } from '../state.js';
import { $ } from '../utils.js';
import { warnDismissed } from './dismiss.js';

// checkSshBanner queries /api/stick/status to find out whether SSH is
// open on the current speaker and toggles the global top banner
// accordingly. Called on every refreshStatus + discoverBoxes so the
// warning shows up before the user even visits the Settings tab.
export async function checkSshBanner() {
  const gb = $('globalSecurityBanner');
  if (!gb) return;
  const box = state.currentBox;
  // The Setup tab has no current speaker context, so the banner would
  // be free-floating and just noise. Otherwise check sshOpen status.
  if (!box || state.view === 'setup') { gb.classList.add('hidden'); return; }
  // OTA window: agent restarts, SSH may flap, and the banner's
  // "Reboot now" button would interrupt the agent exec. Suppress
  // until doBoxUpdate clears the flag (finally{} guaranteed).
  if (state.otaInProgress) { gb.classList.add('hidden'); return; }
  // Everything below ends in exactly one write of the banner's visibility,
  // including the failure paths. A verdict left standing because the speaker
  // could not be asked is the bug this had: the banner says "this speaker", so
  // a stale yes becomes a false alarm about whichever speaker is selected now,
  // and it asks the user to reboot one for no reason (Jens, 2026-10-02: the Bad
  // speaker, whose agent answered running:false with port 22 refusing).
  let reachable = false;
  let data = null;
  try {
    const r = await boxFetch(box, '/api/stick/status');
    if (r.ok) {
      data = await r.json();
      reachable = true;
    }
  } catch {
    // Asleep, unreachable, or a reply that was not JSON. Nothing is known, so
    // nothing is claimed: reachable stays false and the banner comes down.
  }
  // A speaker switch while the request was in flight would otherwise paint the
  // PREVIOUS speaker's answer under the new speaker's name, the same race the
  // volume sync guards against.
  if (!sameBoxIdentity(state.currentBox, box)) return;
  // The banner is a "remove the stick now that setup is done, otherwise SSH
  // stays open" reminder. As of the pre-1.0 hardening run.sh no longer
  // force-opens sshd on every boot; SSH is open only because a stick is in (the
  // stick opens sshd via its remote_services marker), and a stickless reboot
  // closes it. So sshOpen is an accurate, self-clearing signal, and keying on it
  // (not data.mounted) also covers the Portable, where the stick is in but never
  // auto-mounts so mounted=false (Jens, 2026-06-17). The old mounted-based gate
  // was a workaround from when sshd was always up (#11). Setup view, the OTA
  // window, the persistent-marker case and the per-speaker dismissal are all
  // handled above or inside sshBannerShow, which carries their reasoning.
  const show = sshBannerShow({ reachable, status: data, dismissed: warnDismissed(box, 'ssh') });
  gb.classList.toggle('hidden', !show);
}

# STR Roadmap

Forward-looking items beyond the v1.0 gate defined in `CLAUDE.md`.
This is a wishlist with rough sequencing, not a commitment. Items
move in and out as reality lands.

For the security-specific roadmap see
[`docs/THREAT-MODEL.md`](THREAT-MODEL.md#hardening-roadmap).

## Post-1.0 (already named in CLAUDE.md)

- Code signing and notarization for Windows and macOS installers.
- Additional verified speaker models (ST20, ST30, Portable).
- Wails sandboxing on macOS and Windows.

## In-flight engineering

These are tracked here because they span multiple PRs and need a
durable home so future sessions do not re-scope or duplicate the
work. Distinct from the post-1.0 items above: scheduling here is
"next few PRs", not "after v1.0".

### Frontend refactor of `desktop-app/frontend/src/main.js`

Started 2026-05-17. Goal: split the ~3500-line monolith into
focused modules, add i18n, switch the default UI to English.

| Phase | Scope | Status |
|-------|-------|--------|
| A | Extract leaf modules (`state`, `utils`, `localization`, `logos`, `api`) out of `main.js`. All comments and identifiers English. | merged via [#40](https://github.com/JRpersonal/streborn/pull/40) (or about to be, check the branch) |
| B | Extract view modules: `views/box-discovery.js`, `views/presets.js`, `views/playback.js`, `views/search.js`, `views/settings.js`, `views/setup.js`, `views/footer.js`. After this `main.js` shrinks to the DOM skeleton + view dispatcher (~150 lines). | not started |
| C | i18n system: minimal `t()` helper plus `en` and `de` bundles in `desktop-app/frontend/src/i18n/`. Locale detected from `navigator.language`, with explicit override stored in `localStorage`. **Default is English** per the global-audience rule; German remains a first-class supported locale but is no longer the implicit fallback. | not started |
| D | Translate the remaining inline German comments and the few mixed-language handler strings still sitting in `main.js` (and any view module that ends up holding them after Phase B). Closes the CLAUDE.md "all code/comments/identifiers in English" rule. | not started, naturally falls out of Phase B+C |

Why this is on the roadmap rather than just done: Phase A alone
was ~600 lines of careful diff and surfaced six unrelated bugs
that we durably fixed in flight (see #40). Phase B+C are bigger
still; the right move is one PR per phase so each is reviewable
and any regression can be bisected to its phase.

## Under consideration

### iOS web app (PWA) installable from the website

Idea: the user opens `st-reborn.de` on an iPhone, taps "Add to Home
Screen", and from then on has an app icon that controls the local
SoundTouch speakers without going through the desktop app.

Scope on day one would be the same surface the desktop app exposes:
discover running agents on the LAN, browse internet radio, manage
presets, control playback.

**Feasibility: needs a study before this gets scheduled.** Several
hard blockers exist and a clean answer to each is a prerequisite:

1. **Mixed content / TLS.** A PWA served from `https://st-reborn.de`
   cannot call `http://<speaker-lan-ip>:8888` from a SecureContext.
   Safari blocks the request before it leaves the page.
   Mitigation paths:
   - Agent serves HTTPS with a cert signed by an STR root CA that
     the user installs once via a configuration profile on iOS.
     `internal/tlsgen` already produces per-agent certs; the gap is
     the trust anchor on the client.
   - Or: ship the PWA from the agent itself over plain HTTP. iOS
     refuses to register service workers off `localhost` without
     TLS, so "installable" likely degrades to "bookmarked website".
2. **Discovery.** Browsers have no mDNS / DNS-SD API. The PWA cannot
   see `_streborn._tcp.local` the way the desktop app does. Options:
   - Manual IP entry on first launch.
   - QR code printed by the desktop app or shown in the setup
     wizard, encoding `https://<ip>:<port>` plus a pairing token.
   - Optional: a tiny local-only helper on the user's router or NAS,
     but that pushes the install bar back up.
3. **Background and lock-screen control.** iOS PWAs do not get
   MediaSession lock-screen controls the way native apps do, and
   they suspend aggressively in the background. Playback control
   from the lock screen is almost certainly out of scope for v1 of
   the PWA.
4. **Persistence and storage.** iOS evicts PWA storage after roughly
   seven days of non-use. Presets and settings must be source-of-
   truth on the agent, not in the PWA. This actually fits STR's
   existing architecture, but is worth confirming.

If the TLS-trust story can be made acceptable (one-tap profile
install with a clear consent screen, or a route that does not need
a custom CA at all), this is worth doing. If it requires walking
users through manual certificate trust dialogs, it is not.

### Factory reset wizard in the desktop app

A guided flow in the desktop app that takes a paired speaker back to
a known state without making the user open a shell. Useful when
selling or giving away a speaker, when moving to a different Wi-Fi
network, or when an install has ended up in a corrupt state and the
user wants to try again from scratch.

The wizard should offer at least three levels, in increasing
severity, and explain in plain language what each does before it
runs:

1. **Reset STR data only.** Clears the preset store and any
   per-device settings on the agent. Wi-Fi, the NAND override, and
   the agent binary stay in place. Reversible: the user can
   re-add presets.
2. **Reset Wi-Fi.** Rewrites `/etc/wpa_supplicant.conf` to drop the
   current network so the speaker comes back up in its own setup
   mode on the next boot. The user reconnects via the desktop app
   or the USB stick.
3. **Uninstall STR.** Removes `/mnt/nv/streborn/` and the NAND
   override so the speaker boots stock Bose firmware again. After
   this the speaker has no working internet radio (Bose cloud is
   dead), so the wizard must warn that the USB stick is needed to
   reinstall.

Hard requirements that must hold before this ships:

- Every level must be **idempotent** and safe to interrupt. A wizard
  step that half-writes the NAND override file and then dies leaves
  the speaker in a worse state than before.
- The "uninstall" path must not touch `/etc/wpa_supplicant.conf`
  unless explicitly selected, so a user who only wants STR gone
  keeps their Wi-Fi working.
- The agent must expose a dedicated reset endpoint. The desktop app
  should never SSH into the box for this; SSH-root with a known
  password is the very thing
  [[project-box-security-hardening]] is trying to close.
- The USB stick remains the recovery medium of last resort. If the
  wizard breaks something, plugging the stick back in must fix it
  (per [[feedback-stick-is-recovery]]). The wizard reset paths and
  the stick boot scripts must converge on the same expected state.

Tracked as a GitHub issue under the `enhancement` label.

### Hardware long-press preset save (INTERNET_RADIO re-sourcing spike)

Tracked in [#69](https://github.com/JRpersonal/streborn/issues/69).

The hardware long-press of keys 1-6 currently does not save the
playing station. Live `/gabbo` analysis on ST10 (Bose FW 27.0.6)
confirms the speaker firmware emits **no** WebSocket frame for a
hardware long-press, so the existing STR hook on
`nowSelectionUpdated` has nothing to attach to. The save path is
firmware-internal and gated on the live `ContentItem`
`isPresetable="true"` flag, which the firmware hardcodes to `false`
for our UPnP-routed audio.

The only architecture that can re-enable native long-press is to make
the firmware see the live stream as `INTERNET_RADIO` (where
`isPresetable="true"` is the firmware default). That requires:

1. Mapping the TUNEIN partner endpoint advertised by marge's
   `/bmx/registry/v1/services` and proxying enough of it to satisfy
   the firmware's source-validation path.
2. Routing radio playback through that proxied source instead of
   raw UPnP.
3. Making sure short-press, app-driven save, and standby recovery
   still work end to end after the source switch.

Low priority: the in-app preset workflow already covers the use
case (the user has to play the station first either way), so the
incremental user benefit is small relative to a multi-PR spike.
Kept on the roadmap so the live-analysis findings are not lost.

### Other ideas (loose)

- Android version of the same PWA, with the same feasibility caveats
  except Android allows arbitrary CA install slightly more easily.
- Multi-room grouping across speakers using the speaker's existing
  local zone API on port 8090 (no cloud involved). Tracked as a
  separate issue with a research write-up.
- Spotify Connect on the speaker, tracked in
  [#78](https://github.com/JRpersonal/streborn/issues/78). Several
  plausible paths (librespot on-box, librespot in the Wails backend,
  Spotify Web API). Post-1.0 spike, all paths blocked by the loss of
  the Bose-Spotify OEM partner credentials.

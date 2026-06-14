# STR Roadmap

Forward-looking items beyond the v1.0 gate defined in `CLAUDE.md`.
This is a wishlist with rough sequencing, not a commitment. Items
move in and out as reality lands.

For the security-specific roadmap see
[`docs/THREAT-MODEL.md`](THREAT-MODEL.md#hardening-roadmap).

## Post-1.0 (already named in CLAUDE.md)

- Code signing and notarization for Windows and macOS installers.
- Promote the remaining model statuses to Verified (Portable is
  Verified, ST20 spotty contributor-confirmed, ST30 live-confirmed;
  see [`MODELS.md`](MODELS.md)).
- Wails sandboxing on macOS and Windows.

## In-flight engineering

These are tracked here because they span multiple PRs and need a
durable home so future sessions do not re-scope or duplicate the
work. Distinct from the post-1.0 items above: scheduling here is
"next few PRs", not "after v1.0".

### Frontend refactor of `desktop-app/frontend/src/main.js`

Started 2026-05-17. Goal: split the `main.js` monolith (~3500 lines
then, **6892 lines as of 2026-06-12**) into focused modules, add
i18n, switch the default UI to English.

| Phase | Scope | Status |
|-------|-------|--------|
| A | Extract leaf modules (`state`, `utils`, `localization`, `logos`, `api`) out of `main.js`. All comments and identifiers English. | **done** — merged via [#40](https://github.com/JRpersonal/streborn/pull/40) |
| B | Extract view modules + shared services along the existing section-comment seams (see the refactoring backlog below for the concrete module cut). After this `main.js` shrinks to the DOM skeleton + router + bootstrap (~600 lines; the old ~150-line estimate predates the feature growth). | not started — `src/views/` exists but is empty; `main.js` has grown to 6892 lines, which makes this the highest-leverage refactor in the repo |
| C | i18n system: minimal `t()` helper plus `en` and `de` bundles in `desktop-app/frontend/src/i18n/`. Locale detected from `navigator.language`, with explicit override stored in `localStorage`. **Default is English** per the global-audience rule; German remains a first-class supported locale but is no longer the implicit fallback. | **done** — shipped in #46 and grown to 11 locale bundles. Caveat: 9 of the 10 non-English bundles are ~285-301 keys behind `en.json` (silent English fallback); a CI completeness check is in the refactoring backlog |
| D | Translate the remaining inline German comments and the few mixed-language handler strings still sitting in `main.js` (and any view module that ends up holding them after Phase B). Closes the CLAUDE.md "all code/comments/identifiers in English" rule. | mostly done via the #46 i18n sweep; ~a dozen German comments remain in `main.js` (and the older on-stick scripts are still German, tracked in the backlog below) — finish when Phase B touches those regions |

Why this is on the roadmap rather than just done: Phase A alone
was ~600 lines of careful diff and surfaced six unrelated bugs
that we durably fixed in flight (see #40). Phase B+C are bigger
still; the right move is one PR per phase so each is reviewable
and any regression can be bisected to its phase.

### Refactoring backlog (audited 2026-06-12)

Result of a full-repo refactor audit, prioritized by payoff/risk.
Each item is a single reviewable PR; line numbers as of v0.7.21.

**Correctness-adjacent (do first, small):**

1. `runSSHWithFlags`/`runSSHWithFlagsStdin` race on `c.Process` and
   can nil-deref panic when the timeout beats `Start()` (4-8 s
   timeouts in use). Rewrite both on `exec.CommandContext`
   (`desktop-app/install_str.go:669-732`). Same PR: nil-ctx guard
   (`appCtx()` helper) for the five `a.ctx` parents, and start the
   LAN-sweep drain before spawning (`app.go:737-784`).
2. Unify the six hand-rolled reboot variants and five SSH-handshake
   policies on the hardened forms (`sync; /sbin/reboot` detached;
   4x retry handshake) — the #114 lesson is currently unapplied on
   the uninstall/factory-reset/OTA paths.
3. `webui.handleBoxSyncPresets` pushes the raw `StreamURL` instead of
   the proxy slot URL (`webui.go:2071-2102`) — latent bug the
   stream-URL single-source below would have prevented.
4. Replace `boxws`'s whole-frame `strings.Contains` sniffing with one
   typed `xml.Unmarshal` per frame: a track title containing
   `STOP_STATE` can fire the user-stop suppressor today. This is also
   the landing spot for the planned `presetsUpdated` parsing.
5. Extend the SSRF dial guard to `internal/upnp`'s playlist fetches
   (currently naked clients) via a shared `netutil` guarded-client
   helper; fixes a short-read bug in `extractStreamFromPlaylist` on
   the way.

**Structure (mechanical, medium):**

6. Single source for the box-facing loopback URLs
   (`/stream/<slot>`, `/spotify/stream-<slot>.ogg`, `/stream/raw?u=`):
   today stamped out at 9+ sites across `cmd/agent`, `webui`, and the
   frontend; the v0.7.21 self-proxy regression was this class.
7. Unify the dual Spotify recall paths (hardware
   `playSpotifyPreset` vs app `handlePlaySlot`) into one orchestrator:
   they have already drifted (the soft verify still carries the
   restart-on-re-Play bug the hardware path fixed in v0.7.4).
8. File splits, pure moves, no behavior change: `desktop-app/app.go`
   (3158 lines -> ~10 files incl. `boxssh.go` for the SSH transport
   shared by 6 features), `cmd/agent/main.go` (2026 lines),
   `internal/webui/webui.go` (2485 lines), `internal/spotify/manager.go`
   (1510 lines), `internal/streamproxy/streamproxy.go` (1052 lines).
9. Frontend Phase B (see table above): cut `main.js` along its
   existing section comments into ~9 view modules + 6 services;
   pre-step: decompose the 910-line `renderBoxSettings`. Delete the
   dead `checkBoxUpdate` (its banner element no longer exists).

**Guardrails (CI, small):**

10. CI is blind to the `desktop-app` Go module (no vet/test/lint runs
    there; a live `go vet` finding exists) — add working-directory
    steps. Add a `wails generate module` freshness check (known
    recurring gotcha) and an i18n key-completeness report. Run
    `busybox sh -n` over `run.sh` next to shellcheck.
11. Tests for the privacy sanitizers in `logexport.go`
    (anonymize IP/MAC/SSID/serial) — a silent regression there leaks
    user data into public issue attachments. Then: boxws frame
    parser, upnp SOAP golden tests, version helpers.
12. `usb-stick/run.sh` (3461 lines): keep single-file (the
    rc.local copy chain forbids sourcing siblings) but add a
    table-of-contents header, the BusyBox parse gate above, and a
    parity test for the duplicated `lang_int_for_cc` table.

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

**Status:** Level 2 shipped in v0.5.17 as `TrueFactoryReset`
(commit `5ddc6e8`); level 3 shipped as `UninstallSTR` (Speaker
Settings → Remove STR, with a stick-present safeguard; removes
`/mnt/nv/streborn/` and the boot override). Only level 1 (reset STR
data only) is still open. Both shipped levels currently ride the
pre-1.0 SSH channel; before the v1.0 SSH hardening lands they must
migrate to a dedicated agent reset endpoint (see the requirement
above), after which SSH becomes opt-in.

### Other ideas (loose)

- Android version of the same PWA, with the same feasibility caveats
  except Android allows arbitrary CA install slightly more easily.
- Multi-room grouping: **shipped as alpha** since the v0.7.x line
  ([#70](https://github.com/JRpersonal/streborn/issues/70)) — native
  `/setZone` plus a per-agent mirror fallback, NAND-persisted zones
  with auto-reform, stereo pairs. Remaining: promote out of alpha
  after broader hardware feedback.
- Spotify Connect: **shipped as beta** since the v0.7.x line
  ([#78](https://github.com/JRpersonal/streborn/issues/78)) via a
  supervised go-librespot sidecar with Ogg passthrough, per-slot
  stream URLs, Spotify presets and multi-account. Remaining: promote
  out of beta (stability), native multi-account upstream (fork issue
  #1), upstreaming the passthrough patch (devgianlu PR #316).
- Additional streaming providers, tracked in
  [#103](https://github.com/JRpersonal/streborn/issues/103). STR plays
  radio and Spotify today; SoundCloud, Amazon Music and **Deezer** are
  candidates. **Deezer is wanted** by users whose existing Deezer
  station-key presets still work after the Bose shutdown (the cloud
  source survived where radio did not). Two distinct pieces:
  - **Preserve, then import.** As of v0.7.21 an STR install no longer
    overwrites existing speaker presets, so Deezer keys survive an
    install untouched. The next step is reading the existing Deezer
    `ContentItem` location out of the box's preset XML (visible over the
    box API / in `presetsUpdated` frames) so STR can show and keep them
    even when the user has not set them in STR. This is the same
    box-side preset-state parsing that would also help preset recall and
    box/app sync, so it is worth doing once, generally.
  - **Create new Deezer presets** from STR. This needs a Deezer
    integration (auth + the playable URL shape) and is the larger,
    later piece. First action is collecting an anonymised example of a
    working Deezer preset URL from a user's box XML to learn the format.

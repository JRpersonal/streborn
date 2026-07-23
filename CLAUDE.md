# STR (SoundTouch Reborn): briefing for Claude Code

This file is the entry point for any Claude Code session working on
this repository. Read it first.

## What this project is

In February 2026 Bose shut down the SoundTouch cloud. All SoundTouch
speakers (models 10, 20, 30, Portable) lost their internet radio,
presets, and remote control overnight. STR (SoundTouch Reborn) brings
them back **without any Bose cloud dependency**.

### Components

- **Stick Agent**: a small Go binary delivered via a USB stick used
  for the initial install. The agent copies itself to the speaker's
  NAND on first boot and from then on runs entirely from there, so
  the stick can be removed for normal operation. It stands in for
  `streaming.bose.com` and the Bose `bmx-cloud` services locally so
  the speaker pairs and accepts presets.
- **Desktop App (ST Reborn)**: Wails application for Windows, macOS,
  Linux. Discovers running agents on the LAN via mDNS, ships a web
  UI for browsing internet radio, managing presets, and controlling
  playback. Also performs the initial USB-stick provisioning and
  later OTA agent updates.
- **Website**: Astro site (English and German) at `st-reborn.de`
  with downloads, FAQ, privacy, imprint. Maintained in a separate
  repository.

### Architecture in one picture

```
Client (Browser / Desktop App)
  -- REST API --> Stick Agent webui :8888
     (sm2 boxes (ST10 rhino, sm2-ST30 mojo) reached directly; BCO/whitelisted
      chassis (Portable/taigan, ST20 spotty/scm, scm-ST30 mojo, scm-Wave lisa)
      reached via iptables PREROUTING REDIRECT :17008 -> loopback :8888;
      the ST20/ST30/Wave each exist in BOTH chassis generations - see
      docs/MODEL-VARIANTS.md before assuming the port path)
                            |
                            +--> /api/presets        preset store on NAND
                            +--> /api/play, /api/play/<slot>, /api/pause, /api/stop
                            |     upnp.PlayURL -> Box:8091/AVTransport
                            +--> /api/status         proxy Box:8090/now_playing (cached)
                            +--> /api/box/*          speaker settings (name/volume/bass/
                            |     source/wlan/reboot/airplay-opt/sync-presets/zone/group)
                            +--> /api/region, /api/stick/status, /api/debug/*
                            +--> /api/agent/version, /api/agent/update
                            |     OTA (HTTP; the app falls back to SSH-OTA and
                            |     refreshes a still-inserted stick before the reboot)
                            +--> /api/webhooks, /api/webhooks/test
                            |     user-configured HTTP triggers on box events (NAND)
                            +--> /stream/<slot>, /stream/raw
                            |     streamproxy (survives CDN token expiry; converts
                            |     HLS playlists to one continuous ADTS/MP3 stream)
                            +--> /api/stream/bitrate, /api/stream/title, /api/stream-status
                            +--> /spotify/stream.ogg, /spotify/stream-1..6.ogg, /spotify/info
                            |     Spotify Connect (beta): supervised go-librespot
                            |     sidecar, raw Ogg passthrough (internal/spotify)
                            |
                            +--> boxws  ws://127.0.0.1:8080/ (gabbo protocol)
                            |     nowSelectionUpdated -> upnp.PlayURL
                            |
                            +--> autopair  POST /setMargeAccount every 5 min
                            |
                            +--> marge stub  :9080 (HTTP) / :443 (TLS)
                            |     emulates streaming.bose.com + content.api.bose.io
                            |     /streaming/account/.../device/  -> adddeviceresponse
                            |     /streaming/sourceproviders      -> source list
                            |     /bmx/registry/v1/services       -> service list
                            +--> bmx stub    :8081  (healthz-only placeholder; the
                            |     registry route above is answered by marge)
                            |
                            +--> :17002 BatteryMonitor fallback (Portable only)
                            |     accepts BoseApp's battery client when the stock
                            |     BatteryMonitor is wedged -> stops the fd-leak
                            |     reboot loop (see docs/FIRMWARE-NOTES.md)
                            |
                            +--> mDNS announce _streborn._tcp.local

Bose firmware ports STR talks to / around:
  :8080 gabbo WS    :8090 BoseApp REST    :8091 UPnP AVTransport
  :17000 TAP CLI (standby wake, provisioning probes)
  :17008 SoftwareUpdate (external entry on whitelisted chassis, REDIRECT -> :8888)
  :17002 BatteryMonitor
```

Audio path: UPnP AVTransport directly to the speaker on port 8091.
We never proxy audio through the dead Bose cloud.

Radio search runs **app-side**: the desktop app queries
`radio-browser.info` (free, no API key) directly via the top-level
`radiobrowser/` package (`desktop-app/radio.go`); the agent no longer
serves `/api/radio` and only receives the final stream URL to play.
This replaces the discontinued Bose TuneIn integration.

Hardware preset buttons 1 through 6 are re-enabled by hooking the
Bose WebSocket bus (gabbo protocol on `:8080`).

Beyond radio, the desktop app ships a DLNA media library (top-level
`dlna/` package), Spotify Connect (beta), multiroom zones (beta),
diagnostics export, and box maintenance (true factory reset,
uninstall STR, setup-AP Wi-Fi push). The UI ships in 11 locales;
English and German are first-class.

## Conventions in this repo

### Language

**All code, comments, identifiers, commit messages, documentation,
PR descriptions, and developer-facing text are in English.**

User-facing UI strings live in i18n bundles. English and German are
first-class; other languages welcome via PR.

### Style

- Go 1.25+, `gofmt`, `golangci-lint` clean.
- Logging via `log/slog`.
- Module path: `github.com/JRpersonal/streborn`.
- Tests in `_test.go` files alongside the code they cover.
- No emoji in code, commits, or PR descriptions unless explicitly
  requested.
- **Commits drive release notes.** Use Conventional Commits
  (`type(scope): summary`). The release pipeline (`cmd/relnotes`) turns
  the `feat` / `fix` / `perf` / breaking commits since the last tag into
  the user-facing "What's changed" list, and the summary after the colon
  is shown to end users almost verbatim. So write the summary as a clear
  end-user statement (not internal jargon), with no version prefix in the
  subject, and use a non-user-facing type (`chore`, `ci`, `build`,
  `docs`, `test`, `refactor`, `style`) for work that must stay out of the
  notes. See `docs/RELEASE-NOTES.md` and `CONTRIBUTING.md`.

### Disclaimers (legal, do not remove)

- "SoundTouch" and "Bose" are registered trademarks of Bose
  Corporation. STR is an **unofficial, community-built project**, not
  affiliated with, endorsed by, or authorized by Bose. This must be
  visible in `README.md`, on the website footer, and in the
  application About dialog.
- STR is provided as-is under the LICENSE file. Users run it at their
  own risk.

## What never goes into this repo

The following must never be committed:

- Bose firmware binaries, NAND dumps, decompiled Bose code, or any
  other Bose copyrighted material.
- Network captures, traces, or logs that contain data from accounts
  or devices other than your own test hardware.
- Personal identifiers: real LAN IPs, MAC addresses, speaker device
  IDs or serial numbers, private email addresses. Use placeholders in
  examples: `192.0.2.1`, `AA:BB:CC:DD:EE:FF`, `device-id-here`,
  `user@example.com`.
- Anyone's Wi-Fi SSIDs or captured credentials.
- Build outputs (`bin/`, `desktop-app/build/`, `dist/`).

If you spot any of these, treat it as a security incident: stop,
alert the user, and propose a sanitisation commit before doing
anything else.

## Threat model and box hardening

The full threat model, the known weaknesses of the speaker firmware
that STR runs on top of, and the hardening roadmap live in
[`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md). Read it before
touching anything in `internal/autopair/`, `internal/tlsgen/`,
`usb-stick/setup-tls.sh`, or `usb-stick/iptables-setup.sh`.

User-facing security guarantees and the vulnerability reporting
process are in [`SECURITY.md`](SECURITY.md).

## What v1.0 means

STR is pre-1.0. The bar to ship 1.0 is intentionally low and
measurable, not aspirational:

1. **At least two speaker models verified end to end.** Met: ST10
   (rhino) and Portable (taigan) are Verified, ST20 (spotty) is
   contributor-confirmed with final stability confirmation in
   progress, and a live ST30 (mojo) has run the agent successfully.
   Current per-model state lives in [`docs/MODELS.md`](docs/MODELS.md).
2. **Hardware presets 1 to 6 work after a cold boot, a standby cycle,
   and a Wi-Fi outage**: no manual reset required.
3. **First-install experience is honest.** SmartScreen / Gatekeeper
   warnings are documented on the website Verify page with the exact
   click path; SHA256 sums and Sigstore attestations are linked.
4. **Threat model published.** `docs/THREAT-MODEL.md` covers the
   speaker firmware caveats, what STR mitigates, and what it does
   not.
5. **Legal pages complete.** Website imprint, privacy policy, and
   their German equivalents have no placeholder text.

Code signing, notarization, additional models, and Wails sandboxing
are post-1.0. Forward-looking ideas beyond v1.0, currently an iOS
PWA proposal and a factory-reset wizard for the desktop app, live
in [`docs/ROADMAP.md`](docs/ROADMAP.md).

## Release pipeline dry-run

`release.yml` runs a weekly automatic dry-run (Sundays 04:30 UTC) so
Dependabot bumps of release-only actions like
`attest-build-provenance`, `action-gh-release`, or
`upload-artifact`'s release-only usage pattern are exercised before
the next real tag. PR CI does not exercise these actions.

To trigger a dry-run manually (e.g. after a Dependabot bump you do
not want to wait a week for), dispatch the release workflow without
a version input:

```bash
gh workflow run release.yml
```

Either path runs verify-source, builds the agent and all three
desktop OS packages, and attests every artifact via Sigstore. The
final `Publish GitHub Release` step is skipped automatically when
no `version` input is supplied. If a dry-run green-checks, the next
real `vX.Y.Z` tag will work.

## Build version stamping

The Wails desktop app embeds the ARM stick agent binary and is the
only component that initiates an over-the-air agent update on the
speaker. If the desktop app and the embedded agent are built from
different commits, the version-comparison logic flags the stick as
out of date even right after a successful update: the OTA banner
then loops.

The release workflow builds both from the same checkout in a single
pipeline. Do not split that into independent jobs without preserving
the shared version stamp.

## Build and embed quirks

- **`desktop-app/agentbin/streborn-armv7l`** is a `go:embed` target.
  An empty file with the same name is checked in so the embed
  compiles on a clean checkout. CI overwrites it with the real ARM
  binary built in an earlier job. The `.gitignore` has explicit
  exceptions so the stub stays tracked while real build outputs
  remain ignored.
- **`sticksetup/embedded/winformat.exe`** is also a `go:embed`
  target. Same pattern: empty stub tracked, CI overwrites. Local
  developers build the real embeds with `make winformat-embed` and
  `make agent-embed`; both run automatically as dependencies of
  `make wails-build` / `make wails-dev`. (Raw `go build` misses the
  `GOOS`/`GOARCH` pins and the version stamp.)
- Empty stubs mean `agentbin.Available()` correctly returns `false`
  on dev builds, so the desktop app falls back to a configured
  external path instead of writing zero bytes onto the stick.

## Runtime quirks worth remembering

- **NAND override beats SD card.** On the speaker,
  `/mnt/nv/streborn/run-override.sh` runs in place of the SD-based
  entry point. The SD card is unreliable for writes; treat it as
  read-only.
- **Do not re-exec `run-override.sh` while it is already running.**
  It collides with the Bose SCM manager and the speaker ends up in a
  bad state. Update the binary and restart cleanly instead.
- **`/etc/wpa_supplicant.conf`** must be rewritten in full with one
  `network={}` block. Appending breaks Wi-Fi because the speaker
  tries dead networks first.
- **Do not poll stick config endpoints in a loop.** Read once at
  agent start, at most a few times after USB mount events.
- **Standby recovery:** after the speaker is put into standby via the
  power button, UPnP and CLI still respond but playback does not
  resume. Use the wake-and-wait helper to recover.

## Repository layout

```
cmd/
  agent/         Stick agent main
  mdns-probe/    mDNS debug CLI
  relnotes/      Release-notes generator (conventional commits -> notes)
  winformat/     FAT32 format helper for the Windows installer
desktop-app/     Wails project (Vite frontend, Go backend; own Go module)
discovery/       mDNS discovery (top-level so Wails can import it)
dlna/            DLNA MediaServer client for the Library tab (top-level)
docs/            Sanitised technical docs only
internal/        Stick-agent-only packages (boxapi, marge, autopair, ...)
radiobrowser/    radio-browser.info client (app-side radio search)
setup/           Legacy PowerShell setup wizard (superseded by the
                 desktop app's in-app stick setup)
sticksetup/      Embedded setup workflow
tools/           Support diagnostics scripts
usb-stick/       Stick filesystem layout and run.sh
wifiprofiles/    Cross-platform Wi-Fi profile reader for the wizard
.github/         Workflows, dependabot, CODEOWNERS, security policies
```

The website (st-reborn.de) lives in a separate repository
(`JRpersonal/streborn-website`). A release here triggers a build
there via `repository_dispatch`.

`discovery/`, `dlna/`, `radiobrowser/`, `sticksetup/`, and
`wifiprofiles/` live at the top level on purpose. The desktop app is
its own Go module and imports them; Go forbids importing from another
module's `internal/`.

Hardware support state per model is tracked in
[`docs/MODELS.md`](docs/MODELS.md).

## Workflows

Workflows live in `.github/workflows/`: `build.yml` (source
verification: lockfile consistency, vulnerability scan, tests,
cross-compiles, shellcheck), `release.yml` (release publication on
signed tags), `codeql.yml`, `scorecard.yml` (OSSF Scorecard),
`dependabot-automerge.yml`, and two `workflow_dispatch` builds of the
Spotify sidecar binaries (`go-librespot.yml` primary, with the STR
Ogg-passthrough patch from `.github/patches/`; `librespot.yml`
fallback), both Sigstore-attested. Dependabot, Secret Scanning, and
Push Protection are additionally enabled at the repository settings
level.

## How a new Claude session should start

1. Read this file end to end.
2. Skim `README.md` for the user-facing pitch.
3. Read [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the
   component map, tech stack, port table, and the sequence diagrams
   that show how discovery, playback, marge emulation, install, and
   OTA actually flow.
4. Check [`docs/MODELS.md`](docs/MODELS.md) for hardware support
   state, [`docs/MODEL-VARIANTS.md`](docs/MODEL-VARIANTS.md) for the
   per-variant fingerprint table (moduleType, firmware, kernel,
   components) that incoming diagnostics get matched against, and
   [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md) for security
   context. Before touching `internal/spotify/` or the go-librespot
   workflows, also read
   [`docs/streaming/spotify.md`](docs/streaming/spotify.md); for box
   firmware quirks, [`docs/FIRMWARE-NOTES.md`](docs/FIRMWARE-NOTES.md).
5. Run `go build ./...` and `make wails-dev` once to confirm the
   local environment is healthy. The stick agent contains Linux-only
   syscalls; on Windows or macOS hosts use
   `GOOS=linux GOARCH=arm GOARM=5 go build ./...` to cross-compile
   it for the actual target. GOARM=5 (softfloat) is deliberate: some
   early SoundTouch CPUs lack working VFP and a hardware-float binary
   SIGILLs at startup and soft-bricks the box (issue #302). `make
   build-arm` / `make agent-embed` already pin this.

## Communicating with users

- **Every issue/PR/discussion reply must be fully followable in
  English AND also serve the original reporter in their own
  language.** When a user writes in another language, structure the
  reply as three parts: (1) an English translation of their message
  (e.g. a short "User reports: ..." line), (2) your answer in English,
  and (3) the same answer in the user's language. English is always
  present so every reader can follow the thread; the native-language
  copy is added so the reporter is served directly. When the user
  already wrote in English, a plain English reply is enough.
- Questions that are not bug reports belong in GitHub Discussions,
  not Issues.
- The maintainer's contact email is the one on file at GitHub for
  security reports. Do not put a personal email address into
  user-facing strings.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for how to set up a local
build, the PR checklist, and the commit message format.

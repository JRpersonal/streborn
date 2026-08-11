# STR (SoundTouch Reborn): briefing for Claude Code

This file is the entry point for any Claude Code session working on
this repository. Read it first.

## What this project is

In February 2026 Bose shut down the SoundTouch cloud. All SoundTouch
speakers (models 10, 20, 30, Portable) lost their internet radio,
presets, and remote control overnight. STR (SoundTouch Reborn) brings
them back **without any Bose cloud dependency**.

### Components

- **Stick Agent**: a small Go binary running on the speaker. The
  normal first install happens over the network (the speaker's
  setup port); the USB stick remains the fallback and recovery
  path. The agent lives on the speaker's NAND and runs entirely
  from there, so no stick is needed for normal operation. It stands
  in for `streaming.bose.com` and the Bose `bmx-cloud` services
  locally so the speaker pairs and accepts presets.
- **Desktop App (ST Reborn)**: Wails application for Windows, macOS,
  Linux. Discovers running agents on the LAN via mDNS, ships a web
  UI for browsing internet radio, managing presets, and controlling
  playback. Also performs the initial install (network install by
  default, USB-stick provisioning as the fallback) and later OTA
  agent updates.
- **Website**: Astro site (English and German) at `st-reborn.de`
  with downloads, FAQ, privacy, imprint. Maintained in a separate
  repository.

### Architecture

The component map, the full port table, and the sequence diagrams for
discovery, playback, marge emulation, install, and OTA live in
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

The trap that document does not shout loudly enough: the ST20, ST30,
and Wave each exist in BOTH chassis generations, so read
[`docs/MODEL-VARIANTS.md`](docs/MODEL-VARIANTS.md) before assuming
whether a box is reached on `:8888` directly (sm2) or via the
`:17008` PREROUTING REDIRECT (BCO/whitelisted chassis).

Audio path: UPnP AVTransport directly to the speaker on port 8091.
We never proxy audio through the dead Bose cloud.

Hardware preset buttons 1 through 6 are re-enabled by hooking the
Bose WebSocket bus (gabbo protocol on `:8080`).

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

Additional models and Wails sandboxing are post-1.0. Code signing is
done on both desktop platforms: Windows installers are Certum-signed
since v0.9.20, the macOS app and DMG are Developer ID signed and
Apple-notarized since v0.9.33. Forward-looking ideas beyond v1.0,
currently an iOS PWA proposal and a factory-reset wizard for the
desktop app, live in [`docs/ROADMAP.md`](docs/ROADMAP.md).

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

`discovery/`, `dlna/`, `radiobrowser/`, `sticksetup/`, and
`wifiprofiles/` live at the top level on purpose. The desktop app is
its own Go module and imports them; Go forbids importing from another
module's `internal/`.

The website (st-reborn.de) lives in a separate repository
(`JRpersonal/streborn-website`). A release here triggers a build
there via `repository_dispatch`.

Hardware support state per model is tracked in
[`docs/MODELS.md`](docs/MODELS.md).

## Workflows

Workflows live in `.github/workflows/`. Two things are not visible
from those files: the Spotify sidecar builds are `workflow_dispatch`
only (`go-librespot.yml` primary, `librespot.yml` fallback), and
Dependabot, Secret Scanning, and Push Protection are enabled at the
repository settings level rather than in any workflow file.

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

# STR (SoundTouch Reborn) — Briefing for Claude Code

This file is the entry point for any Claude Code session working on
this repository. Read it first.

## What this project is

In February 2026 Bose shut down the SoundTouch cloud. All SoundTouch
speakers (models 10, 20, 30, Portable) lost their internet radio,
presets, and remote control overnight. STR (SoundTouch Reborn) brings
them back **without any Bose cloud dependency**.

### Components

- **Stick Agent** — a small Go binary delivered via a USB stick used
  for the initial install. The agent copies itself to the speaker's
  NAND on first boot and from then on runs entirely from there — the
  stick can be removed for normal operation. It emulates
  `streaming.bose.com` and the Bose `bmx-cloud` services locally so
  the speaker pairs and accepts presets.
- **Desktop App (ST Reborn)** — Wails application for Windows, macOS,
  Linux. Discovers running agents on the LAN via mDNS, ships a web
  UI for browsing internet radio, managing presets, and controlling
  playback. Also performs the initial USB-stick provisioning and
  later OTA agent updates.
- **Website** — Astro site (English and German) at `st-reborn.de`
  with downloads, FAQ, privacy, imprint. Maintained in a separate
  repository.

### Architecture in one picture

```
Client (Browser / Desktop App)
  -- REST API ----------> Stick Agent (8888)
                            |
                            +--> /api/presets   preset store on NAND
                            +--> /api/play      upnp.PlayURL -> Box:8091/AVTransport
                            +--> /api/radio     radiobrowser.Search/TopVote
                            +--> /api/status    proxy /now_playing
                            |
                            +--> boxws  ws://127.0.0.1:8080/gabbo
                            |     nowSelectionUpdated -> upnp.PlayURL
                            |
                            +--> autopair  POST /setMargeAccount every 5 min
                            |
                            +--> marge stub  emulates streaming.bose.com
                            |     /streaming/account/.../device/  -> adddeviceresponse
                            |     /bmx/registry/v1/services       -> service list
                            |     /streaming/sourceproviders      -> source list
                            |
                            +--> mDNS announce _streborn._tcp.local
```

Audio path: UPnP AVTransport directly to the speaker on port 8091.
We never proxy audio through the dead Bose cloud.

Radio source: `radio-browser.info` (free, no API key) replaces the
discontinued Bose TuneIn integration.

Hardware preset buttons 1 through 6 are re-enabled by hooking the
Bose WebSocket bus (`/gabbo`).

## Conventions in this repo

### Language

**All code, comments, identifiers, commit messages, documentation,
PR descriptions, and developer-facing text are in English.**

User-facing UI strings live in i18n bundles. English and German are
first-class; other languages welcome via PR.

### Style

- Go 1.22+, `gofmt`, `golangci-lint` clean.
- Logging via `log/slog`.
- Module path: `github.com/JRpersonal/streborn`.
- Tests in `_test.go` files alongside the code they cover.
- No emoji in code, commits, or PR descriptions unless explicitly
  requested.

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

1. **At least two speaker models verified end to end.** ST10 is the
   reference target today. One additional model (ST20 or ST30)
   confirmed by a maintainer or a trusted contributor on real
   hardware.
2. **Hardware presets 1–6 work after a cold boot, a standby cycle,
   and a Wi-Fi outage** — no manual reset required.
3. **First-install experience is honest.** SmartScreen / Gatekeeper
   warnings are documented on the website Verify page with the exact
   click path; SHA256 sums and Sigstore attestations are linked.
4. **Threat model published.** `docs/THREAT-MODEL.md` covers the
   speaker firmware caveats, what STR mitigates, and what it does
   not.
5. **Legal pages complete.** Website imprint, privacy policy, and
   their German equivalents have no placeholder text.

Code signing, notarization, additional models, and Wails sandboxing
are post-1.0.

## Build version stamping

The Wails desktop app embeds the ARM stick agent binary and is the
only component that initiates an over-the-air agent update on the
speaker. If the desktop app and the embedded agent are built from
different commits, the version-comparison logic flags the stick as
out of date even right after a successful update — the OTA banner
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
  developers can build the real one with:
  `go build -ldflags "-s -w" -o sticksetup/embedded/winformat.exe ./cmd/winformat`
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
  winformat/     FAT32 format helper for the Windows installer
desktop-app/     Wails project (Vite frontend, Go backend)
discovery/       mDNS discovery (top-level so Wails can import it)
docs/            Sanitised technical docs only
internal/        Stick-agent-only packages (boxapi, marge, autopair, ...)
setup/           Setup wizard helpers
sticksetup/      Embedded setup workflow
usb-stick/       Stick filesystem layout and run.sh
website/         Astro site
wifiprofiles/    Cross-platform Wi-Fi profile reader for the wizard
.github/         Workflows, dependabot, CODEOWNERS, security policies
```

`discovery/` lives at the top level on purpose. The desktop app
imports it, and Go forbids importing from another module's
`internal/`.

Hardware support state per model is tracked in
[`docs/MODELS.md`](docs/MODELS.md).

## Workflows

Workflows live in `.github/workflows/`. They cover source
verification (lockfile consistency, vulnerability scan, tests),
cross-platform builds, and release publication on signed tags.
Dependabot, Secret Scanning, CodeQL, and Push Protection are enabled
at the repository settings level.

## How a new Claude session should start

1. Read this file end to end.
2. Skim `README.md` for the user-facing pitch.
3. Check [`docs/MODELS.md`](docs/MODELS.md) for hardware support
   state and [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md) for
   security context.
4. Run `go build ./...` and `cd desktop-app && wails dev` once to
   confirm the local environment is healthy. The stick agent
   contains Linux-only syscalls; on Windows or macOS hosts use
   `GOOS=linux GOARCH=arm GOARM=7 go build ./...` to cross-compile
   it for the actual target.

## Communicating with users

- Issues and PRs are in English. If a user opens a non-English
  issue, respond in their language and add an English summary so
  other maintainers can follow.
- Questions that are not bug reports belong in GitHub Discussions,
  not Issues.
- The maintainer's contact email is the one on file at GitHub for
  security reports. Do not put a personal email address into
  user-facing strings.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for how to set up a local
build, the PR checklist, and the commit message format.

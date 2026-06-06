# STR, SoundTouch Reborn

**Cloud free firmware project for Bose SoundTouch speakers.**

<p align="center">
  <a href="https://github.com/JRpersonal/streborn/actions/workflows/build.yml"><img src="https://github.com/JRpersonal/streborn/actions/workflows/build.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/JRpersonal/streborn/actions/workflows/codeql.yml"><img src="https://github.com/JRpersonal/streborn/actions/workflows/codeql.yml/badge.svg" alt="CodeQL"></a>
  <a href="https://github.com/JRpersonal/streborn/actions/workflows/release.yml"><img src="https://github.com/JRpersonal/streborn/actions/workflows/release.yml/badge.svg" alt="Release"></a>
  <a href="https://scorecard.dev/viewer/?uri=github.com/JRpersonal/streborn"><img src="https://api.securityscorecards.dev/projects/github.com/JRpersonal/streborn/badge" alt="OpenSSF Scorecard"></a>
  <a href="https://github.com/JRpersonal/streborn/releases/latest"><img src="https://img.shields.io/github/v/release/JRpersonal/streborn" alt="Latest release"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/github/license/JRpersonal/streborn" alt="License"></a>
</p>

<p align="center">
  <img src="docs/screenshots/OS%20Hero/app-listen.jpg" alt="ST Reborn desktop app" width="820">
</p>

Bose discontinued their SoundTouch cloud service in February 2026. STR keeps the speakers usable: a USB stick installs a small Go agent onto the speaker that stands in for the discontinued cloud locally, talks to the speaker over the home network, and brings the hardware preset buttons back to life. **Once the agent is installed, the stick can be removed**: the agent persists on the speaker and survives reboots.

## How it works in one paragraph

After the first boot with the stick attached, the agent copies itself into the speaker's persistent storage and from then on starts automatically every time the speaker powers on, no stick required for normal use. It hosts a stand-in for the Bose cloud on the loopback interface and redirects the relevant DNS names so the speaker treats it as the real cloud. Internet Radio playback then happens over UPnP AVTransport on the speaker, which is supported natively. The hardware preset buttons are wired through the speaker's local WebSocket so a button press triggers playback of the saved station.

## Screenshots

The desktop app: browse and assign presets, search internet radio, browse a DLNA library, manage speaker settings, and prepare the USB stick.

| | | |
|:--:|:--:|:--:|
| [![Presets and playback](docs/screenshots/OS%20Hero/app-listen.jpg)](docs/screenshots/OS%20Hero/app-listen.jpg) | [![Internet radio search](docs/screenshots/OS%20Hero/app-search.jpg)](docs/screenshots/OS%20Hero/app-search.jpg) | [![Speaker settings](docs/screenshots/OS%20Hero/app-settings1.jpg)](docs/screenshots/OS%20Hero/app-settings1.jpg) |
| Presets and playback | Internet radio search | Speaker settings |
| [![DLNA library](docs/screenshots/OS%20Hero/app-library.jpg)](docs/screenshots/OS%20Hero/app-library.jpg) | [![USB stick setup](docs/screenshots/OS%20Hero/app-stick-step1.jpg)](docs/screenshots/OS%20Hero/app-stick-step1.jpg) | [![Stick: Wi-Fi, name, region](docs/screenshots/OS%20Hero/app-stick-step2.jpg)](docs/screenshots/OS%20Hero/app-stick-step2.jpg) |
| DLNA music library | USB stick setup | Wi-Fi, name, region |

The interface is available in eleven languages (English, German, French, Spanish, Japanese, Ukrainian, Dutch, Polish, Lithuanian, Latvian, Turkish). The full per-language screenshot set lives in [`docs/screenshots/`](docs/screenshots/) and is regenerated automatically with `npm run screenshots`, a headless Playwright harness that mocks the backend with demo data, so no speaker is needed.

## Status (May 2026)

STR is pre-1.0. This section is the honest snapshot. No marketing.

### What works

- Discovery of installed sticks over mDNS, list view in the desktop app.
- Playback control: play / pause / stop / volume / bass / source switch (AUX, Bluetooth, Standby) via the speaker's existing UPnP AVTransport endpoint on port 8091. I never route audio through the dead Bose cloud.
- Radio search via radio-browser.info. Replaces the dead Bose TuneIn integration. No API key.
- Six preset slots, persisted on the stick agent. Hardware preset buttons 1 to 6 work after install via a hook into Bose's WebSocket bus (`/gabbo`).
- OTA agent updates from the desktop app. Build stamp comparison catches version drift.
- WLAN reconfigure from the desktop app. I rewrite `/etc/wpa_supplicant.conf` in full because appending breaks Wi-Fi.
- Setup wizard for the USB stick, including preset region, friendly name, and Wi-Fi credentials. FAT32 formatting helper bundled.

Targets: SoundTouch 10, 20, 30, Portable. My reference target is the ST10. ST20 and ST30 I have touched on but not finalised against real hardware end to end.

### What I do for security

- DNS pinning for the Bose hostnames (`streaming.bose.com`, `bmx-cloud.*`, TuneIn partner subdomain) to `127.0.0.1` via an `/etc/hosts` bind-mount. The speaker no longer makes outbound queries for these names. This closes the residual domain-squat risk if Bose lets the DNS lapse and someone re-registers it.
- Per-box local TLS CA, generated on first boot, stored in `/mnt/nv/streborn/ca/`, installed in the speaker's own trust store. Only valid for the loopback-redirected hostnames. Never transmitted. TLS only on loopback.

### What I do not do for security yet

- No client authentication on the stick web UI (`:8888`). Any device on the LAN that can reach the speaker can edit presets and trigger playback.
- No encryption between the desktop app and the stick. Both sit on the LAN, the LAN is the trust boundary.
- No verification of the stock speaker firmware integrity.
- No sandboxing of the desktop application.

### What is inherited from stock Bose firmware (and I do not change)

- HTTP control on `:8090` and UPnP on `:8091` accept any LAN client without authentication. Standard SoundTouch behaviour, not added by me.
- The speakers ship with `root` having no password set. SSH (port 22) is enabled by Bose's own init script when a `remote_services` file is present on a mounted USB stick. While the stick is in, any device on the LAN can reach a passwordless root shell. I remove the stick from the boot path after first install and stop `sshd` once the stick is unmounted, so in normal operation the port is closed. The window during which the stick sits in the speaker is the only time this is exposed. The desktop app shows a banner during that window.

### Factory reset

A Bose factory reset clears only what Bose itself knows about: the Bose preset database, account, friendly name, Wi-Fi. It does not touch `/mnt/nv/streborn/`, which is where my agent binary, CA, preset store, region, name, and the `run-override.sh` hook live. After a factory reset, STR is still installed and boots automatically.

Implication: a speaker being passed on or sold needs a separate "Uninstall STR" step that removes `/mnt/nv/streborn/` and the NAND override. I have that wizard step planned (see [`docs/ROADMAP.md`](./docs/ROADMAP.md), "Factory reset wizard"), not yet shipped.

### Pre-1.0 gaps I still owe before tagging 1.0

Per my own criteria in [`CLAUDE.md`](./CLAUDE.md):

1. I have only verified ST10 end to end. A second model confirmed on real hardware (by me or a trusted contributor) is the explicit requirement.
2. Hardware preset buttons need to survive cold boot, standby cycle, and Wi-Fi outage. I observe this working, but I do not yet have a regression test that pins it.
3. First-install experience: SmartScreen and Gatekeeper documentation on the website Verify page with the exact click path and a linked SHA256 plus Sigstore attestation. Partially in place, not finalised.
4. Threat model document published. Present in [`docs/THREAT-MODEL.md`](./docs/THREAT-MODEL.md). It does not yet cover the persistence-across-factory-reset point above, which I owe.
5. Legal pages on the website (imprint, privacy, both German). Some sections still contain placeholders.

Code signing, notarization, additional models beyond the 1.0 threshold, sandboxing the Wails app, and the hardening steps (token auth on `:8888`, iptables egress lockdown, automatic `passwd root` on install) I see as post-1.0.

## Quick start for developers

```bash
git clone https://github.com/JRpersonal/streborn.git
cd streborn

# Build the stick agent for the speaker hardware (ARMv7l)
make build-arm

# Build the desktop app (requires Wails v2 CLI)
cd desktop-app
wails build
```

Requirements: Go 1.22 or newer, Node 20 or newer, Wails CLI v2 for the desktop app.

The website (st-reborn.de) lives in a separate repository, [`JRpersonal/streborn-website`](https://github.com/JRpersonal/streborn-website). A release here triggers a build there via `repository_dispatch`.

## Architecture

If you want to understand how the agent, the desktop app, and the speaker's stock firmware fit together (components, ports, data flows for discovery, playback, marge emulation, install, OTA), read [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md). It is short, has diagrams, and is the right starting point for contributors.

## Repository layout

| Path | Description |
|------|-------------|
| `cmd/agent/` | Stick agent entry point |
| `internal/` | Marge cloud stub, BMX, UPnP, mDNS, WebSocket hook, preset store |
| `usb-stick/` | Bootstrap and runtime scripts on the speaker |
| `setup/` | Setup wizard (PowerShell) |
| `desktop-app/` | Cross-platform Wails app |
| `.github/` | CI and release workflows |
| `docs/` | Public documentation (architecture, threat model, models, roadmap) |

## Downloads and end user documentation

See [st-reborn.de](https://st-reborn.de).

## Verifying release artifacts

Every release on GitHub Releases is built by the official workflow and ships with build provenance attestations via Sigstore. You can verify any binary with:

```bash
gh attestation verify STR-Setup-Windows.exe --owner JRpersonal
```

For the threat model and the vulnerability reporting process see [SECURITY.md](./SECURITY.md) and [docs/THREAT-MODEL.md](./docs/THREAT-MODEL.md).

## How this repo stays clean

Every change is checked automatically and the results are public, so you do not have to take my word for any of it. The badges at the top of this page are live: green means the latest run passed, click one to open the run.

- **CI** runs `golangci-lint`, `govulncheck` (Go vulnerability scan), and the test suite on every push and pull request.
- **CodeQL** runs static security analysis on every push and weekly.
- **OpenSSF Scorecard** audits the supply-chain posture (branch protection, pinned dependencies, signed releases, token hygiene) weekly and publishes the score.
- **Dependabot** keeps Go, npm, and GitHub Actions dependencies patched; all third-party actions are pinned to a commit SHA.
- **Secret Scanning with Push Protection** blocks commits that contain a leaked credential.
- **Releases** are built only by the workflow from a signed tag, with SHA256 sums and Sigstore build-provenance attestations (above). All shipped binaries, including the small speaker shim, are compiled from source by the workflow; no opaque prebuilt binaries are committed.

Findings from Dependabot, CodeQL, and Scorecard surface in the repository's [Security tab](https://github.com/JRpersonal/streborn/security). The full policy is in [SECURITY.md](./SECURITY.md), and hard-won notes about the stock firmware STR runs on top of are in [docs/FIRMWARE-NOTES.md](./docs/FIRMWARE-NOTES.md).

## Privacy

STR has no accounts, no ads, and no third-party trackers in the app. The speaker never contacts the Bose cloud: STR answers it locally. The desktop app makes a single external call, an optional version check to st-reborn.de that sends only the app version, build, OS, architecture, and language, and is disablable with `STR_NO_UPDATE_CHECK=1`. The website uses cookieless GoatCounter analytics. Full breakdown: [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md#telemetry-analytics-and-privacy).

## Contributing

Issues and pull requests welcome. By submitting a contribution you agree to license it under MIT. Significant changes please open an issue first to discuss the approach.

## Support the project

If STR helped bring your speaker back to life, please consider a donation.

[![GitHub Sponsors](https://img.shields.io/github/sponsors/JRpersonal?label=Sponsor%20on%20GitHub&logo=GitHub&color=ea4aaa)](https://github.com/sponsors/JRpersonal)

More payment options on [st-reborn.de](https://st-reborn.de/#donate).

## Disclaimer

STR is an independent open source project. The abbreviation **ST** references compatibility with Bose SoundTouch family speakers. STR is **not affiliated with, endorsed by, sponsored by, or otherwise connected to** Bose Corporation. **Bose** and **SoundTouch** are registered trademarks of Bose Corporation in the United States and other countries.

STR exists solely to restore functionality of these speakers after the official Bose cloud service shutdown in February 2026. Reverse engineering for interoperability is permitted under EU Directive 2009/24/EC, Article 6, and comparable provisions in other jurisdictions.

The software is provided AS IS, without warranty. Use at your own risk.

## Acknowledgements and third-party software

STR stands on other people's open source. Thank you.

- **[go-librespot](https://github.com/devgianlu/go-librespot)** by devgianlu, **GPL-3.0** , the Spotify Connect client that powers STR's Spotify support. It ships as a **separate binary** that STR runs as a child process and talks to over a local API/pipe; it is not linked into STR, so STR's own MIT code and the GPL-3.0 binary are merely aggregated, each under its own license. STR builds it from a small fork, [JRpersonal/go-librespot](https://github.com/JRpersonal/go-librespot) (also GPL-3.0), that adds a raw-Ogg passthrough mode; that change is offered back upstream. The full source of the bundled build is public there.
- **[radio-browser.info](https://www.radio-browser.info/)** , the community-run radio station directory STR searches, with no key and no account.
- **[Octicons](https://github.com/primer/octicons)** by GitHub (MIT) , a couple of UI icons.
- The community that documented the SoundTouch TAP CLI and firmware behaviour after the cloud shutdown, whose findings STR builds on.

Bundled components keep their own licenses; STR's own code is MIT.

## License

MIT. See [LICENSE](./LICENSE). The bundled go-librespot binary is GPL-3.0; see the Acknowledgements above.

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

The interface is available in eleven languages (English, German, French, Spanish, Japanese, Ukrainian, Dutch, Polish, Lithuanian, Latvian, Turkish). The full per-language screenshot set lives in [`docs/screenshots/`](docs/screenshots/) and is regenerated automatically with `npm run shoot` in `desktop-app/frontend/screenshots/`, a headless Playwright harness that mocks the backend with demo data, so no speaker is needed.

## Status (June 2026)

STR is pre-1.0. This section is the honest snapshot. No marketing.

### What works

- Discovery of installed sticks over mDNS, list view in the desktop app. Speakers without STR show up too, marked "ready for STR", and can be installed in-app.
- Playback control: play / pause / stop / volume / bass / source switch (AUX, Bluetooth, Standby) via the speaker's existing UPnP AVTransport endpoint on port 8091. I never route audio through the dead Bose cloud.
- Radio search via radio-browser.info, queried directly by the desktop app (no API key); only the final stream URL goes to the speaker. HLS-only stations (BBC and co.) are converted on the fly by the agent's stream proxy. On a blocked or dead stream the app automatically tries another listing of the same station.
- Six preset slots, persisted on the stick agent. Hardware preset buttons 1 to 6 work after install via a hook into Bose's WebSocket bus (gabbo). Existing non-STR presets (e.g. Deezer) are left untouched.
- Spotify Connect (beta): a supervised go-librespot sidecar on the speaker; Spotify presets, multi-account, live now-playing.
- DLNA music library: browse FRITZ!Box / Synology / Plex / miniDLNA servers and save tracks as presets.
- Multiroom zones and stereo pairs (alpha).
- Webhooks: user-configured HTTP triggers on box events (remote keys, power, AUX).
- OTA agent updates from the desktop app, with an SSH fallback and a pre-reboot stick refresh so the update cannot be reverted by the boot sync. Build stamp comparison catches version drift.
- WLAN reconfigure from the desktop app. I rewrite `/etc/wpa_supplicant.conf` in full because appending breaks Wi-Fi.
- Setup wizard for the USB stick, including preset region, friendly name, box language, and Wi-Fi credentials. FAT32 formatting helper bundled.
- Diagnostics export (anonymised), true factory reset, and a full "Uninstall STR" that returns the speaker to stock.

Targets: SoundTouch 10, 20, 30, Portable. ST10 and Portable are verified end to end; ST20 is contributor-confirmed; ST30 has run live with the final pass pending. Current per-model state: [`docs/MODELS.md`](./docs/MODELS.md).

### What I do for security

- DNS pinning for the Bose hostnames (`streaming.bose.com`, `bmx-cloud.*`, TuneIn partner subdomain) to `127.0.0.1` via an `/etc/hosts` bind-mount. The speaker no longer makes outbound queries for these names. This closes the residual domain-squat risk if Bose lets the DNS lapse and someone re-registers it.
- Per-box local TLS CA, generated on first boot, stored in `/mnt/nv/streborn/ca/`, installed in the speaker's own trust store. Only valid for the loopback-redirected hostnames; the CA private key never leaves the speaker's NAND. The stand-in listeners themselves are LAN-reachable like the rest of the agent surface.

### What I do not do for security yet

- No client authentication on the stick web UI (`:8888`). Any device on the LAN that can reach the speaker can edit presets and trigger playback.
- No encryption between the desktop app and the stick. Both sit on the LAN, the LAN is the trust boundary.
- No verification of the stock speaker firmware integrity.
- No sandboxing of the desktop application.

### What is inherited from stock Bose firmware (and I do not change)

- HTTP control on `:8090` and UPnP on `:8091` accept any LAN client without authentication. Standard SoundTouch behaviour, not added by me.
- The speakers ship with `root` having no password set. SSH (port 22) is enabled by Bose's own init script when a `remote_services` file is present on a mounted USB stick. **Pre-1.0, STR itself keeps `sshd` running on every boot** (stick or no stick): when an install or update leaves the agent down, SSH is the only channel that still lets the desktop app pull diagnostics and repair the box, and I currently weight that recovery path over closing the port. Any device on the LAN can reach a passwordless root shell while the speaker is on. The desktop app shows a banner reminding you to remove the stick after setup; it does not indicate SSH state. Making SSH opt-in (via a stick marker) is part of the v1.0 hardening. Until then: put the speaker on a trusted network.

### Factory reset

A Bose factory reset clears only what Bose itself knows about: the Bose preset database, account, friendly name, Wi-Fi. It does not touch `/mnt/nv/streborn/`, which is where my agent binary, CA, preset store, region, name, and the `run-override.sh` hook live. After a factory reset, STR is still installed and boots automatically.

Implication: a speaker being passed on or sold needs a separate "Uninstall STR" step. That ships in the desktop app: Speaker Settings offers **Remove STR** (removes `/mnt/nv/streborn/` and the boot override, returns the speaker to stock Bose firmware) and a separate **True Factory Reset**. See [`docs/ROADMAP.md`](./docs/ROADMAP.md), "Factory reset wizard", for the remaining level (reset STR data only).

### Pre-1.0 gaps I still owe before tagging 1.0

Per my own criteria in [`CLAUDE.md`](./CLAUDE.md):

1. Two models verified end to end: met (ST10 and Portable verified, ST20 contributor-confirmed, ST30 live with the final pass pending; see [`docs/MODELS.md`](./docs/MODELS.md)).
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

# Build the desktop app with embedded helpers and version stamp
# (requires Wails v2 CLI; raw `wails build` leaves the embeds empty)
make wails-build
```

Requirements: Go 1.25 or newer, Node 20 or newer, Wails CLI v2 for the desktop app. Note: on Windows/macOS hosts the agent itself only cross-compiles (`make build-arm`); plain `go build ./...` fails on its Linux-only syscalls.

The website (st-reborn.de) lives in a separate repository, [`JRpersonal/streborn-website`](https://github.com/JRpersonal/streborn-website). A release here triggers a build there via `repository_dispatch`.

## Architecture

If you want to understand how the agent, the desktop app, and the speaker's stock firmware fit together (components, ports, data flows for discovery, playback, marge emulation, install, OTA), read [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md). It is short, has diagrams, and is the right starting point for contributors.

## Repository layout

| Path | Description |
|------|-------------|
| `cmd/` | Stick agent entry point, plus `winformat`, `relnotes`, `mdns-probe` helpers |
| `internal/` | Agent-only packages: marge cloud stub, BMX, UPnP, WebSocket hook, preset store, stream proxy, Spotify manager, zones, webhooks |
| `discovery/` | mDNS discovery (top level so the desktop app can import it) |
| `dlna/` | DLNA MediaServer client for the Library tab (top level) |
| `radiobrowser/` | radio-browser.info client for the app-side radio search (top level) |
| `sticksetup/` / `wifiprofiles/` | Embedded stick provisioning + saved-Wi-Fi reader (top level) |
| `usb-stick/` | Bootstrap and runtime scripts on the speaker |
| `setup/` | Legacy PowerShell wizard (superseded by the in-app stick setup) |
| `desktop-app/` | Cross-platform Wails app (own Go module) |
| `.github/` | CI and release workflows |
| `docs/` | Public documentation (architecture, threat model, models, roadmap) |

## Downloads and end user documentation

See [st-reborn.de](https://st-reborn.de).

## Verifying release artifacts

Every release on GitHub Releases is built by the official workflow and ships with build provenance attestations via Sigstore. You can verify any binary with:

```bash
gh attestation verify STR-Windows-vX.Y.Z.exe --owner JRpersonal
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

STR has no accounts, no ads, and no third-party trackers in the app. The speaker never contacts the Bose cloud: STR answers it locally; with Spotify Connect enabled it talks to Spotify's servers. The desktop app talks to st-reborn.de (optional version check, disablable with `STR_NO_UPDATE_CHECK=1`; sends only the app version, build, OS, architecture, and language), to radio-browser.info for the station search, and to public favicon endpoints for station logos. The website uses cookieless GoatCounter analytics. Full breakdown: [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md#telemetry-analytics-and-privacy).

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

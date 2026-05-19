# STR — SoundTouch Reborn

**Cloud free firmware project for Bose SoundTouch speakers.**

Bose discontinued their SoundTouch cloud service in February 2026. STR keeps the speakers usable: a USB stick installs a small Go agent onto the speaker that emulates the missing cloud locally, talks to the speaker over the home network, and brings the hardware preset buttons back to life. **Once the agent is installed, the stick can be removed** — the agent persists on the speaker and survives reboots.

## How it works in one paragraph

After the first boot with the stick attached, the agent copies itself into the speaker's persistent storage and from then on starts automatically every time the speaker powers on, no stick required for normal use. It hosts a stand-in for the Bose cloud on the loopback interface and redirects the relevant DNS names so the speaker treats it as the real cloud. Internet Radio playback then happens over UPnP AVTransport on the speaker, which is supported natively. The hardware preset buttons are wired through the speaker's local WebSocket so a button press triggers playback of the saved station.

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
- Per-box local TLS CA, generated on first boot, stored in `/mnt/nv/streborn/ca/`, installed in the speaker's own trust store. Only valid for the hijacked hostnames. Never transmitted. TLS only on loopback.

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

# Run the website locally
cd website
npm install
npm run dev
```

Requirements: Go 1.22 or newer, Node 20 or newer, Wails CLI v2 for the desktop app.

## Repository layout

| Path | Description |
|------|-------------|
| `cmd/agent/` | Stick agent entry point |
| `internal/` | Marge cloud stub, BMX, UPnP, mDNS, WebSocket hook, preset store |
| `usb-stick/` | Bootstrap and runtime scripts on the speaker |
| `setup/` | Setup wizard (PowerShell) |
| `desktop-app/` | Cross platform Wails app |
| `website/` | Static Astro plus Tailwind website |
| `.github/` | Release and website deploy workflows |
| `docs/` | Public documentation |

## Downloads and end user documentation

See [st-reborn.de](https://st-reborn.de).

## Verifying release artifacts

Every release on GitHub Releases is built by the official workflow and ships with build provenance attestations via Sigstore. You can verify any binary with:

```bash
gh attestation verify STR-Setup-Windows.exe --owner JRpersonal
```

For the threat model and the vulnerability reporting process see [SECURITY.md](./SECURITY.md) and [docs/THREAT-MODEL.md](./docs/THREAT-MODEL.md).

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

## License

MIT. See [LICENSE](./LICENSE).

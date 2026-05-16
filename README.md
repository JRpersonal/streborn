# STR — SoundTouch Reborn

**Cloud free firmware project for Bose SoundTouch speakers.**

Bose discontinued their SoundTouch cloud service in February 2026. STR keeps the speakers usable: a USB stick installs a small Go agent onto the speaker that emulates the missing cloud locally, talks to the speaker over the home network, and brings the hardware preset buttons back to life. **Once the agent is installed, the stick can be removed** — the agent persists on the speaker and survives reboots.

## What works

- Internet Radio playback over the physical preset buttons 1 to 6
- Desktop app for Windows and Mac with station search via radio-browser.info
- Automatic discovery of every running agent on the LAN through mDNS
- Multiple speakers in the same network; one USB stick is enough to install onto all of them in turn
- No separate server, no cloud account

Tested on SoundTouch 10. Other models on the roadmap.

## How it works in one paragraph

After the first boot with the stick attached, the agent copies itself into the speaker's persistent storage and from then on starts automatically every time the speaker powers on — no stick required for normal use. It hosts a stand-in for the Bose cloud on the loopback interface and redirects the relevant DNS names so the speaker treats it as the real cloud. Internet Radio playback then happens over UPnP AVTransport on the speaker, which is supported natively. The hardware preset buttons are wired through the speaker's local WebSocket so a button press triggers playback of the saved station.

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

## Security

Every release on GitHub Releases is built by the official workflow and ships with build provenance attestations via Sigstore. You can verify any binary with:

```bash
gh attestation verify STR-Setup-Windows.exe --owner JRpersonal
```

See [SECURITY.md](./SECURITY.md) for the full threat model and vulnerability reporting.

## Contributing

Issues and pull requests welcome. By submitting a contribution you agree to license it under MIT. Significant changes please open an issue first to discuss the approach.

## Support the project

If STR helped bring your speaker back to life, please consider a donation. Sponsor button at the top of the repo or links on [st-reborn.de](https://st-reborn.de/#donate).

## Disclaimer

STR is an independent open source project. The abbreviation **ST** references compatibility with Bose SoundTouch family speakers. STR is **not affiliated with, endorsed by, sponsored by, or otherwise connected to** Bose Corporation. **Bose** and **SoundTouch** are registered trademarks of Bose Corporation in the United States and other countries.

STR exists solely to restore functionality of these speakers after the official Bose cloud service shutdown in February 2026. Reverse engineering for interoperability is permitted under EU Directive 2009/24/EC, Article 6, and comparable provisions in other jurisdictions.

The software is provided AS IS, without warranty. Use at your own risk.

## License

MIT. See [LICENSE](./LICENSE).

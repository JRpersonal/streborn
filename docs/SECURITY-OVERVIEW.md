# Security, privacy and release verification at a glance

The short overview of what STR does and does not do for security, what a Bose factory reset leaves behind, how to verify a download, and how the repository is checked. The full threat model is in [`THREAT-MODEL.md`](THREAT-MODEL.md), the reporting process in [`SECURITY.md`](../SECURITY.md).

# What I do for security

- DNS pinning for the Bose hostnames (`streaming.bose.com`, `bmx-cloud.*`, TuneIn partner subdomain) to `127.0.0.1` via an `/etc/hosts` bind-mount. The speaker no longer makes outbound queries for these names. This closes the residual domain-squat risk if Bose lets the DNS lapse and someone re-registers it.
- Per-box local TLS CA, generated on first boot, stored in `/mnt/nv/streborn/ca/`, installed in the speaker's own trust store. Only valid for the loopback-redirected hostnames; the CA private key never leaves the speaker's NAND. The stand-in listeners themselves are LAN-reachable like the rest of the agent surface.

# What I do not do for security yet

- No client authentication on the agent's web UI (`:8888`). Any device on the LAN that can reach the speaker can edit presets and trigger playback.
- No encryption between the desktop app and the agent. Both sit on the LAN, the LAN is the trust boundary.
- No verification of the stock speaker firmware integrity.
- No sandboxing of the desktop application.

# What is inherited from stock Bose firmware (and I do not change)

- HTTP control on `:8090` and UPnP on `:8091` accept any LAN client without authentication. Standard SoundTouch behaviour, not added by me.
- The speakers ship with `root` having no password set, and SSH (port 22) is enabled by Bose's own init script when a `remote_services` file is present on a mounted USB stick. **STR does not hold that port open.** On a normal, stickless boot the speaker ends up with SSH closed; STR only force-starts `sshd` when you deliberately ask for it, with the SSH switch in the speaker's settings in the desktop app (it places the marker file `/mnt/nv/streborn/enable-ssh` on the speaker). That keeps the repair channel available when an install or update leaves the agent down, without leaving a passwordless root shell on your LAN the rest of the time. While a setup stick is inserted, Bose's own gate opens SSH, which is why the app reminds you to pull the stick after setup. The same switch turns it off again.

# Factory reset

A Bose factory reset clears only what Bose itself knows about: the Bose preset database, account, friendly name, Wi-Fi. It does not touch `/mnt/nv/streborn/`, which is where my agent binary, CA, preset store, region, name, and the `run-override.sh` hook live. After a factory reset, STR is still installed and boots automatically.

Implication: a speaker being passed on or sold needs a separate "Uninstall STR" step. That ships in the desktop app: Speaker Settings offers **Remove STR** (removes `/mnt/nv/streborn/` and the boot override, returns the speaker to stock Bose firmware) and a separate **True Factory Reset**. See [`docs/ROADMAP.md`](ROADMAP.md), "Factory reset wizard", for the remaining level (reset STR data only).

## Verifying release artifacts

Every release on GitHub Releases is built by the official workflow and ships with build provenance attestations via Sigstore. You can verify any binary with:

```bash
gh attestation verify STR-Windows.exe --owner JRpersonal
```

The desktop downloads have stable names without a version (`STR-Windows.exe`, `STR-macOS.dmg`, `STR-Linux-x64.tar.gz`; releases up to v1.0.1 carried the version in the name). The version is shown in the app and, on Windows, under Properties > Details. Each release also carries its Sigstore bundle as a file, `STR-<version>.sigstore.json`, for offline checks with `gh attestation verify STR-Windows.exe --bundle STR-<version>.sigstore.json --owner JRpersonal`.

Windows builds are additionally Authenticode-signed with a Certum open-source code-signing certificate; check the signature in the file's Properties > Digital Signatures tab or with `signtool verify /pa`.

For the threat model and the vulnerability reporting process see [SECURITY.md](../SECURITY.md) and [docs/THREAT-MODEL.md](THREAT-MODEL.md).

## How this repo stays clean

Every change is checked automatically and the results are public, so you do not have to take my word for any of it. The badges below are live: green means the latest run passed, click one to open the run.

<p align="center">
  <a href="https://github.com/JRpersonal/streborn/actions/workflows/build.yml"><img src="https://github.com/JRpersonal/streborn/actions/workflows/build.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/JRpersonal/streborn/actions/workflows/codeql.yml"><img src="https://github.com/JRpersonal/streborn/actions/workflows/codeql.yml/badge.svg" alt="CodeQL"></a>
  <a href="https://github.com/JRpersonal/streborn/actions/workflows/release.yml"><img src="https://github.com/JRpersonal/streborn/actions/workflows/release.yml/badge.svg" alt="Release"></a>
  <a href="https://scorecard.dev/viewer/?uri=github.com/JRpersonal/streborn"><img src="https://api.securityscorecards.dev/projects/github.com/JRpersonal/streborn/badge" alt="OpenSSF Scorecard"></a>
  <a href="https://www.bestpractices.dev/projects/15200"><img src="https://www.bestpractices.dev/projects/15200/badge" alt="OpenSSF Best Practices"></a>
  <a href="https://pkg.go.dev/github.com/JRpersonal/streborn"><img src="https://pkg.go.dev/badge/github.com/JRpersonal/streborn.svg" alt="Go Reference"></a>
  <a href="https://github.com/JRpersonal/streborn/releases/latest"><img src="https://img.shields.io/github/v/release/JRpersonal/streborn" alt="Latest release"></a>
  <a href="../LICENSE"><img src="https://img.shields.io/github/license/JRpersonal/streborn?cacheSeconds=3600" alt="License"></a>
</p>


- **CI** runs `golangci-lint`, `govulncheck` (Go vulnerability scan), and the test suite on every push and pull request.
- **CodeQL** runs static security analysis on every push and weekly.
- **OpenSSF Scorecard** audits the supply-chain posture (branch protection, pinned dependencies, signed releases, token hygiene) weekly and publishes the score.
- **Dependabot** keeps Go, npm, and GitHub Actions dependencies patched; all third-party actions are pinned to a commit SHA.
- **Secret Scanning with Push Protection** blocks commits that contain a leaked credential.
- **Releases** are built only by the release workflow, from the main branch, with SHA256 sums and Sigstore build-provenance attestations (see the section above). All shipped binaries, including the small speaker shim, are compiled from source by the workflow, and every release carries the Sigstore bundle and the SLSA provenance as files (`STR-<version>.sigstore.json`, `STR-<version>.intoto.jsonl`). One prebuilt binary is still in the repository: `usb-stick/str-shim.so`, which only local developer builds use. Release builds replace it with the workflow-built, attested copy (since v1.0.1), and removing it from the repository is planned.

Findings from Dependabot, CodeQL, and Scorecard surface in the repository's [Security tab](https://github.com/JRpersonal/streborn/security). The full policy is in [SECURITY.md](../SECURITY.md), and hard-won notes about the stock firmware STR runs on top of are in [docs/FIRMWARE-NOTES.md](FIRMWARE-NOTES.md). How to bring a speaker to Bose's last firmware, including very old ones, is in [docs/FIRMWARE-UPDATE.md](FIRMWARE-UPDATE.md).

## Privacy

STR has no accounts, no ads, and no third-party trackers in the app. The speaker never contacts the Bose cloud: STR answers it locally; it fetches station logos for its display and the phone remote through its own `/art` proxy (the station's logo host and `icons.duckduckgo.com`, sending only the station's public domain, never a LAN host), and with Spotify Connect enabled it talks to Spotify's servers. The desktop app talks to st-reborn.de (optional version check, disablable with `STR_NO_UPDATE_CHECK=1`; sends only the app version, build, OS, architecture, and language), to st-reborn.de again for the list in the Wishes tab (a plain download, translations included, nothing is sent), to radio-browser.info for the station search, and to public favicon endpoints for station logos. The website uses cookieless GoatCounter analytics. Full breakdown: [docs/ARCHITECTURE.md](ARCHITECTURE.md#telemetry-analytics-and-privacy).

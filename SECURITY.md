# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest release | yes |
| prior releases | no |

Use the most recent release. Older builds may contain known vulnerabilities or compatibility issues.

## Reporting a Vulnerability

If you discover a security issue please do **not** open a public GitHub issue. Send an email to:

**str@sichtbar-app.de**

Subject line: `[security] STR (SoundTouch Reborn)`

Include:

- a clear description of the issue
- steps to reproduce
- affected version(s)
- your assessment of impact
- whether you wish to be credited

STR is maintained in spare time, so there is no formal SLA — but
reports get attention as quickly as the maintainer can manage,
typically within a few days.

Critical findings will be patched and a short advisory published in
GitHub Releases. Reporters who follow responsible disclosure are
credited unless they prefer to stay anonymous.

## Threat Model

STR (SoundTouch Reborn) installs a small agent onto a Bose SoundTouch speaker via a USB stick that is used for the initial install. After the install, the agent runs from the speaker's own persistent storage and the stick can be removed. It also ships a desktop application and a website.

Critical trust boundaries:

1. **Binary release** users download to their machine
2. **Code that runs on the speaker** with root privileges
3. **TLS certificate authority** that the stick installs in the speaker's trust store
4. **GitHub repository** and its build artifacts
5. **Website** that hosts download links and donation channels

## Build Provenance

All binaries published on GitHub Releases are produced exclusively by the official GitHub Actions workflow in this repository. Manual uploads to releases are not part of the trusted build process.

Each release attaches:

- the binary itself
- a `SHA256SUMS` file
- a build attestation produced by GitHub's OIDC infrastructure (sigstore based)

Users can verify a download with either:

```bash
# Method 1: SHA256 checksum
sha256sum -c SHA256SUMS

# Method 2: GitHub CLI attestation verification
gh attestation verify STR-Setup-Windows.exe \
    --owner JRpersonal
```

The attestation proves:

- the binary was built by the official workflow
- in the official repository
- from a specific commit
- by GitHub's runners (not a developer machine)

## Supply Chain Security Practices

### Repository

- Branch protection on `main`: pull request required, status checks must pass, signed commits required, force push forbidden, admin enforcement enabled.
- Two factor authentication required for all maintainers, hardware key preferred.
- Personal access tokens minimized; OIDC used for CI auth where possible.
- Dependabot enabled for Go modules, npm packages, and GitHub Actions.
- All third party GitHub Actions pinned to a full commit SHA (not a tag).

### Build

- Builds run in clean GitHub hosted runners.
- `go mod verify` runs before build.
- `npm ci` is used (never `npm install`) to ensure lockfile is honored.
- Build artifacts are attested using `actions/attest-build-provenance`.
- SHA256 sums are generated and uploaded alongside artifacts.

### Release

- Releases are created from signed Git tags only.
- Release notes contain the commit hash and link to the workflow run.
- No manual binary uploads to releases.

### Website

- Static HTML, no server side code on the website.
- Download links point exclusively to GitHub Releases, never to the webspace.
- SHA256 hashes are shown on the download page for verification.
- CSP header restricts script sources to self plus GoatCounter analytics.

## What Users Can Do

- Always download from the official sources: `https://github.com/JRpersonal/streborn/releases` or `https://st-reborn.de`.
- Verify the SHA256 checksum before running an installer.
- Inspect the source code on GitHub if you are technical.
- Run the desktop app on a network where the SoundTouch lives only.
- Keep the app updated.

## Known Limitations

- Windows installers are currently **not code signed** with an EV certificate. Windows SmartScreen may warn on first download. Plans exist to acquire an EV code signing certificate once donations cover the annual cost.
- macOS builds are **not notarized** yet. Same reason.
- The desktop app does not implement application sandboxing. Future versions may use OS provided sandboxing.

## Changelog

- 2026 May 15: initial security policy.

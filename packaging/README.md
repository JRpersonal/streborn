# Package managers: winget and Homebrew

The desktop app is published to two package managers on top of the GitHub
release and st-reborn.de:

| Platform | Package manager | What users type |
|---|---|---|
| Windows | winget (the Windows Package Manager) | `winget install JRpersonal.STReborn` |
| macOS | Homebrew, own tap | `brew install --cask jrpersonal/tap/st-reborn` |

After the one-time setup below, every real release updates both by itself
(job `package-managers` in `.github/workflows/release.yml`). A preview release
never reaches either. Each half skips with a notice while its token is
missing, and a failure there never turns the release red.

## Why an own tap and not homebrew/cask

Homebrew's own cask collection only takes a cask submitted by the project's
owner once the repository has at least 225 stars, 90 forks or 90 watchers
(docs.brew.sh/Package-Acceptance-Policy). STR is below that, so it ships in its
own tap, `JRpersonal/homebrew-tap`. Users add nothing by hand: the
`jrpersonal/tap/` prefix in the install command taps it automatically.

## One-time setup

### 1. Homebrew tap

1. Create a new **public** repository `JRpersonal/homebrew-tap` (the name must
   start with `homebrew-`). No template, no README needed.
2. Copy `packaging/homebrew/Casks/st-reborn.rb` from this repository into it
   as `Casks/st-reborn.rb` and commit it on `main`.
3. Create a fine-grained token at
   https://github.com/settings/personal-access-tokens/new:
   - Name: `STR release -> homebrew-tap`
   - Repository access: only `JRpersonal/homebrew-tap`
   - Repository permissions: **Contents: Read and write** (Metadata read-only
     is added automatically), nothing else
   - Expiration: one year
4. Store it in this repository (`JRpersonal/streborn`) under Settings > Secrets
   and variables > Actions as **`HOMEBREW_TAP_TOKEN`**.

Check on a Mac: `brew install --cask jrpersonal/tap/st-reborn`.

### 2. winget

winget only accepts updates for a package that already exists in
`microsoft/winget-pkgs`, so the first version is submitted by hand.

1. Fork https://github.com/microsoft/winget-pkgs to your account
   (`JRpersonal/winget-pkgs`). The release workflow opens its pull requests
   from this fork.
2. Submit v1.0.2 from the prepared manifests in
   `packaging/winget/manifests/j/JRpersonal/STReborn/1.0.2/`:
   - copy that folder to the same path in your fork, on a new branch,
   - `winget validate --manifest <path to the 1.0.2 folder>` must succeed,
   - optional local test: `winget install --manifest <path>` (needs
     `winget settings --enable LocalManifestFiles` once, as admin),
   - open the pull request against `microsoft/winget-pkgs` and wait until the
     maintainers merge it (usually a few days; their bot runs the checks).
3. Create a **classic** token at https://github.com/settings/tokens/new
   (komac needs `public_repo` to push to your fork and open the PR; the
   fine-grained kind cannot open pull requests on repositories you do not own):
   - Note: `STR release -> winget-pkgs`
   - Scope: **`public_repo`** only
   - Expiration: one year
4. Store it in this repository as **`WINGET_TOKEN`**.

From then on each release opens a pull request in `microsoft/winget-pkgs`
with the new version; the winget maintainers merge it.

## What the release job does

- **winget**: downloads komac 2.16.0 (checked against its recorded SHA256)
  and runs `komac update JRpersonal.STReborn --version X.Y.Z --urls
  .../releases/download/vX.Y.Z/STR-Windows.exe --submit`.
- **Homebrew**: reads the SHA256 of `STR-macOS.dmg` from the release's
  `SHA256SUMS`, sets `version` and `sha256` in `Casks/st-reborn.rb` of the tap
  and pushes the commit.

## Things worth knowing

- **winget installs the portable exe**: it lands under
  `%LOCALAPPDATA%\Microsoft\WinGet\Packages\` and is started with the command
  `streborn` (or `ST Reborn` is pinned by hand). winget does not create a
  Start menu entry for a portable app.
- **Both apps also update themselves.** The cask carries `auto_updates true`,
  so `brew upgrade` leaves an app alone that already updated itself. winget
  may still list an update until its own version record catches up; running
  `winget upgrade` then simply installs the same version again.
- The tokens expire after a year. GitHub mails a reminder; renew them and
  replace the two secrets.

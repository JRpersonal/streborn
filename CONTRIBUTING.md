# Contributing to STR (SoundTouch Reborn)

Thanks for considering a contribution. STR is a small project that
keeps a discontinued piece of consumer audio hardware alive, and
every model we get tested by a real user makes it more useful.

## Ways to help

- **Test on your speaker model.** ST10 is the reference target.
  Reports are a contribution, even just "ST20 works, presets
  survive standby cycle". Open a [Discussion][discussions] under
  Hardware, or attach to an existing thread.
- **File a bug.** Use Issues for things that are reproducibly
  broken. Use [Discussions][discussions] for questions, ideas,
  setup help, or "is this expected?".
- **Improve documentation.** README, `docs/`, FAQ on the website,
  or the German translation of any of those.
- **Send a code change.** See below.

[discussions]: https://github.com/JRpersonal/streborn/discussions

## Before opening a code PR

1. Open a Discussion or Issue first if the change is non-trivial
   (more than a typo, more than a one-file fix). It saves both
   sides time and avoids parallel work.
2. Read [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the
   component map, tech stack, port table, and the sequence
   diagrams showing discovery, playback, marge emulation, install,
   and OTA. It is the shortest path to understanding how the
   pieces interact.
3. Read [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md) if you are
   touching anything under `internal/autopair/`,
   `internal/tlsgen/`, `usb-stick/setup-tls.sh`, or
   `usb-stick/iptables-setup.sh`.

## Local development

Requirements: Go 1.22+, Node 20+, [Wails CLI v2][wails], `make`.
Linux is the smoothest target; macOS and Windows work but have
extra Wails toolchain dependencies.

[wails]: https://wails.io/docs/gettingstarted/installation

```bash
git clone https://github.com/JRpersonal/streborn.git
cd streborn

# Sanity check
go build ./...
go test ./...

# Stick agent for the speaker hardware
make build-arm

# Desktop app, dev mode with hot reload
cd desktop-app
wails dev

# Website
cd ../website
npm ci
npm run dev
```

The `desktop-app/agentbin/streborn-armv7l` and
`sticksetup/embedded/winformat.exe` files are empty stubs in the
repo. CI overwrites them with the real binaries during release.
On a developer machine `agentbin.Available()` returns `false`, and
the desktop app falls back to a configured external path.

## Code style and conventions

- **Language.** All code, comments, identifiers, commit messages,
  and PR descriptions are in English. User-facing UI strings live
  in i18n bundles (English and German first-class).
- **Go.** `gofmt` clean, `go vet ./...` clean, `golangci-lint`
  clean. Tests in `_test.go` next to the code they cover. Logging
  via `log/slog`.
- **Frontend.** Whatever Wails generated. Small project, no extra
  framework opinions.
- **Commits.** Imperative mood, present tense. One logical change
  per commit. Reference the Issue or Discussion if there is one:
  `Fix preset reconcile loop on standby (#42)`.
- **No emoji** in code, commits, or PR descriptions unless the
  change is specifically about UI emoji.

## PR checklist

Tick these in your PR description:

- [ ] `go vet ./...` and `go test ./...` pass locally.
- [ ] If the change touches the stick agent, I have either tested
      on real hardware or noted in the PR that I have not.
- [ ] No personal data, real LAN IPs, MAC addresses, or device
      serial numbers were added.
- [ ] If a new dependency was added, it is on a current, supported
      version.

## Licensing

STR is MIT licensed. By submitting a contribution, you agree to
license it under the same terms. The full text is in
[`LICENSE`](LICENSE).

## Conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
Be civil, disagree on the technical merits, assume good intent.
Report incidents to the address listed in
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## Maintainers

See [`.github/CODEOWNERS`](.github/CODEOWNERS) for the current
owners of each part of the tree.

## Support the project

If you cannot contribute code but want to help anyway, a donation
keeps the lights on.

[![GitHub Sponsors](https://img.shields.io/github/sponsors/JRpersonal?label=Sponsor%20on%20GitHub&logo=GitHub&color=ea4aaa)](https://github.com/sponsors/JRpersonal)

More payment options are listed on [st-reborn.de](https://st-reborn.de/#donate).

# Contributing to STR (SoundTouch Reborn)

Thanks for considering a contribution. STR is a small project that
keeps a discontinued piece of consumer audio hardware alive, and
every model we get tested by a real user makes it more useful.

## Ways to help

- **Test on your speaker model.** ST10 is the reference target.
  Reports — even just "ST20 works, presets survive standby cycle"
  — are a contribution. Open a [Discussion][discussions] under
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
2. Read [`CLAUDE.md`](CLAUDE.md). It is written for AI assistants
   but is also the shortest tour of the architecture, conventions,
   and runtime quirks. Sections that matter for contributors:
   *Conventions in this repo*, *What never goes into this repo*,
   *Build and embed quirks*, *Runtime quirks worth remembering*.
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
the desktop app falls back to a configured external path. See
*Build and embed quirks* in [`CLAUDE.md`](CLAUDE.md).

## Code style and conventions

- **Language.** All code, comments, identifiers, commit messages,
  and PR descriptions are in English. User-facing UI strings live
  in i18n bundles (English and German first-class).
- **Go.** `gofmt` clean, `go vet ./...` clean, `golangci-lint`
  clean. Tests in `_test.go` next to the code they cover. Logging
  via `log/slog`.
- **Frontend.** Whatever Wails generated — small project, no extra
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
- [ ] If the change touches the website, I built it locally and
      checked that legal pages still render.
- [ ] No personal data, real LAN IPs, MAC addresses, or device
      serial numbers were added (see *What never goes into this
      repo* in `CLAUDE.md`).
- [ ] If a new dependency was added, it is on a current, supported
      version.

## Licensing

STR is MIT licensed. By submitting a contribution, you agree to
license it under the same terms. The full text is in
[`LICENSE`](LICENSE).

## Conduct

Be civil. Disagree on the technical merits. Assume good intent.
Maintainers will close threads that turn into personal attacks.

## Maintainers

See [`.github/CODEOWNERS`](.github/CODEOWNERS) for the current
owners of each part of the tree.

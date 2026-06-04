# Release notes pipeline

How "what changed" reaches users, end to end, and what the website
(separate repo `JRpersonal/streborn-website`) has to implement.

## One source of truth

Release notes are generated once, at release time, from the
Conventional Commit subjects since the previous tag. They are written
into two places that everything else reads:

1. The **GitHub Release body** (human-readable, on the Releases page).
2. The **`notes` field of `manifest.json`** (machine-readable), which is
   a release asset and is what the website and the desktop app consume.

Nothing scrapes the Releases UI. There is no second, hand-maintained
changelog to keep in sync.

## How it is generated (this repo)

`cmd/relnotes` parses `git log <previous-tag>..<this-tag>` and keeps only
user-facing commit types: `feat` -> New features, `fix` -> Fixes,
`perf` -> Performance, plus anything marked breaking (`type!: ...`).
Noise (`chore`, `ci`, `build`, `test`, `style`, `refactor`, `docs`) is
dropped so the reader sees only what tells them whether to upgrade.

The release workflow (`.github/workflows/release.yml`) runs it in the
`release` job:

- `dist/changelog.md` is inserted near the top of the GitHub Release body.
- `dist/notes.json` (`{ "markdown": "...", "items": [...] }`) is merged
  into `manifest.json` under `notes`.

## manifest.json shape (the contract)

`manifest.json` is published as a release asset and is reachable at the
stable URL:

```
https://github.com/JRpersonal/streborn/releases/latest/download/manifest.json
```

It now contains a `notes` object in addition to the existing fields:

```jsonc
{
  "version": "v0.6.22",
  "build": "2026-06-04-1200",
  "released_at": "2026-06-04T12:00:00Z",
  "commit": "…",
  "artifacts": { "desktop_windows": { "url": "…", "sha256": "…" }, … },
  "notes": {
    "markdown": "## What's changed in v0.6.22\n\n### New features\n\n- …\n",
    "items": [
      { "type": "feat", "scope": "i18n", "summary": "Add Lithuanian", "breaking": false, "commit": "abc123def" },
      { "type": "fix",  "scope": "frontend", "summary": "Sort language filter", "breaking": false, "commit": "…" }
    ]
  }
}
```

`notes.markdown` is a small, safe subset (h2/h3 headings and `-` bullet
lists only). `notes.items` is the same content structured, for richer
rendering.

## What the website has to implement

### 1. `update-check.php` (the endpoint the app already calls)

The desktop app calls, on startup:

```
GET https://st-reborn.de/api/update-check.php?v=<ver>&b=<build>&os=<goos>&arch=<goarch>&lang=<locale>
```

It expects a small JSON object. The app already reads `version`,
`downloadUrl`, and **`notes`**. To surface the change list in the app's
update banner, the endpoint must include `notes` (the Markdown string):

```json
{
  "version": "v0.6.22",
  "build": "2026-06-04-1200",
  "downloadUrl": "https://st-reborn.de/download/windows",
  "notes": "## What's changed in v0.6.22\n\n### New features\n\n- …\n"
}
```

Implementation: fetch the latest `manifest.json` (cache it; it changes
only on release), pick `downloadUrl` from `artifacts` by the `os`/`arch`
query params, and pass `notes.markdown` through as `notes`.

Constraints the app relies on:
- Keep `notes` reasonably small; the app reads at most 16 KB of the whole
  response and renders only headings and bullet lines.
- `notes` is optional. If omitted, the app shows the version and a
  download button with no "What's new" section. Forward-compatible: the
  app already ships this behaviour.
- `lang` is sent for future localized notes. For now notes are English
  (generated from English commit messages); the endpoint can ignore
  `lang` or echo English regardless.

### 2. A changelog / releases page

Render a public changelog from `manifest.json` (latest release) and,
ideally, an archive of past releases. `notes.items` gives you typed
entries (feat/fix/perf, scope, summary) to group and style; or render
`notes.markdown` directly. This page is what "automatically visible on
the website" means: it updates itself on every release because it reads
the manifest, which the release pipeline regenerates.

The website is already notified on each release via
`repository_dispatch` (`event-type: app-release-published`, payload
`{tag,url,build,commit}`); the deploy can re-fetch `manifest.json` then.

## What the desktop app already does (this repo)

- `CheckAppUpdate` (desktop-app/app.go) forwards the endpoint's full JSON
  to the frontend, including `notes`.
- The update banner (desktop-app/frontend/src/main.js, `checkAppUpdate` +
  `renderUpdateNotes`) shows a collapsible "What's new" with the curated
  list, rendered through a safe minimal Markdown subset.

## Future: localized notes

The app sends `lang`. To localize, translate `notes.markdown` /
`notes.items[].summary` at release time (or in `update-check.php`) and
return the translation matching `lang`, falling back to English. Out of
scope for the first iteration by decision; English-from-commits ships
first.

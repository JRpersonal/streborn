# Share buttons ("Recommend STR")

## Single source of truth

Share targets are defined in exactly one place: the website registry
`website/src/data/share-targets.json` in `JRpersonal/streborn-website`,
published as `https://st-reborn.de/share-targets.json`. Its schema and
build-time rules live next to it in `website/src/lib/share.ts`.

The desktop app has no list of its own. A copy of the registry is committed at
`desktop-app/frontend/src/data/share-targets.json` and refreshed with

```
make share-targets                                   # from st-reborn.de
make share-targets SHARE_TARGETS_FILE=<path>         # from a website checkout
```

The sync script (`desktop-app/frontend/scripts/sync-share-targets.mjs`) checks
the file with the same rules as the website build
(`desktop-app/frontend/src/shareRegistry.js`) and aborts without touching the
committed copy on any error. The app therefore makes no network request for the
registry at runtime. A new target, a changed order or `enabled: false` reaches
the app with a sync and a rebuild, no code change.

A new entry that uses `label` or `note` needs its text key in every app bundle
first (`share.labels.<label>`, `share.notes.<note>`); the sync refuses the
registry until the English bundle has it.

## Where the app shows the buttons

- **Footer menu item "Recommend STR"**: always there, opens a dialog with the
  targets grouped as in the registry (social, messenger, forum, direct).
- **Install success**: one quiet line plus the buttons under the success
  message, the first time an install succeeds on this computer. The decision is
  stored in `localStorage` (`shareOfferShown`); if storage is unavailable the
  row is not shown at all rather than risk showing it twice.

Nothing else: no banner, nothing at app start, no reminder, no coupling to
donations, updates or features.

## Behaviour

- Links open in the system browser via Wails `BrowserOpenURL`, like every other
  external link in the app. Values are encoded strictly (see
  `mailtourl.test.js`) so Wails accepts every URL.
- The shared URL is the website start page in the UI language
  (`https://st-reborn.de/` for English, `https://st-reborn.de/<lang>/`
  otherwise, `zh-Hant` maps to `/zh-tw/`), without any parameter.
- `copy`: clipboard with a visible confirmation; if both the web and the native
  clipboard fail, a read-only field with the link is shown and selected.
- `instance-prompt` (Mastodon): asks once inline for the instance, accepts only
  a bare host name, remembers it (`str-mastodon-instance`) and offers to forget
  it.
- `note`: shown as text under its target and linked via `aria-describedby`. The
  Reddit note shows whenever the UI language differs from `shareLocale`.
- Every button has a visible name and an accessible label ("Share on X"); the
  dialog traps Tab, closes on Escape and returns focus.

## Hard rules

- Reddit targets keep `allowBody: false`. Identical prewritten posts with the
  same link are treated as coordinated spam and get the domain banned. Both the
  website build and the app sync enforce it.
- No auto posting, platform APIs, login or OAuth. Every target is a link.
- No click counting in the app. The website counts its own clicks with
  GoatCounter; the app sends nothing.

## Texts

The app bundles carry the website's share texts under `share.*` (`on`,
`copied`, `copyFailed`, `mastodonPrompt`, `mastodonChange`, `postTitle`, `text`,
`labels.*`, `notes.*`, `groups.*`), taken from
`website/src/i18n/share.ts`, plus two app-only keys: `share.menu` and
`share.successLine`. `share.test.js` fails if any UI language misses one.

## Phone remote

The phone remote served by the agent (`internal/webui/assets/index.html`) has
no list of its own either. The sync script also writes
`internal/webui/assets/share.json`: the buttons already resolved per remote
language, with the same `resolveShareTargets` and the app texts, plus the
glyphs in use. The agent embeds that file and serves one language at a time
on `GET /share.json?lang=xx`; the page only draws what it gets, in its own
"Recommend STR" card (collapsed, separate from the donate card). Links open in
a new tab, copy falls back to a selectable field (the remote runs on plain
http, where the async clipboard API does not exist), Mastodon asks once inline.

`share.test.js` fails when `share.json` no longer matches registry and texts,
and `internal/webui/share_test.go` fails when a remote language has no buttons
or a share intent URL appears in the page. Either way: `make share-targets`.

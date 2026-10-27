# UI test suite (Playwright)

Browser tests for the STR desktop frontend and the phone remote. The built
frontend runs in a real Chromium with every Go binding (`window.go.main.App.*`)
and the Wails runtime replaced by a mock backend, and every agent HTTP call
answered from demo data. No speaker, no Wails, no network: a net guard aborts
every request that would leave the preview server.

It is a package of its own on purpose. Playwright and its browser download
live here and never in the app's `package.json`, so the app's `frontend` CI job
and the wails build never install them.

## Running it

```bash
# once, from desktop-app/frontend (vite builds and serves the app):
npm install
# once, here:
cd e2e
npm install
npx playwright install chromium

npx playwright test                                   # everything, port 4179
E2E_PORT=4180 npx playwright test specs/desktop-smoke.spec.mjs   # one file on its own port
E2E_PORT=4180 E2E_SKIP_BUILD=1 npx playwright test specs/desktop-smoke.spec.mjs   # reuse that port's build
npx playwright test --project=desktop                 # one project
E2E_WEBKIT=1 npx playwright test specs/desktop-smoke.spec.mjs    # adds webkit-smoke (npx playwright install webkit)
```

The `VAR=value npx ...` lines are bash (Git Bash on Windows). In PowerShell
set the variable first; it stays set for the rest of that shell:

```powershell
$env:E2E_PORT=4180; npx playwright test specs/desktop-smoke.spec.mjs
$env:E2E_SKIP_BUILD=1; npx playwright test specs/desktop-smoke.spec.mjs
Remove-Item Env:E2E_SKIP_BUILD          # back to building
```

Tests run `fullyParallel`: every test boots its own page on its own mock, so
one file spreads over all workers. A spec must not rely on the order of its
tests or on state another test left behind.

The web server builds the frontend into `desktop-app/.e2e-out/dist-<PORT>` and
serves it with `vite preview`. Results, traces and the HTML report (CI only)
land in `desktop-app/.e2e-out/results-<PORT>` and `report-<PORT>`. Nothing
goes into `frontend/dist`, so the `go:embed` anchor `dist/.gitkeep` survives a
run, and two runs on two ports never share a build.

The server is never reused: if the port already answers (another checkout's
preview, or `screenshots/ui2-check.mjs`, which also defaults to 4179), the run
stops with "already used". Pick another `E2E_PORT`.

A failed test keeps its trace: `npx playwright show-trace <results dir>/.../trace.zip`.

## Projects

| project | viewport | runs |
|---|---|---|
| `desktop` | 1240 x 740, the app's default window | `desktop-*.spec.mjs` |
| `desktop-min` | 1000 x 600, the smallest window | `desktop-layout`, `desktop-i18n`, `desktop-banners` |
| `phone` | 390 x 844, iPhone UA, touch | `phone-*.spec.mjs` |
| `webkit-smoke` | Desktop Safari, only with `E2E_WEBKIT=1` | `desktop-smoke.spec.mjs` |

Playwright matches `testMatch` against the absolute path, so every pattern in
`playwright.config.mjs` starts at a path separator (`[\\/]desktop-...`, `/` or `\`).
Without that, `desktop-` also matches the `desktop-app/` directory and the
desktop project would pull in every spec. Check with
`npx playwright test --list --project=desktop`.

## Writing a spec

```js
import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle } from '../helpers/clock.mjs';

test.use({ ui2: true, fakeClock: true, scenario: { bindings: { SetBoxVolume: { value: null, delayMs: 50 } } } });

test('volume goes to the current speaker', async ({ app, mock }) => {
  await settle(app.page, 9_000);
  await mock.resetCalls();
  // ... drive the page ...
  await mock.expectCalled('SetBoxVolume', { pred: onHost('demobox.local') });
});
```

**Fixtures** (`fixtures.mjs`): `app` boots the desktop frontend and waits for
the layout's ready marker (`{ page, api, errors, scenario }`, where `api.calls`
are the agent HTTP requests). `mock` is the Node side of the mock
(`helpers/mockclient.mjs`). `phone` boots the phone remote
(`{ page, handler, errors }`). Every test fails on an uncaught page error;
console errors are attached to the report.

**Options** (`test.use`): `ui2`, `theme` (dark, light, contrast), `appLocale`,
`scale` ('1' to '3'), `scenario` (a patch, below), `fakeClock`, `ready` (a
selector that means "booted"), `preInit` (an init script that runs before the
mock, for example one that makes storage throw), and for the phone
`phoneLang`, `phoneView`, `phoneGrouped`, `phoneOverrides`.

**The mock backend** lives in `../testkit/`, shared with the screenshot
harness: `demo-data.mjs` (fixtures and the speaker roster), `scenario.mjs`
(what a run looks like, and `bannerOverrides`, the one place that knows what
makes each notice appear), `desktop-mock.mjs` (the in-page model and its
`window.__strMock`), `agent-routes.mjs` and `phone-mock.mjs`.

The roster: `livingRoom` (current, playing, `demobox.local:8888`), `bedroom`
(192.0.2.52, standby, volume 18), `office` (192.0.2.53, offline), `kitchen`
(192.0.2.51:8090, Bose original) and `oldAgent` (Hallway, 192.0.2.54, agent
v1.0.10, so it needs an update). Default boxes: the first four.

The UI 2.0 Speakers area (phase 3) has a network of its own:
`scenario: speakersScenario()` (`testkit/scenario.mjs`) gives ten speakers,
one in every state the area shows: playing (`livingRoom`), standby
(`bedroom`), update ready (`oldAgent`), starting up (`nursery`, `strSilent`),
not reachable (`office`), a stereo pair (`terraceLeft` + `terraceRight`), a
stored fixed group (`dining` leads, `hall` follows) and a Bose original
(`kitchen`). `SPEAKERS3.states` in `testkit/demo-data.mjs` names the roster
key of each state.

A binding the model does not answer is overridden in the spec:
`scenario: { bindings: { Name: spec } }`, where spec is `{ value }`,
`{ error }`, `{ never: true }` or `{ sequence: [spec, ...] }`, optionally with
`delayMs` (page clock), `times` (first n calls) and `byHost`. An agent path is
overridden through `scenario: { routes: { '/api/path': { status, body } } }`.
Notices: `scenario: { banners: { appUpdate, firewall, speakerUpdate, noInternet, security } }`.

The UI 2.0 sidebar lists every speaker (`#ui2SpkLists`, one `.ui2-spk-row`
each, a stereo pair as one row, the playing group pinned on top). A row's
`.ui2-spk-pick` chooses the speaker you control, its `.ui2-spk-act` (`+` or
`-`) edits the group. Group volume and the per-speaker Levels
(`#ui2LevelsBtn`, `#ui2LevelsPop`) live in the player. There is no room
switcher and no Output popover any more.

**Who touches what.** A spec writes only its own spec file and the baseline
file it owns. The shared files (`../testkit/*`, `playwright.config.mjs`,
`fixtures.mjs`, `helpers/*`, `package*.json`, this README, `build.yml`,
`eslint.config.js`, `.gitignore`, `dependabot.yml`) change in their own
commit. A real bug a spec finds becomes `test.fixme(<reason>)`, where the
reason says what breaks and where (the `*_BUG` constants at the top of a
spec). Add `see #N` once an issue exists; that is recommended, not enforced.
Only the cause of an app bug is a reason for `test.fixme`; a test that cannot
be made stable is a bug in the test.

## Flake policy

- **Waiting.** No `waitForTimeout` and no `setTimeout` in specs; the lint
  config enforces both. Use web-first assertions, and `expect.poll` (or
  `mock.expectCalled`) for call counts outside the fake clock.
- **Time.** Polls, debounces, reconnect timers and banner delays run under
  the `fakeClock` option and `helpers/clock.mjs` `settle`. A spec that sets
  `delayMs` must advance the clock. Under `runFor`, count `window.go`
  bindings only, never agent `page.route` calls.
- **Retries.** CI retries once, local runs never. A test that only passes on
  retry is a bug: fix it, or quarantine it with `test.fixme` plus an issue
  within a week. Never raise retries. Do not raise the 30 s test or 5 s expect
  timeouts without a comment saying why.
- **Determinism.** Fixed viewport, timezone Europe/Berlin, locale en-US,
  `reducedMotion: 'reduce'`, the `KILL_ANIM` stylesheet (`helpers/a11y.mjs`)
  before axe and geometry checks, clock start 2026-10-23T18:00:00Z.
- **Network.** Nothing leaves the browser. Every test fails on an uncaught
  `pageerror`.
- **No visual snapshots** in phase 1: system font stacks differ between
  Windows and Linux. Revisit if the app ever bundles its own web font.

## Baselines

`baselines/` holds what a run is allowed to find.

- `a11y-desktop.json`, `a11y-phone.json`: axe findings per scan key, each with
  a node count and a note (`helpers/a11y.mjs`). More than the baseline fails,
  less fails too ("stale, lower it"), and an entry without a real note fails
  (empty, or still starting with `TODO`). A note says why the finding is
  accepted; an issue number is recommended, many notes say "no issue filed".
  Refresh with `E2E_UPDATE_A11Y=1 npx playwright test --workers=1 <spec>`,
  review the diff, write the notes.
- `unmocked-desktop.json`, `unmocked-phone.json`: the binding names (desktop)
  and agent paths (phone) a strict run hits without a model. A new name fails,
  a listed name that was not hit fails as stale. Refresh with
  `E2E_UPDATE_UNMOCKED=1`. These lists only shrink: model a name in testkit
  instead of listing it.

**An `@axe-core/playwright` bump** (Dependabot, group `playwright`) can change
what the scans report. In the Dependabot PR run
`E2E_UPDATE_A11Y=1 npx playwright test --workers=1 specs/desktop-a11y.spec.mjs specs/phone-smoke.spec.mjs`,
review the baseline diff, give every new entry a note, and commit it to that PR.

# Screenshot harness

Dev-only tool that renders the STR desktop frontend in headless Chromium
with the Go backend **mocked** (deterministic demo data, no speaker, no
Wails), sets the UI language, drives each screen, and captures one PNG per
view per language into `../../../docs/screenshots/<lang>/`.

It is deliberately isolated from the app's `package.json`: its only
dependency is `playwright`, declared here, so the app's CI / wails build
never installs playwright (which would download ~130 MB of browsers).

## Usage

```bash
# once, from desktop-app/frontend (for vite):
npm install

# then, here:
cd screenshots
npm install            # playwright + browser
npm run shoot          # builds the frontend, serves it, captures all views
```

`shoot.mjs` builds the frontend with the parent's own vite, serves it with
`vite preview`, and screenshots every view in every language. It touches no
app source; the mocks live in `../testkit/`.

Narrow a run with `SHOOT_LANGS=en,de` and/or `SHOOT_VIEWS=app-listen,app-recent`.
The UI 2.0 preview shots (`app-ui2-dark`, `app-ui2-light`, `app-ui2-contrast`,
`app-ui2-xl`) are not part of a full run; `SHOOT_VIEWS=ui2` takes all four.
`SHOOT_VIEWS=ui2p2` shoots the phase 2 views (Radio, its station rows, Library,
Recently played, Spotify) in the dark and the light theme, and
`SHOOT_OUT=<dir>` writes a run outside `docs/screenshots`, for review shots.
The UI 2.0 shots use the app's default window (1240 x 740) and a simulated
network of eleven speakers (a group of two playing, a stereo pair, an offline
speaker, a Bose original, idle speakers), so the sidebar speaker list has to
scroll. The demo data and the mocks live in `../testkit/`, shared with the
Playwright suite in `../e2e/`; `demo.mjs` holds the scenario each shot runs.

## UI 2.0 check (CI)

```bash
npm run ui2-check      # builds, serves and checks the UI 2.0 preview
```

`ui2-check.mjs` drives the preview against the same simulated speakers and
asserts what the sidebar promises (one row per speaker, the pair as one row,
Offline in words, `+` and `-` where the group edit acts, arrow keys, the
pinned group and the navigation staying put while the list scrolls, Levels in
the player, and that `+` edits the group it was clicked in). CI runs it in the
`UI 2.0 preview (simulated speakers)` job of `build.yml`.

The setup views (`app-install`, `app-stick-step1`, `app-stick-step2`) add a
stock speaker to the demo network so the network-install card has a target;
the stick shots open the "Prepare a USB stick instead" fallback below it.

## Phone remote

```bash
npm run shoot-phone    # writes ../../../docs/screenshots/phone/<lang>/<view>.png
```

`shoot-phone.mjs` renders the phone remote, the self-contained page every
speaker serves (`internal/webui/assets/index.html`), read straight from the
repo. It needs no build and no server: Playwright intercepts every request to
a placeholder origin (`http://demobox.local:8888`), answers `/` with that page
and every `/api/*` call (plus the radio-browser.info search the page makes from
the phone) with demo data, and aborts anything else, so no real speaker or
network is ever contacted. Viewport 390x844 at 2x, languages `en` and `de` by
default. Calls the harness does not mock are listed at the end of a run.


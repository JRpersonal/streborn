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
app source; the mocks live only in this script.

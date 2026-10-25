// testkit/phone-mock.mjs: everything the phone remote (the page every STR
// speaker serves, internal/webui/assets/index.html) asks the network for,
// answered from demo data. Runs in Node as a Playwright route handler for
// '**/*':
//
//   const phone = phoneHandler({ indexHTML, iconPNG });
//   await context.route('**/*', phone.handle);
//   phone.calls      // [{ method, url, t }] every request it answered
//   phone.unmocked   // Set of 'GET /api/x' paths it had no model for (answered {})
//   phone.options    // mutable: grouped, view, overrides
//
// It serves the placeholder origin http://demobox.local:8888, the
// radio-browser.info search the page makes from the phone, and a fake art
// host; every other request is aborted, so nothing leaves the browser.
// overrides maps a path to { status, body } or { abort: true }.

import { PHONE } from './demo-data.mjs';

export function phoneHandler({ indexHTML, iconPNG, view = '', grouped = false, overrides = {} } = {}) {
  const options = { view, grouped, overrides };
  const calls = [];
  const unmocked = new Set();

  async function handle(route) {
    const req = route.request();
    const u = new URL(req.url());
    calls.push({ method: req.method(), url: req.url(), t: Date.now() });
    const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

    if (u.origin === PHONE.ART_HOST) {
      const k = u.pathname.replace(/^.*\//, '').replace(/\.svg$/, '');
      const [letter, bg] = PHONE.COVERS[k] || ['?', '#555'];
      return route.fulfill({ status: 200, contentType: 'image/svg+xml', body: PHONE.coverSVG(letter, bg) });
    }
    if (/radio-browser\.info$/.test(u.hostname)) return json(PHONE.STATIONS);
    if (u.origin !== PHONE.ORIGIN) return route.abort(); // nothing else may leave the browser

    const p = u.pathname;
    const ov = (options.overrides || {})[p];
    if (ov) {
      if (ov.abort) return route.abort('connectionrefused');
      const isText = typeof ov.body === 'string';
      return route.fulfill({
        status: ov.status || 200,
        contentType: ov.contentType || (isText ? 'text/plain' : 'application/json'),
        body: isText ? ov.body : JSON.stringify(ov.body === undefined ? {} : ov.body),
      });
    }
    if (p === '/' || p === '/index.html') {
      // Same substitution the agent does (status_index.go): the speaker's
      // name becomes the page title.
      return route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: indexHTML.replace('<title>ST Reborn</title>', `<title>${PHONE.TITLE}</title>`) });
    }
    if (p === '/icon.png' || p === '/icon-large.png') return route.fulfill({ status: 200, contentType: 'image/png', body: iconPNG });
    if (p === '/manifest.webmanifest') return json({ name: PHONE.TITLE, icons: [] });
    if (p === '/share.json') return route.fulfill({ status: 404, body: '' }); // share card stays hidden

    if (p === '/api/status') {
      return route.fulfill({ status: 200, contentType: 'application/xml', headers: { 'X-STR-Playing-Key': '2' }, body: PHONE.STATUS_PLAYING });
    }
    if (p === '/api/stream/title') return json({ title: 'The Demo Band - Morning Light' });
    if (p === '/api/presets') return json(PHONE.PRESETS);
    if (p === '/api/box/settings') return json(PHONE.SETTINGS);
    if (p === '/api/agent/version') return json(PHONE.VERSION);
    if (p === '/api/alarms') return json(PHONE.ALARMS);
    if (p === '/api/recent') return json(PHONE.RECENT);
    if (p === '/api/favorites') return json(PHONE.FAVORITES);
    if (p === '/api/peers') return json(PHONE.PEERS);
    if (p === '/api/box/zone') return json(PHONE.zoneDoc(options.grouped));
    if (p === '/api/box/zone/volume') return json(PHONE.zoneVolume(options.grouped));
    if (p === '/api/box/sleep') return json({ active: false, minutes: 0 });
    if (p === '/api/queue') return json({ active: false });
    if (p === '/api/position') return json({});
    if (p === '/api/library/servers') return json(PHONE.LIB_SERVERS);
    if (p === '/api/box/mediaservers') return json([]);
    if (p === '/spotify/info') return json({});
    unmocked.add(req.method() + ' ' + p);
    return json({});
  }

  return { handle, calls, unmocked, options };
}

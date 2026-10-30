// testkit/agent-routes.mjs: the agent HTTP endpoints the desktop frontend
// fetch()es directly from a speaker (http://<speaker>:8888/api/...), answered
// with demo data. Runs in Node as a Playwright route handler:
//
//   const api = agentRoutes(resolveScenario(sc));
//   await page.route('**/api/**', api);
//   api.calls   // [{ method, url, t }] every request it answered
//
// scenario.routes overrides one path: { '/api/stick/status': { status, body } }
// answers that (an object body goes out as JSON), { abort: true } fails the
// request the way an unreachable speaker does.

import { DEMO } from './demo-data.mjs';

export function agentRoutes(scenario = {}, demo = DEMO) {
  const overrides = scenario.routes || {};
  const appInfo = Object.assign({}, demo.appInfo, scenario.appInfo || {});
  const calls = [];
  const handler = async (route) => {
    const req = route.request();
    const url = req.url();
    let pathname = '';
    try { pathname = new URL(url).pathname; } catch { pathname = url; }
    calls.push({ method: req.method(), url, t: Date.now() });
    const ov = overrides[pathname];
    if (ov) {
      if (ov.abort) return route.abort('connectionrefused');
      const isText = typeof ov.body === 'string';
      return route.fulfill({
        status: ov.status || 200,
        contentType: ov.contentType || (isText ? 'text/plain' : 'application/json'),
        body: isText ? ov.body : JSON.stringify(ov.body === undefined ? {} : ov.body),
      });
    }
    const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
    if (url.includes('/api/radio/search') || url.includes('/api/radio/top')) return json(demo.stations);
    if (url.includes('/api/radio/languages')) return json(demo.languages);
    if (url.includes('/api/radio/tags')) return json([]);
    if (url.includes('/api/region')) return json({ countryCode: 'US', country: 'United States' });
    if (url.includes('/api/agent/version')) {
      // presetRows follows the speaker the request went to (#1110).
      let host = '';
      try { host = new URL(url).hostname; } catch { host = ''; }
      const box = [...(scenario.boxes || []), scenario.current].find((b) => b && b.host === host);
      return json({ version: appInfo.version, build: appInfo.build, ...(box && box.presetRows ? { presetRows: box.presetRows } : {}) });
    }
    if (url.includes('/api/box/settings')) return json(demo.settings);
    if (url.includes('/api/stick/status')) return json({});
    if (url.includes('/api/debug/state')) return json({});
    return json([]); // safe default for any other /api/* call
  };
  handler.calls = calls;
  return handler;
}

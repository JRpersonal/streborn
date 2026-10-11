// Pandora and iHeartRadio: the music services the speaker firmware plays by
// itself (docs/streaming/us-services.md). The agent's GET /api/us-services
// answer is turned into what the settings card and the Find tab show. Pure
// functions only, so the rules are unit-tested (usservices.test.js).

// The two services, in the order the settings card lists them. `key` is the
// name the Go bindings take, `type` the firmware's source enum. Brand names,
// so the label is the same in every language.
export const US_SERVICES = [
  { key: 'pandora', type: 'PANDORA', label: 'Pandora', experimental: false },
  { key: 'iheart', type: 'IHEART', label: 'iHeartRadio', experimental: true },
];

// The stand-in writes this when a carried-over source had no account name.
const NO_ACCOUNT_NAME = '(no account name)';

// usServiceState reads one service out of a GET /api/us-services answer.
//
// state is one of:
//   'unknown'  no answer (older agent, speaker unreachable)
//   'off'      the service is not enabled on this speaker (outside the US
//              without the opt-in)
//   'unlinked' enabled, no account on the speaker
//   'linked'   the speaker holds an account but does not list the source as
//              READY yet (it logs in and registers it, which takes a while)
//   'ready'    the speaker lists the source as READY with an account
//
// account is the masked account name the agent reports, '' when it has none.
export function usServiceState(status, type) {
  if (!status || typeof status !== 'object') return { state: 'unknown', account: '' };
  const stand = status.standIn || {};
  const svc = (Array.isArray(stand.services) ? stand.services : []).find(s => s && s.type === type) || {};
  const box = (status.boxSources && status.boxSources[type]) || {};
  const mode = svc.mode || (stand.us ? 'us' : (stand.optIn ? 'optin' : 'off'));
  let account = String(svc.username || '').trim();
  if (!account && svc.carriedOver && svc.carriedOver !== NO_ACCOUNT_NAME) account = String(svc.carriedOver).trim();
  if (mode === 'off') return { state: 'off', account };
  const hasBoxAccount = box.hasAccount === 'true' || box.hasAccount === true;
  const boxReady = String(box.status || '').toUpperCase() === 'READY';
  if (hasBoxAccount && boxReady) return { state: 'ready', account };
  if (hasBoxAccount || svc.registered) return { state: 'linked', account };
  return { state: 'unlinked', account };
}

// usServicesRegionIsUS reports whether STR treats the speaker as a US one,
// where both services are on without the opt-in.
export function usServicesRegionIsUS(status) {
  if (!status || typeof status !== 'object') return false;
  const region = status.region || {};
  if (String(region.country || '').toUpperCase() === 'US') return true;
  return !!(status.standIn && status.standIn.us);
}

// usServicesOptIn reports whether the outside-the-US opt-in is switched on.
export function usServicesOptIn(status) {
  return !!(status && status.standIn && status.standIn.optIn);
}

// usServiceStatusKey picks the i18n key for a service's status line.
export function usServiceStatusKey(st) {
  const named = !!(st && st.account);
  switch (st && st.state) {
    case 'ready': return named ? 'usServices.stateReady' : 'usServices.stateReadyNoName';
    case 'linked': return named ? 'usServices.stateLinked' : 'usServices.stateLinkedNoName';
    case 'off': return 'usServices.stateOff';
    default: return 'usServices.stateUnlinked';
  }
}

// usServiceCanDisconnect says whether a row offers Disconnect instead of the
// login form.
export function usServiceCanDisconnect(st) {
  return !!st && (st.state === 'linked' || st.state === 'ready');
}

// pandoraFindVisible is the rule for the Find tab's Pandora entry: only while
// the speaker lists Pandora as READY with an account.
export function pandoraFindVisible(status) {
  return usServiceState(status, 'PANDORA').state === 'ready';
}

// pandoraStationRows turns the agent's station list into the rows the Find
// tab draws. A station without a location cannot be played and is dropped.
export function pandoraStationRows(list) {
  const stations = list && Array.isArray(list.stations) ? list.stations : (Array.isArray(list) ? list : []);
  return stations
    .filter(s => s && String(s.location || '').trim())
    .map(s => ({
      name: String(s.name || s.itemName || '').trim() || String(s.location).trim(),
      location: String(s.location).trim(),
      type: String(s.type || '').trim() || 'stationurl',
      art: /^https?:\/\//i.test(String(s.art || '')) ? String(s.art) : '',
      presetable: s.presetable !== false,
    }));
}

// agentErrorCode pulls the agent's JSON "code" out of a binding error
// ("status 409: {...,"code":"not-linked"}"), '' when there is none.
export function agentErrorCode(err) {
  const m = /"code"\s*:\s*"([^"]+)"/.exec(String((err && err.message) || err || ''));
  return m ? m[1] : '';
}

// agentErrorText pulls the agent's JSON "error" out of a binding error, or
// returns the error as it is.
export function agentErrorText(err) {
  const s = String((err && err.message) || err || '');
  const m = /"error"\s*:\s*"((?:[^"\\]|\\.)*)"/.exec(s);
  if (!m) return s;
  try { return JSON.parse('"' + m[1] + '"'); } catch { return m[1]; }
}

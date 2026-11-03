// stationdns.js: the speaker could not look up a station's address.
//
// The agent's stream proxy reports a failed lookup of the station's host as
// reason "dns" in /api/stream-status (internal/streamproxy/dnsfail.go): the
// router's name server failing (SERVFAIL, timeouts) or an ad-blocker such as
// Pi-hole or AdGuard answering for the host. Support evidence (#1084 #1043
// #823 and mail): "one station is silent", "radio stops after seconds", and
// the app blamed the station. A single blocked host never trips the speaker's
// internet verdict, so the no-internet banner stays quiet; this is the only
// place the cause gets named.
//
// A Find stations play already polls stream-status and words the reason
// (search.js streamErrorMessage). A preset key press did not look at all: the
// speaker took the command, so the app said nothing while the key stayed
// silent. watchStationDns is that look, kept small: a few polls in the first
// seconds after the press, stopping at the first answer about the station.

// STATION_DNS is the stream-status reason of a failed lookup.
export const STATION_DNS = 'dns';

function hostOf(u) {
  try {
    return new URL(String(u || '')).hostname.toLowerCase();
  } catch {
    return '';
  }
}

// sameStation: the failure belongs to the station that was started. The agent
// may report the healed URL of a preset rather than the stored one, so the
// same host counts as the same station.
export function sameStation(failedURL, startedURL) {
  if (!failedURL || !startedURL) return false;
  if (failedURL === startedURL) return true;
  const h = hostOf(failedURL);
  return h !== '' && h === hostOf(startedURL);
}

// isStationDnsFailure: a stream-status answer says the lookup of the started
// station's host failed.
export function isStationDnsFailure(data, startedURL) {
  return !!(data && data.error && data.reason === STATION_DNS && sameStation(data.url, startedURL));
}

// watchStationDns polls stream-status a few times after a station started
// and resolves to the failure when the station's lookup failed, or null when
// the station played, failed some other way, the window ran out, or the user
// moved on (stillCurrent false). Any fetch error ends the watch: best-effort.
export async function watchStationDns({
  fetchStatus, url, stillCurrent = () => true,
  windowMs = 6000, intervalMs = 800,
  sleep = (ms) => new Promise(r => setTimeout(r, ms)),
  now = () => Date.now(),
}) {
  if (!url || typeof fetchStatus !== 'function') return null;
  const deadline = now() + windowMs;
  while (now() < deadline) {
    await sleep(intervalMs);
    if (!stillCurrent()) return null;
    let data;
    try {
      data = await fetchStatus();
    } catch {
      return null;
    }
    if (!data || !data.error || !sameStation(data.url, url)) continue;
    return isStationDnsFailure(data, url) ? data : null;
  }
  return null;
}

// isRadioPreset: a preset whose key the agent serves by looking up a station
// host by name, so a failed lookup can be its cause. Spotify, queue and
// native-service keys carry no station URL, and a URL with an IP address (a
// music server on the LAN) involves no lookup.
export function isRadioPreset(p) {
  if (!p || !p.stream_url) return false;
  if (p.type === 'spotify' || p.type === 'queue' || p.type === 'native') return false;
  const u = String(p.stream_url);
  if (!/^https?:\/\//i.test(u)) return false;
  const h = hostOf(u);
  return h !== '' && !h.startsWith('[') && !/^\d{1,3}(\.\d{1,3}){3}$/.test(h);
}

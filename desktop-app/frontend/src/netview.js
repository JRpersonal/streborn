// What the settings screen's network section says, decided without a DOM.
//
// The speaker's /networkInfo is not one shape. A wpa speaker (ST10, sm2 ST20)
// lists a WIFI_INTERFACE with a name and a signal, and a real ETHERNET one
// when a cable is in. A coprocessor speaker (scm ST20, ST30, Portable) lists
// only an ETHERNET_INTERFACE: its Wi-Fi chip is presented as eth0, so
// "Ethernet connected" there means Wi-Fi OR a cable, and the scm ST20 does not
// even name the network.
//
// The rule: a network name is only shown as the one the speaker is on when the
// speaker (or the agent's live wpa read) said so. The agent used to fill the
// name in from STR's own records on the coprocessor speakers, and after an
// owner moved one through Bose's setup page the screen named the network they
// had switched off (support mail, 2026-10-21). STR's network now arrives as
// strSSID and is shown as what it is: the last network set through ST Reborn.

const isUp = (state) => {
  const s = String(state || '').toUpperCase();
  if (s.includes('DISCONNECT') || s.includes('NOT_CONNECTED')) return false;
  return s.endsWith('_CONNECTED');
};

// networkView returns:
//   kind: 'none'     not connected at all
//         'wired'    a real cable on a speaker that also has a Wi-Fi radio
//         'wifi'     on Wi-Fi, network named
//         'wifi-unnamed'  on Wi-Fi (a signal class proves it), name not reported
//         'unknown-link'  connected, but the speaker reports neither a name nor
//                    a signal, so it may as well be on a cable
//   ip, ssid, signal, frequencyKHz, strSSID (STR's last network, context only),
//   coprocessor (the eth0-presented Wi-Fi chip).
export function networkView(net) {
  const ifaces = (net && net.interfaces) || [];
  const conn = ifaces.find((i) => i.type === 'WIFI_INTERFACE' && isUp(i.state))
    || ifaces.find((i) => i.state === 'NETWORK_ETHERNET_CONNECTED' && i.ipAddress);
  if (!conn) return { kind: 'none' };
  const hasRealWifiIface = ifaces.some((i) => i.type === 'WIFI_INTERFACE');
  const base = {
    ip: conn.ipAddress || '',
    ssid: conn.ssid || '',
    signal: conn.signal || '',
    frequencyKHz: conn.frequencyKHz || 0,
    strSSID: conn.strSSID || '',
    coprocessor: conn.type !== 'WIFI_INTERFACE' && !hasRealWifiIface,
  };
  if (conn.type !== 'WIFI_INTERFACE' && hasRealWifiIface) {
    return { ...base, kind: 'wired', ssid: '', signal: '', strSSID: '' };
  }
  if (base.ssid) return { ...base, kind: 'wifi' };
  if (base.coprocessor) return { ...base, kind: base.signal ? 'wifi-unnamed' : 'unknown-link' };
  return { ...base, kind: 'wifi-unnamed' };
}

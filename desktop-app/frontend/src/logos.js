// logos.js — station logo / icon resolution.
//
// Many radio-browser entries ship without a favicon. When that is
// the case we derive the domain from the homepage or stream URL and
// fall back to the DuckDuckGo icon service. On 404 the img element
// cascades through the remaining candidates via onerror.

import { escapeAttr } from './utils.js';

export function extractHost(u) {
  if (!u) return '';
  try { return new URL(u).hostname; } catch { return ''; }
}

// rootDomain strips the first subdomain. "stream.rockland-digital.de"
// → "rockland-digital.de", "icecast.wdr.de" → "wdr.de". The favicon
// services often hit even when the streaming host itself has no
// branded logo.
export function rootDomain(host) {
  if (!host) return '';
  const parts = host.split('.');
  if (parts.length < 3) return host;
  return parts.slice(1).join('.');
}

// iconServicesFor yields favicon-service URLs for a host. We only
// query DuckDuckGo's privacy-friendly icon service. The Google
// equivalent (www.google.com/s2/favicons) used to live in this list
// too but was removed on user request — it is a data-mining service
// and the few extra logos it might resolve are not worth handing
// every browse session to Google.
export function iconServicesFor(host) {
  if (!host) return [];
  return [
    `https://icons.duckduckgo.com/ip3/${host}.ico`,
  ];
}

export function stationLogoCandidates(s) {
  const out = [];
  if (s.favicon) out.push(s.favicon);
  const hosts = [];
  for (const u of [s.homepage, s.url, s.url_resolved]) {
    const h = extractHost(u);
    if (h && !hosts.includes(h)) hosts.push(h);
    const r = rootDomain(h);
    if (r && !hosts.includes(r)) hosts.push(r);
  }
  for (const h of hosts) {
    for (const svc of iconServicesFor(h)) {
      if (!out.includes(svc)) out.push(svc);
    }
  }
  return out;
}

// logoImgTag renders an <img> wired up so onerror cycles through the
// remaining fallback candidates encoded in data-fallbacks. Once the
// list is empty, the helper at window.__nextLogoFallback hides the
// element so CSS can render its placeholder.
export function logoImgTag(s, cssClass) {
  const candidates = stationLogoCandidates(s);
  if (candidates.length === 0) return '<div class="fav-empty"></div>';
  const first = candidates[0];
  const rest = candidates.slice(1).join('|');
  return `<img class="${cssClass}"
            src="${escapeAttr(first)}"
            data-fallbacks="${escapeAttr(rest)}"
            onerror="window.__nextLogoFallback(this)"/>`;
}

// Global helper called by the inline onerror handler. Cycles through
// data-fallbacks until one loads or the element is hidden. Lives on
// window because the onerror handler runs in the inline-script global
// scope and cannot import.
window.__nextLogoFallback = function(img) {
  const list = (img.dataset.fallbacks || '').split('|').filter(Boolean);
  if (list.length === 0) {
    img.onerror = null;
    img.style.display = 'none';
    return;
  }
  const next = list.shift();
  img.dataset.fallbacks = list.join('|');
  img.src = next;
};

// bestLogoForStation returns the best available logo URL for a
// station. Used when saving to a preset slot so the preset button
// and the box both have a logo to show.
export function bestLogoForStation(s) {
  return stationLogoCandidates(s)[0] || '';
}

// stationLogoChain returns all logo candidates as a pipe-separated
// string. Persisted into preset.art so the frontend can keep
// cascading through fallbacks even after an app restart. The stick
// agent splits the pipe-separated art at PlayURL time and uses only
// the first entry as the UPnP albumArtURI.
export function stationLogoChain(s) {
  return stationLogoCandidates(s).join('|');
}

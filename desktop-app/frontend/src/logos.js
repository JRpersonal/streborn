// logos.js — station logo / icon resolution.
//
// Many radio-browser entries ship without a usable favicon. The cascade
// (see stationLogoCandidates + logoImgTag) tries, in order: the
// station's own HTTPS favicon, the DuckDuckGo icon service derived from
// the station's hostnames, an HTTP-only favicon as a late attempt, and
// finally a locally generated monogram (a data: URI, no network). On
// each load failure the img element walks to the next candidate via
// onerror. DuckDuckGo is the only external favicon service and is hit
// only when the station has no usable logo of its own; Google's was
// deliberately excluded (data mining).

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

// monogramDataUri builds a self-contained SVG tile from a station's
// name: its first character on a colour derived from the name. It is a
// data: URI, so it renders instantly with NO network request and leaks
// nothing to anyone. This is the terminal fallback, replacing the old
// idea of a third-party "always returns an image" service (icon.horse),
// which in practice returned HTTP 504 for obscure stations after a
// ~15 s hang, the very stations that have no logo to begin with.
function monogramColor(seed) {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) >>> 0;
  return `hsl(${h % 360} 40% 38%)`;
}
export function monogramDataUri(name) {
  const raw = (name || '?').trim();
  const ch = (raw.charAt(0) || '?').toUpperCase()
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  const bg = monogramColor(raw || '?');
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="160">` +
    `<rect width="160" height="160" rx="14" fill="${bg}"/>` +
    `<text x="80" y="108" font-family="Segoe UI,Arial,sans-serif" font-size="84" ` +
    `font-weight="700" fill="#ffffff" text-anchor="middle">${ch}</text></svg>`;
  return 'data:image/svg+xml;utf8,' + encodeURIComponent(svg);
}

// stationLogoCandidates returns an ordered list of icon URLs to try
// for a station. The browser's onerror cascade walks the list and
// keeps the first one that loads.
//
// Ordering rationale:
//   1. The station's own radio-browser `favicon`, but ONLY when it is
//      HTTPS. That is the authentic, often highest-quality logo, so it
//      goes first when it can actually load. The reason it was not
//      first historically is that many of these URLs are unreliable:
//      HTTP-only ones are blocked as mixed content in the secure
//      webview, and some return 402/403 (e.g. REYFM behind a Cloudflare
//      paywall). Gating on HTTPS removes the most common failure (mixed
//      content) so the true logo wins whenever it is servable.
//   2. DuckDuckGo's privacy-friendly icon service, derived from the
//      station's hostnames. Reliable clean 200/404 and a normalised
//      square favicon, so it is the dependable middle of the cascade.
//   3. An HTTP-only radio-browser favicon as a late attempt (blocked
//      today, harmless to keep for non-secure contexts).
//   (The terminal fallback, a locally generated monogram, is appended
//    in logoImgTag, not here, see monogramDataUri.)
//
// Privacy is the tie-breaker for this order. The onerror cascade fires
// sequentially: only the candidates up to the first success are ever
// requested. Putting the station's own favicon first means a station
// with a working logo costs ZERO third-party requests: DuckDuckGo never
// learns the user is looking at it. DuckDuckGo (the only external
// favicon service) is reached solely when the station provides no usable
// logo of its own, and each request leaks nothing but that station's
// public domain. When even that misses, the fallback is a local
// monogram, no network at all. Google's favicon service is deliberately
// excluded (data mining), and icon.horse was dropped after it returned
// HTTP 504 (a ~15 s hang) for exactly the obscure stations it was meant
// to cover.
export function stationLogoCandidates(s) {
  const out = [];
  const push = (u) => { if (u && !out.includes(u)) out.push(u); };

  const hosts = [];
  for (const u of [s.homepage, s.url, s.url_resolved]) {
    const h = extractHost(u);
    if (h && !hosts.includes(h)) hosts.push(h);
    const r = rootDomain(h);
    if (r && !hosts.includes(r)) hosts.push(r);
  }

  // 1. The station's own favicon, first, but only if HTTPS.
  const faviconHttps = s.favicon && /^https:/i.test(s.favicon);
  if (faviconHttps) push(s.favicon);

  // 2. DuckDuckGo icon service per host.
  for (const h of hosts) {
    for (const svc of iconServicesFor(h)) push(svc);
  }

  // 3. An HTTP-only radio-browser favicon, deferred to the end.
  if (s.favicon && !faviconHttps) push(s.favicon);

  // Note: the local monogram terminal fallback is appended only in the
  // live <img> cascade (logoImgTag), not here, so persisted preset art
  // and UPnP albumArtURI stay real http(s) URLs the speaker can fetch.
  return out;
}

// logoImgTag renders an <img> wired up so onerror cycles through the
// remaining fallback candidates encoded in data-fallbacks. The local
// monogram data: URI is appended as the guaranteed last candidate, so
// every station ends up with a tile (real logo where one exists, a
// generated letter tile otherwise) and the hide path is effectively
// never reached.
export function logoImgTag(s, cssClass) {
  const candidates = [...stationLogoCandidates(s), monogramDataUri(s.name)];
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

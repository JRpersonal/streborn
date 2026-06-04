// logos.js — station logo / icon resolution.
//
// Many radio-browser entries ship without a usable favicon. The cascade
// (see stationLogoCandidates) tries, in order: the station's own HTTPS
// favicon, the DuckDuckGo icon service derived from the station's
// hostnames, an HTTP-only favicon as a late attempt, and finally
// icon.horse as a terminal fallback that always returns an image. On
// each load failure the img element walks to the next candidate via
// onerror. All three external services are privacy-respecting; Google's
// favicon service was deliberately removed (data mining).

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

// iconHorseFor yields the icon.horse URL for a host. icon.horse is an
// independent, privacy-respecting favicon service (no ad/data company
// behind it). It is used as a terminal fallback because it always
// returns an image (a generated monogram when it has no real icon),
// so it never 404s and therefore must sit last in the cascade, after
// every source that can return the station's true logo.
export function iconHorseFor(host) {
  if (!host) return '';
  return `https://icon.horse/icon/${host}`;
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
//   4. icon.horse as the terminal fallback. It always returns an image
//      (a generated monogram if it has nothing better), so it can only
//      ever be last: it guarantees a tile instead of a blank
//      placeholder, without masking any source above it.
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

  // 4. icon.horse terminal fallback (always returns an image).
  for (const h of hosts) push(iconHorseFor(h));

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

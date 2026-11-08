// #1773: a station without a logo of its own got the icon of the domain its
// stream host is under. "Radio Eure du Blues" streams from a *.ddns.net host and
// wore ddns.net's favicon through DuckDuckGo. A shared or dynamic-DNS host, its
// root domain, and an IP literal are never looked up; with nothing left the key
// keeps its letter tile. The list mirrors the Go package stationhost
// (stationhost_test.go checks the copy).
import { describe, it, expect } from 'vitest';
import { logoHostsFor, sharedLogoHost, stationLogoCandidates } from './logos.js';

describe('sharedLogoHost', () => {
  it('takes dynamic-DNS, tunnel and IP hosts for shared', () => {
    for (const h of ['radioeure.ddns.net', 'ddns.net', 'RADIO.NO-IP.ORG', 'blues.duckdns.org', 'members.dyndns.org',
      'x.dyndns.info', 'x.dnsalias.net', 'x.homeip.net', 'a.mooo.com', 'abc.ngrok.io', 'abc.trycloudflare.com',
      'radio.ddns.net.', '192.0.2.7', '[2001:db8::1]']) {
      expect(sharedLogoHost(h), h).toBe(true);
    }
  });
  it('leaves a station\'s own host alone', () => {
    for (const h of ['', 'www.radio-eure.example', 'stream.example.de', 'notddns.net', 'ddns.network', 'myddns.net.example']) {
      expect(sharedLogoHost(h), h).toBe(false);
    }
  });
});

describe('logo lookups skip shared hosts', () => {
  const eure = {
    name: 'Radio Eure du Blues',
    homepage: 'https://www.radio-eure.example/',
    url: 'http://radioeure.ddns.net:8000/stream',
    url_resolved: 'http://radioeure.ddns.net:8000/stream',
  };

  it('asks about the homepage only, never the dynamic-DNS stream host or its root', () => {
    const hosts = logoHostsFor(eure);
    expect(hosts).toContain('www.radio-eure.example');
    expect(hosts.some((h) => h.includes('ddns.net'))).toBe(false);
    const cands = stationLogoCandidates(eure);
    expect(cands.some((c) => c.includes('ddns.net'))).toBe(false);
    expect(cands).toContain('https://icons.duckduckgo.com/ip3/www.radio-eure.example.ico');
  });

  it('a station on a shared host alone has nothing to look up: the letter tile stays', () => {
    const s = { name: 'Blues', homepage: '', url: 'http://blues.duckdns.org/live', url_resolved: 'http://203.0.113.9:8000/live' };
    expect(logoHostsFor(s)).toEqual([]);
    expect(stationLogoCandidates(s)).toEqual([]);
  });

  it('an own favicon is still used', () => {
    const s = { name: 'Blues', favicon: 'https://cdn.example/blues.png', url: 'http://blues.duckdns.org/live' };
    expect(stationLogoCandidates(s)).toEqual(['https://cdn.example/blues.png']);
  });
});

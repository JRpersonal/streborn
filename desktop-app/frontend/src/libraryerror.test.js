import { describe, it, expect } from 'vitest';
import { looksUnreachable } from './views/library.js';

// A browse that fails because the server did not answer gets a sentence naming
// the server and the likely reason. Anything else keeps the underlying error,
// because inventing a friendly sentence for an unknown fault hides it.
describe('looksUnreachable', () => {
  it('recognises the shapes a sleeping or switched-off server produces', () => {
    // The first one is what a reporter actually saw on screen (#1047, #1065):
    // he let the MinimServer host go to sleep on purpose to see what STR said.
    for (const msg of [
      'BrowseLibrary: context deadline exceeded',
      'Get "http://10.0.0.5:9790/dev": dial tcp: i/o timeout',
      'dial tcp 10.0.0.5:9790: connectex: No connection could be made because the target machine actively refused it',
      'connection refused',
      'read tcp: connection reset by peer',
      'no route to host',
      'dial tcp: lookup minim.local: no such host',
      'network is unreachable',
      'unexpected EOF',
      'The operation timed out',
    ]) {
      expect(looksUnreachable(msg), msg).toBe(true);
    }
  });

  it('leaves a real server answer alone, so its error is not papered over', () => {
    for (const msg of [
      'BrowseLibrary: UPnPError 701 no such object',
      'browse failed: 500 Internal Server Error',
      'the server returned malformed DIDL-Lite',
      'unauthorized',
    ]) {
      expect(looksUnreachable(msg), msg).toBe(false);
    }
  });

  it('survives whatever it is handed', () => {
    expect(looksUnreachable(null)).toBe(false);
    expect(looksUnreachable(undefined)).toBe(false);
    expect(looksUnreachable({})).toBe(false);
    expect(looksUnreachable(new Error('context deadline exceeded'))).toBe(true);
  });
});

import { describe, it, expect } from 'vitest';
import { orionStationPayload, slotFromOrionStation } from './utils.js';

// The exact location a SoundTouch 20 reported while playing a native radio
// preset, from the bundle attached to #608. Everything about this test is
// taken from that file rather than invented, because the shape of this payload
// is the whole bug.
const REAL = '/station?data=eyJpbWFnZVVybCI6Imh0dHA6Ly8xMjcuMC4wLjE6ODg4OC9hcnQ_dT1hSFIwY0hNNkx5OWpaRzR0Y0hKdlptbHNaWE11ZEhWdVpXbHVMbU52YlM5ek56UTVPREl2YVcxaFoyVnpMMnh2WjI5a0xuQnVaejkwUFRZek9EYzFOalUyTmpZMU1EQXdNREF3TUEiLCJpc1JlYWx0aW1lIjp0cnVlLCJuYW1lIjoiUmFkaW8gMTAgIDgwJ3MgSGl0cyIsInN0cmVhbVR5cGUiOiJsaXZlUmFkaW8iLCJzdHJlYW1VcmwiOiJodHRwOi8vMTI3LjAuMC4xOjg4ODgvc3RyZWFtL3Jhdz91PWFIUjBjRG92TDNCc1lYbGxjbk5sY25acFkyVnpMbk4wY21WaGJYUm9aWGR2Y214a0xtTnZiUzloY0drdmJHbDJaWE4wY21WaGJTMXlaV1JwY21WamRDOVVURkJUVkZJeU1DNXRjRE0ifQ';

describe('orionStationPayload', () => {
  it('decodes what the speaker reports while playing a native preset', () => {
    const p = orionStationPayload(REAL);
    expect(p).not.toBeNull();
    expect(p.name).toBe("Radio 10  80's Hits");
    expect(p.streamUrl).toContain('/stream/raw?u=');
    expect(p.imageUrl).toContain('/art?u=');
  });

  // This is why the save failed: the payload's stream URL is the RAW proxy
  // form, not a per-slot one, so the slot lookup finds nothing and the save
  // fell through to a path that posted the envelope itself as the stream.
  it('has no per-slot proxy URL, which is why the slot lookup comes up empty', () => {
    expect(slotFromOrionStation(REAL)).toBeNull();
  });

  it('returns null for locations that are not a station descriptor', () => {
    expect(orionStationPayload('/stream/3')).toBeNull();
    expect(orionStationPayload('http://example.com/live.mp3')).toBeNull();
    expect(orionStationPayload('')).toBeNull();
    expect(orionStationPayload(null)).toBeNull();
  });

  it('survives a malformed payload instead of throwing', () => {
    expect(orionStationPayload('/station?data=not-base64!!')).toBeNull();
    expect(orionStationPayload('/station?data=YWJj')).toBeNull(); // "abc", not JSON
  });

  // A per-slot payload must still resolve to its slot, so the copy-slot path
  // that already worked is not disturbed by the refactor.
  it('still finds the slot when the payload carries a per-slot proxy URL', () => {
    const payload = { name: 'X', streamUrl: 'http://127.0.0.1:8888/stream/4' };
    const b64 = Buffer.from(JSON.stringify(payload)).toString('base64')
      .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    expect(slotFromOrionStation('/station?data=' + b64)).toBe(4);
  });
});

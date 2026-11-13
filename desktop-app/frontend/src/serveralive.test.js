import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { clearForAliveServer, isServerOffRefusal, presetServer, serverMatches } from './serveralive.js';

const folderKey = {
  slot: 5, type: 'queue', name: 'Album',
  folder: { server_udn: 'uuid:4D696E69-444C-164E-9D41-B827EB000001' },
  items: [{ url: 'http://192.0.2.20:8200/MediaItems/1.flac' }],
};
const trackKey = { slot: 2, type: 'dlna', name: 'Song', stream_url: 'http://192.0.2.21:50002/m/7.mp3' };
const radioKey = { slot: 1, type: 'radio', name: 'Radio', stream_url: 'http://stream.example.org/live' };

describe('serveralive (#1890)', () => {
  it('reads the server of a library key', () => {
    expect(presetServer(folderKey)).toEqual({ udn: '4d696e69-444c-164e-9d41-b827eb000001', host: '192.0.2.20' });
    expect(presetServer(trackKey)).toEqual({ udn: '', host: '192.0.2.21' });
  });

  it('matches by UDN first, else by host', () => {
    expect(serverMatches(folderKey, { uuid: 'uuid:4D696E69-444C-164E-9D41-B827EB000001', location: 'http://192.0.2.99:1/d.xml' })).toBe(true);
    expect(serverMatches(folderKey, { uuid: 'uuid:other', location: 'http://192.0.2.20:8200/d.xml' })).toBe(false);
    expect(serverMatches(trackKey, { uuid: 'uuid:x', location: 'http://192.0.2.21:50001/desc.xml' })).toBe(true);
    expect(serverMatches(trackKey, { uuid: 'uuid:x', location: 'http://192.0.2.22:50001/desc.xml' })).toBe(false);
  });

  it('clears only the server-off marks of the server that came back', () => {
    const errors = { 5: 'Music server not answering', 2: 'Music server not answering', 1: 'Spotify needs Premium' };
    const serverOff = { 5: true, 2: true };
    const cleared = clearForAliveServer(errors, serverOff, [folderKey, trackKey, radioKey],
      { uuid: 'uuid:4D696E69-444C-164E-9D41-B827EB000001', location: 'http://192.0.2.20:8200/rootDesc.xml' });
    expect(cleared).toEqual([5]);
    expect(errors).toEqual({ 2: 'Music server not answering', 1: 'Spotify needs Premium' });
    expect(serverOff).toEqual({ 2: true });
  });

  it('never clears a mark that is not a server-off refusal', () => {
    const errors = { 2: 'The music server answers, but no longer has the music' };
    const cleared = clearForAliveServer(errors, {}, [trackKey], { uuid: '', location: 'http://192.0.2.21:50001/d.xml' });
    expect(cleared).toEqual([]);
    expect(errors[2]).toBeTruthy();
  });

  it('knows the refusal by its code', () => {
    expect(isServerOffRefusal('Error: {"code":"library-server-offline","server":"NAS"}')).toBe(true);
    expect(isServerOffRefusal('Error: {"code":"library-file-gone"}')).toBe(false);
  });

  it('is wired: the press records the kind, the event clears it', () => {
    const play = readFileSync(new URL('./views/music/play.js', import.meta.url), 'utf8');
    expect(play).toContain('state.presetServerOff[slot] = isServerOffRefusal(errStr)');
    const wiring = readFileSync(new URL('./views/music/wiring.js', import.meta.url), 'utf8');
    expect(wiring).toContain('EventsOn(MEDIA_SERVER_ALIVE_EVENT');
  });
});

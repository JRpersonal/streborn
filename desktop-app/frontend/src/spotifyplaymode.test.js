// #2058: the player's shuffle and repeat buttons while a Spotify playlist or
// album plays. The folder queue's own buttons, asking the speaker's engine.
import { describe, it, expect } from 'vitest';
import {
  buttonRepeatMode,
  mergePlayModeAnswer,
  playModesFromReply,
  playModeUnsupported,
  spotifyPlayModesShown,
} from './spotifyplaymode.js';
import { srcOf } from './test/source.js';

const SPOTIFY = 'http://192.0.2.1:8888/spotify/stream-3.ogg';
const MODES = { shuffle: false, repeat: 'off' };

describe('spotifyPlayModesShown', () => {
  it('shows the buttons for STR\'s own Spotify stream once the agent reported the state', () => {
    expect(spotifyPlayModesShown({ location: SPOTIFY, playModes: MODES })).toBe(true);
    expect(spotifyPlayModesShown({ location: 'http://192.0.2.1:8888/spotify/stream.ogg', playModes: MODES })).toBe(true);
  });

  it('hides them for an agent that never reported a state (older than the buttons)', () => {
    expect(spotifyPlayModesShown({ location: SPOTIFY, playModes: null })).toBe(false);
    expect(spotifyPlayModesShown({ location: SPOTIFY })).toBe(false);
  });

  it('hides them once the agent said it cannot do the press', () => {
    expect(spotifyPlayModesShown({ location: SPOTIFY, playModes: MODES, unsupported: true })).toBe(false);
  });

  it('hides them off Spotify, for the speaker\'s own Spotify, and while a folder plays', () => {
    expect(spotifyPlayModesShown({ location: 'http://192.0.2.1:8888/stream/1', playModes: MODES })).toBe(false);
    expect(spotifyPlayModesShown({ location: '', playModes: MODES })).toBe(false);
    expect(spotifyPlayModesShown({ location: SPOTIFY, playModes: MODES, boxSpotify: true })).toBe(false);
    expect(spotifyPlayModesShown({ location: SPOTIFY, playModes: MODES, queueActive: true })).toBe(false);
    expect(spotifyPlayModesShown()).toBe(false);
  });
});

describe('buttonRepeatMode', () => {
  it('draws the engine\'s three repeat modes as the folder queue\'s', () => {
    expect(buttonRepeatMode('off')).toBe('off');
    expect(buttonRepeatMode('context')).toBe('all');
    expect(buttonRepeatMode('track')).toBe('one');
    expect(buttonRepeatMode(undefined)).toBe('off');
    expect(buttonRepeatMode('something new')).toBe('off');
  });
});

describe('playModesFromReply', () => {
  it('reads the playModes of a /spotify/info reply', () => {
    expect(playModesFromReply({ track: 'A', playModes: { shuffle: true, repeat: 'track' } })).toEqual({ shuffle: true, repeat: 'track' });
    expect(playModesFromReply({ playModes: { shuffle: 1 } })).toEqual({ shuffle: true, repeat: 'off' });
  });

  it('is null for a reply without them', () => {
    expect(playModesFromReply({ track: 'A' })).toBe(null);
    expect(playModesFromReply({ playModes: null })).toBe(null);
    expect(playModesFromReply(null)).toBe(null);
  });
});

describe('mergePlayModeAnswer', () => {
  it('takes the state the press answered', () => {
    expect(mergePlayModeAnswer(MODES, { ok: true, shuffle: true, repeat: 'context' })).toEqual({ shuffle: true, repeat: 'context' });
  });

  it('keeps what was known for a field the agent could not read back', () => {
    expect(mergePlayModeAnswer({ shuffle: false, repeat: 'track' }, { ok: true, shuffle: true })).toEqual({ shuffle: true, repeat: 'track' });
    expect(mergePlayModeAnswer({ shuffle: true, repeat: 'off' }, { ok: true, repeat: 'context' })).toEqual({ shuffle: true, repeat: 'context' });
    expect(mergePlayModeAnswer(null, null)).toEqual({ shuffle: false, repeat: 'off' });
  });
});

describe('playModeUnsupported', () => {
  it('tells "this speaker cannot" from a refusal worth showing', () => {
    expect(playModeUnsupported(new Error('spotify_playmode_unsupported'))).toBe(true);
    expect(playModeUnsupported('spotify_playmode_unsupported')).toBe(true);
    expect(playModeUnsupported(new Error('STR_MISSING_BINDING: SpotifyPlayMode is not available in this build'))).toBe(true);
    expect(playModeUnsupported(new Error('Spotify on STR is not what the speaker plays'))).toBe(false);
    expect(playModeUnsupported(null)).toBe(false);
  });
});

describe('wiring', () => {
  it('the folder buttons ask the Spotify engine while a Spotify playlist plays', () => {
    const wiring = srcOf('mountWiring');
    expect(wiring).toContain("spotifyPlayModeAction('shuffle')");
    expect(wiring).toContain("spotifyPlayModeAction('repeat')");
    expect(srcOf('spotifyPlayModeAction')).toContain('SpotifyPlayMode(box.host, box.port, kind)');
  });

  it('the same buttons draw the Spotify state, without the folder\'s skip and position', () => {
    const render = srcOf('renderQueueControls');
    expect(render).toContain('spotifyPlayModesOn()');
    expect(render).toContain('buttonRepeatMode(pm.repeat)');
    expect(render).toContain("['queuePrevBtn', 'queueNextBtn', 'queuePos']");
  });

  it('the state rides the now-playing refresh, and a speaker switch drops it', () => {
    expect(srcOf('refreshStatus')).toContain('playModesFromReply(np)');
    expect(srcOf('resetNowPlaying')).toContain('state.spotifyPlayModes = null');
  });
});

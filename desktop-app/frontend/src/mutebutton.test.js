import { describe, it, expect } from 'vitest';
import { muteView, muteAfterPress } from './mutebutton.js';

describe('muteView', () => {
  it('shows the crossed-out speaker, and offers to undo, while muted', () => {
    expect(muteView(true)).toEqual({
      glyph: '&#128263;',
      pressed: true,
      labelKey: 'controls.unmute',
    });
  });

  it('shows the plain speaker, and offers to mute, while audible', () => {
    expect(muteView(false)).toEqual({
      glyph: '&#128266;',
      pressed: false,
      labelKey: 'controls.mute',
    });
  });

  it('treats a speaker that reports nothing as audible', () => {
    // BoxSettings from an older agent has no muted field at all, and the
    // button must then look the way it has always looked rather than claim
    // the speaker is silent.
    expect(muteView(undefined).pressed).toBe(false);
    expect(muteView(null).pressed).toBe(false);
  });
});

describe('muteAfterPress', () => {
  it('takes the state the speaker reports', () => {
    expect(muteAfterPress({ muted: true, changed: true }, true)).toBe(true);
    expect(muteAfterPress({ muted: false, changed: true }, false)).toBe(false);
  });

  it('believes the speaker over the user when they disagree', () => {
    // changed:false is the agent saying the speaker was already there. The
    // interesting case is the speaker having moved the other way in between,
    // e.g. somebody pressed MUTE on the remote: its answer still wins.
    expect(muteAfterPress({ muted: false, changed: false }, true)).toBe(false);
  });

  it('goes back when the press could not be delivered', () => {
    // No answer means nothing happened on the speaker, so the icon must show
    // the state it came from. Leaving it on want would tell the user they had
    // silenced a speaker that is still playing.
    expect(muteAfterPress(null, true)).toBe(false);
    expect(muteAfterPress(null, false)).toBe(true);
  });

  it('does not take a malformed answer for a state', () => {
    expect(muteAfterPress({}, true)).toBe(false);
    expect(muteAfterPress({ muted: 'yes' }, true)).toBe(false);
  });
});

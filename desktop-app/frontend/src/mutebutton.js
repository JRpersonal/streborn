// What the mute button shows, kept away from the DOM so it can be tested.
//
// Two small decisions, both of which lie convincingly when they are wrong: an
// icon that shows the wrong state tells the user the speaker is silent when it
// is not, and a press that could not be delivered must not leave the icon
// claiming it was.

// muteView maps a mute state to what the button renders. The muted state is
// carried by the glyph itself, the crossed-out speaker, rather than by a colour
// alone, so it is still readable to somebody who cannot see the colour.
export function muteView(muted) {
  const on = !!muted;
  return {
    glyph: on ? '&#128263;' : '&#128266;',
    pressed: on,
    labelKey: on ? 'controls.unmute' : 'controls.mute',
  };
}

// muteAfterPress says which state to show once a press has been answered.
//
// want is what the user asked for, and the button already shows it: the press
// is drawn immediately, because a button that looks dead for half a second gets
// pressed twice. So this is only about correcting that guess.
//
// The speaker's own answer wins, because the speaker is the one source of truth
// for its mute flag (its remote and its buttons set the same one). A missing or
// shapeless answer means the press never landed, and then the honest thing is
// the state we came FROM, which is the opposite of what was asked for. Showing
// want anyway would be the app telling the user it had silenced a speaker that
// is still playing.
export function muteAfterPress(res, want) {
  if (res && typeof res.muted === 'boolean') return res.muted;
  return !want;
}

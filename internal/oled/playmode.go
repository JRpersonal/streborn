package oled

// Shuffle and repeat on the panel: a notice when a remote key switches the
// Spotify playlist's shuffle or steps its repeat (#1679, internal/webui
// playmodekey.go). Like the sleep and bass steps, a newer one takes over the
// showing one in place, so tapping the repeat key through its modes reads as
// one notice changing.

import "time"

// NoticePlayMode is the shuffle and repeat keys' notice.
const NoticePlayMode NoticeKind = "playmode"

// playModeNoticeHold is how long a shuffle or repeat notice stays.
const playModeNoticeHold = 2 * time.Second

// Shuffle: the playlist's shuffle is now on or off.
func Shuffle(on bool) Notice {
	if on {
		return keyNotice(NoticePlayMode, iconShuffle, 0, "SHUFFLE ON", "Random order", playModeNoticeHold, 2)
	}
	return keyNotice(NoticePlayMode, iconShuffle, 0, "SHUFFLE OFF", "Playlist order", playModeNoticeHold, 2)
}

// Repeat: the playlist's repeat is now mode, the engine's names: "context"
// (the whole playlist), "track" (the one song) or "off".
func Repeat(mode string) Notice {
	switch mode {
	case "context":
		return keyNotice(NoticePlayMode, iconRepeat, 0, "REPEAT ALL", "Whole playlist", playModeNoticeHold, 2)
	case "track":
		return keyNotice(NoticePlayMode, iconRepeat, 0, "REPEAT ONE", "This song", playModeNoticeHold, 2)
	}
	return keyNotice(NoticePlayMode, iconRepeat, 0, "REPEAT OFF", "Plays to the end", playModeNoticeHold, 2)
}

// Liked: the playing Spotify song was saved to the account's Liked Songs
// (the remote's thumbs up key, #1165). name is the song, cut to one line.
func Liked(name string) Notice {
	if name == "" {
		name = "Liked Songs"
	}
	return keyNotice(NoticePlayMode, iconHeart, 0, "LIKED", name, playModeNoticeHold, 2)
}

// Unliked: the playing Spotify song was removed from the account's Liked
// Songs (the remote's thumbs down key, #1165). name is the song, cut to one
// line.
func Unliked(name string) Notice {
	if name == "" {
		name = "Liked Songs"
	}
	return keyNotice(NoticePlayMode, iconHeartOff, 0, "REMOVED", name, playModeNoticeHold, 2)
}

// heartIcon is a filled heart.
func (c canvas) heartIcon() {
	c.heart(iconX+16, iconY+12, 6, 15)
}

// heartOffIcon is the outline of the same heart: a filled heart with a
// smaller one cut out of it.
func (c canvas) heartOffIcon() {
	c.heart(iconX+16, iconY+12, 6, 15)
	c.heart(iconX+16, iconY+13, 4, 0)
}

// heart draws a filled heart around cx with its lobes centred on cy: two
// discs of radius rad over a downward wedge.
func (c canvas) heart(cx, cy, rad, lvl int) {
	c.disc(cx-rad, cy, rad, lvl)
	c.disc(cx+rad, cy, rad, lvl)
	// The wedge runs from the discs' widest line down to the tip.
	half, depth := 2*rad-1, 2*rad+1
	for i := 0; i <= depth; i++ {
		w := half - half*i/depth
		c.line(cx-w, cy+2+i, cx+w, cy+2+i, lvl)
	}
}

// shuffleIcon is two crossing arrows.
func (c canvas) shuffleIcon() {
	const x0, x1, top, bot = iconX + 3, iconX + 27, iconY + 9, iconY + 23
	for d := 0; d < 2; d++ {
		c.line(x0, top+d, x1-4, bot+d, 15)
		c.line(x0, bot+d, x1-4, top+d, 15)
	}
	// arrow heads at the right ends
	for _, y := range []int{top, bot} {
		for i := 0; i < 5; i++ {
			c.line(x1-4+i, y-4+i, x1-4+i, y+5-i, 15)
		}
	}
}

// repeatIcon is a loop with an arrow head.
func (c canvas) repeatIcon() {
	const cx, cy = iconX + 16, iconY + 16
	for _, rad := range []int{10, 11} {
		c.arc(cx, cy, rad, 40, 360, 15)
	}
	// arrow head where the loop opens, pointing down its right side
	const ax, ay = cx + 10, cy - 3
	for i := 0; i < 5; i++ {
		c.line(ax-4+i, ay+i, ax+5-i, ay+i, 15)
	}
}

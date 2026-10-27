package boxws

import (
	"bytes"
	"time"
)

// NowPlayingFrame is one <nowPlaying> document the bus carried, as the
// speaker sent it, with when it arrived. The agent keeps the last few so a
// tap on a preset key can still say what played just BEFORE the tap: by the
// time the tap's selection frame is handled the firmware has already
// switched, and a fresh /now_playing read would name the key's own station.
type NowPlayingFrame struct {
	At time.Time
	// Raw is the <nowPlaying> element alone, in the shape :8090/now_playing
	// answers with, so the same parsers read both.
	Raw []byte
}

// nowPlayingElement cuts the <nowPlaying ...>...</nowPlaying> element out of
// a gabbo frame. nil when the frame has none.
func nowPlayingElement(data []byte) []byte {
	const open = "<nowPlaying"
	start := -1
	for off := 0; off < len(data); {
		i := bytes.Index(data[off:], []byte(open))
		if i < 0 {
			return nil
		}
		i += off
		next := i + len(open)
		// <nowPlayingUpdated> shares the prefix; only the element itself counts.
		if next < len(data) && bytes.IndexByte([]byte(" \t\r\n>/"), data[next]) >= 0 {
			start = i
			break
		}
		off = next
	}
	if start < 0 {
		return nil
	}
	end := bytes.Index(data[start:], []byte("</nowPlaying>"))
	if end < 0 {
		return nil
	}
	end += start + len("</nowPlaying>")
	return bytes.Clone(data[start:end])
}

// noteNowPlayingFrame hands a frame's <nowPlaying> to the handler when it
// wants it (optional hook). Must not block.
func (c *Client) noteNowPlayingFrame(data []byte) {
	if c.handler == nil {
		return
	}
	h, ok := c.handler.(interface{ OnNowPlayingFrame(NowPlayingFrame) })
	if !ok {
		return
	}
	raw := nowPlayingElement(data)
	if raw == nil {
		return
	}
	h.OnNowPlayingFrame(NowPlayingFrame{At: time.Now(), Raw: raw})
}

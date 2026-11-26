package webui

import "bytes"

// How long an announcement plays, for the POST /api/announce answer.
//
// The answer goes out the moment the audio is fetched, long before the clip
// has played, so an automation that wants to wait for the end (or space two
// announcements) had nothing to go on. The audio is already in memory at that
// point, and an MP3's length is in its frame headers: each Layer III frame
// decodes to a fixed number of samples at the rate its header names. So the
// clip's frames are walked, header to header, and their durations summed. A
// TTS text is several MP3 chunks back to back; the walk passes from one to the
// next (and over an ID3 tag between them) and gives the sum.
//
// The speaker's CPU is weak, so the walk only reads headers, never audio, and
// is capped: past mp3WalkMaxFrames the rest of the clip is extrapolated at the
// rate walked so far, and bytes that are not a frame are searched through for
// the next one only within mp3ResyncBudget in total. Anything that does not
// come out as mostly MP3 frames answers "unknown" (0) and the field is left
// out, rather than a length the clip does not have.

const (
	// mp3WalkMaxFrames caps the frames read one by one: about 14 minutes of
	// 44.1 kHz audio, far beyond any announcement.
	mp3WalkMaxFrames = 1 << 15
	// mp3ResyncBudget caps the bytes searched for a frame header after data
	// that is not one, over the whole walk.
	mp3ResyncBudget = 64 << 10
	// mp3MinCoverage is the share of the clip, in percent, that frames and
	// tags must account for before the sum is believed.
	mp3MinCoverage = 90
)

// announceDurationMs is the playing length of an announcement clip in
// milliseconds, or 0 when it cannot be told from the bytes (not MP3).
func announceDurationMs(audio []byte, mime string) int64 {
	if !isMP3Mime(mime) {
		return 0
	}
	return mp3DurationMs(audio)
}

// mp3DurationMs walks the Layer III frame headers of b and sums the frames'
// durations. 0 when b is not recognisably MP3.
func mp3DurationMs(b []byte) int64 {
	var ms float64
	frames, accounted, pos, resync := 0, 0, 0, 0
	// chained: pos is exactly where the previous frame ended, so a header
	// there is believed on its own. Anywhere else (the start, after a tag or
	// junk) the frame after it has to line up too.
	chained := false
	for pos < len(b) {
		if n := id3v2Len(b[pos:]); n > 0 {
			accounted += n
			pos += n
			chained = false
			continue
		}
		if len(b)-pos == 128 && bytes.HasPrefix(b[pos:], []byte("TAG")) { // ID3v1 trailer
			accounted += 128
			break
		}
		var f mp3Frame
		var ok bool
		if chained {
			f, ok = parseMP3Frame(b[pos:])
		} else {
			f, ok = mp3FrameAt(b, pos)
		}
		if !ok {
			chained = false
			next, scanned := mp3Resync(b, pos+1, mp3ResyncBudget-resync)
			resync += scanned
			if next < 0 {
				break
			}
			pos = next
			continue
		}
		chained = true
		ms += float64(f.samples()) * 1000 / float64(f.sampleRate)
		accounted += min(f.length, len(b)-pos)
		pos += f.length
		frames++
		if frames >= mp3WalkMaxFrames && pos < len(b) {
			// The rest at the rate walked so far: exact for constant bitrate,
			// close for variable.
			rest := len(b) - pos
			ms += ms * float64(rest) / float64(accounted)
			accounted += rest
			break
		}
	}
	if frames == 0 || accounted*100 < len(b)*mp3MinCoverage {
		return 0
	}
	return int64(ms + 0.5)
}

// mp3FrameAt parses the frame header at b[pos:] and accepts it only when
// what follows the frame is the end of the clip, a tag, or another frame
// header: one header alone could be a stray sync word in other data.
func mp3FrameAt(b []byte, pos int) (mp3Frame, bool) {
	f, ok := parseMP3Frame(b[pos:])
	if !ok {
		return mp3Frame{}, false
	}
	next := pos + f.length
	if next >= len(b) {
		return f, true
	}
	rest := b[next:]
	if id3v2Len(rest) > 0 || (len(rest) == 128 && bytes.HasPrefix(rest, []byte("TAG"))) {
		return f, true
	}
	if _, ok := parseMP3Frame(rest); ok {
		return f, true
	}
	return mp3Frame{}, false
}

// mp3Resync looks for the next frame (or ID3v2 tag) from b[from:], reading at
// most budget bytes. It returns the offset, or -1, and how many bytes it read.
func mp3Resync(b []byte, from, budget int) (int, int) {
	end := min(len(b), from+max(budget, 0))
	for i := from; i < end; i++ {
		if b[i] == 0xFF {
			if _, ok := mp3FrameAt(b, i); ok {
				return i, i - from + 1
			}
		} else if b[i] == 'I' && id3v2Len(b[i:]) > 0 {
			return i, i - from + 1
		}
	}
	return -1, end - from
}

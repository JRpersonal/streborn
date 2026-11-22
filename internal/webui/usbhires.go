package webui

// Hi-res files on the USB stick (#1136, docs/streaming/usb-stick.md).
//
// Live on the Portable (FW 27.0.6, 2026-11-21): a FLAC at 24 bit / 96 kHz is
// answered with AUDIO_ERROR_DECODER and a 96 kHz WAV stays BUFFERING for good;
// FLAC 16/44.1, AAC, Ogg, WAV 44.1 and ALAC play. The files stay listed (the
// listing reads no file). When one is played, the agent reads the sample rate
// from its header (usbmedia.ReadAudioFormat: FLAC and WAV; M4A is not read)
// and, above 48 kHz, does not hand it to the speaker:
//
//   - a single file (/api/play, a song key from the app or the phone) is
//     refused with code library-file-hires and a sentence the app and the
//     phone page show in the user's language;
//   - a folder (/api/queue, /api/library/play-folder, a folder card, a folder
//     key) steps over such a track (queueItem.Skip) instead of stalling on
//     it, and says how many it stepped over (skippedHiRes); a folder with
//     nothing else in it is refused like a single file;
//   - a hardware key press has no screen: the agent logs why and plays
//     nothing.
//
// A stick file the speaker refuses to decode anyway (AUDIO_ERROR_DECODER for
// a format the header check does not read) is noted (usbNoteDecoderError) and
// shown by GET /api/queue as usbRefused while it is still the play on record.

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/usbmedia"
)

// libraryFileHiResCode is the code of a stick file refused for its sample
// rate. The "library-" prefix makes the phone page read it like the other
// library refusals; the desktop app maps it by its code (friendlyErrorText).
const libraryFileHiResCode = "library-file-hires"

// usbHiResMessage is the English sentence of the refusal.
const usbHiResMessage = "This speaker cannot play this file: it has a sample rate above 48 kHz."

// usbFileFormat reads the format of a file on this speaker's own stick from
// its header. ok is false for any other URL, for a stick that is not served,
// and for a format the header check does not read.
func usbFileFormat(lib *usbmedia.Library, raw string) (usbmedia.AudioFormat, bool) {
	if lib == nil || !isUSBLoopbackFile(raw) {
		return usbmedia.AudioFormat{}, false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return usbmedia.AudioFormat{}, false
	}
	p := strings.TrimPrefix(u.Path, usbmedia.FilePrefix)
	if p == u.Path || p == "" {
		return usbmedia.AudioFormat{}, false
	}
	return lib.AudioFormat(p)
}

// usbServedLib is the served stick's library, or nil, after one look.
func (s *Server) usbServedLib() *usbmedia.Library {
	lib, _, ok := s.usbServed(true)
	if !ok {
		return nil
	}
	return lib
}

// usbHiResFormat reports whether raw is a file on this speaker's stick with a
// sample rate the speaker cannot play, and its format. Only a stick URL costs
// anything: one look at the stick and a header read of a few dozen bytes.
func (s *Server) usbHiResFormat(raw string) (usbmedia.AudioFormat, bool) {
	if !isUSBLoopbackFile(raw) {
		return usbmedia.AudioFormat{}, false
	}
	f, ok := usbFileFormat(s.usbServedLib(), raw)
	return f, ok && f.TooHighForTheSpeaker()
}

// usbHiResRefusal is the answer to a single play of such a file, nil for any
// other URL. slot is 0 for a play that is not a key.
func (s *Server) usbHiResRefusal(raw, title string, slot int) *playRefusal {
	f, hi := s.usbHiResFormat(raw)
	if !hi {
		return nil
	}
	s.logger.Warn("usb music: not handing the speaker a file it cannot play (sample rate above 48 kHz)",
		"title", title, "sampleRate", f.SampleRate, "bits", f.Bits, "slot", slot)
	body := map[string]any{
		"error": usbHiResMessage, "code": libraryFileHiResCode,
		"sampleRate": f.SampleRate, "title": title,
	}
	if slot > 0 {
		body["slot"] = slot
	}
	return jsonRefusal(http.StatusUnprocessableEntity, body)
}

// errUSBFolderAllHiRes is a folder whose every track is such a file.
var errUSBFolderAllHiRes = errors.New("usb music: every track of the folder has a sample rate above 48 kHz")

// usbFolderRefusal is the answer to a folder start that failed because every
// track is too high for the speaker, nil for any other error.
func usbFolderRefusal(err error) *playRefusal {
	if !errors.Is(err, errUSBFolderAllHiRes) {
		return nil
	}
	return jsonRefusal(http.StatusUnprocessableEntity, map[string]any{
		"error": usbHiResMessage, "code": libraryFileHiResCode, "folder": true,
	})
}

// usbMarkHiRes returns items with every stick file the speaker cannot play
// marked Skip, and how many it marked. Items that are no stick files cost
// nothing; the stick is looked at once for the whole folder.
func (s *Server) usbMarkHiRes(items []queueItem) ([]queueItem, int) {
	stick := false
	for _, it := range items {
		if isUSBLoopbackFile(it.URL) {
			stick = true
			break
		}
	}
	if !stick {
		return items, 0
	}
	lib := s.usbServedLib()
	if lib == nil {
		return items, 0
	}
	out := append([]queueItem(nil), items...)
	n := 0
	for i := range out {
		if out[i].Skip {
			n++
			continue
		}
		if f, ok := usbFileFormat(lib, out[i].URL); ok && f.TooHighForTheSpeaker() {
			out[i].Skip = true
			n++
		}
	}
	return out, n
}

// usbDecodeRefusal is what GET /api/queue says about a stick file the speaker
// refused to decode.
type usbDecodeRefusal struct {
	Title string `json:"title,omitempty"`
	// Reason is "decoder" (AUDIO_ERROR_DECODER).
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// usbDecodeShowFor is how long the refusal stays shown while nothing else
// plays.
const usbDecodeShowFor = 10 * time.Minute

// usbNoteDecoderError records the speaker's AUDIO_ERROR_DECODER when what it
// was handed last is a file on this speaker's own stick. Called from the box
// error hook (NoteBoxError); quick, no I/O.
func (s *Server) usbNoteDecoderError(name string) {
	if !strings.Contains(strings.ToUpper(name), "DECODER") {
		return
	}
	_, boxURL := s.play.genAndURL()
	if !isUSBLoopbackFile(boxURL) {
		return
	}
	title := ""
	if lp := s.play.current(); lp != nil && lp.boxURL == boxURL {
		title = lp.title
	}
	s.usb.mu.Lock()
	first := s.usb.decodeURL != boxURL
	s.usb.decodeURL, s.usb.decodeTitle, s.usb.decodeAt = boxURL, title, time.Now()
	s.usb.decodeErr = fmt.Sprintf("%s: %s", name, title)
	s.usb.snap.LastDecoderError = s.usb.decodeErr
	s.usb.mu.Unlock()
	if first {
		s.logger.Warn("usb music: the speaker could not decode a file from the stick",
			"error", name, "title", title, "url", boxURL)
	}
}

// usbDecodeRefusalShown is the refusal while the refused file is still the
// play on record, nil otherwise.
func (s *Server) usbDecodeRefusalShown() *usbDecodeRefusal {
	s.usb.mu.Lock()
	u, title, at := s.usb.decodeURL, s.usb.decodeTitle, s.usb.decodeAt
	s.usb.mu.Unlock()
	if u == "" || time.Since(at) > usbDecodeShowFor {
		return nil
	}
	if _, boxURL := s.play.genAndURL(); boxURL != u {
		return nil
	}
	return &usbDecodeRefusal{Title: title, Reason: "decoder", At: at.UTC().Format(time.RFC3339)}
}

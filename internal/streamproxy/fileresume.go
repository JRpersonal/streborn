package streamproxy

// Reconnecting inside a finite file (#830).
//
// The reconnect loop was written for radio: a station's connection ends (CDN
// token expiry, a stall the watchdog cut), and the next GET picks the
// broadcast up wherever it is now. A finite file has no "now". Rebuilding its
// connection with a plain GET started it at byte 0 again, so the box, which
// already held the first part of the file, was fed that first part a second
// time into its running decode: the listener heard the recording jump back to
// the beginning.
//
// So the slot and raw handlers keep one fileResume per box connection. A
// fetch whose upstream is a file (a size, byte ranges served, no ICY) records
// that, and every byte forwarded to the box moves the offset on. A reconnect
// then asks for "Range: bytes=<offset>-". A server that answers the range
// with a 206 continues exactly there; one that ignores it and sends the whole
// file again is read past the offset first, so the box still gets the bytes
// that follow the ones it has. Anything that does not line up is an error,
// and the next attempt starts over the way it always did.
//
// A radio station never qualifies: it has no size and no ranges, so its
// reconnects stay exactly as they were.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// fileResume is the resume state of one box connection. It is only touched
// by the handler goroutine that owns that connection.
type fileResume struct {
	station string
	finite  bool
	total   int64 // file size in bytes
	next    int64 // offset of the first byte the box has not been sent
}

type fileResumeKey struct{}

// withFileResume attaches fr to the context the handler passes to streamOne.
func withFileResume(ctx context.Context, fr *fileResume) context.Context {
	return context.WithValue(ctx, fileResumeKey{}, fr)
}

// fileResumeFrom returns the resume state on ctx, or nil (every method is
// nil-safe, so a caller without one behaves exactly as before).
func fileResumeFrom(ctx context.Context) *fileResume {
	fr, _ := ctx.Value(fileResumeKey{}).(*fileResume)
	return fr
}

// offsetFor returns the offset a reconnect of station should ask for, 0 when
// it is not a file or nothing has been sent yet.
func (fr *fileResume) offsetFor(station string) int64 {
	if fr == nil || !fr.finite || fr.station != station {
		return 0
	}
	return fr.next
}

// totalFor returns the size of station's file, 0 when unknown.
func (fr *fileResume) totalFor(station string) int64 {
	if fr == nil || fr.station != station {
		return 0
	}
	return fr.total
}

// align learns from resp whether station is a finite file and returns the
// file offset of the first byte resp's body yields. resumeAt is the offset
// this fetch asked for (0 for a plain fetch). On a 200 to a resume request the
// body is read past resumeAt here, so it, too, starts where the box is. An
// error is only ever returned for a resume request, i.e. a reconnect whose
// headers already went to the box.
func (fr *fileResume) align(station string, resp *http.Response, partial bool, resumeAt int64) (int64, error) {
	if fr == nil {
		return 0, nil
	}
	if partial {
		start, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok {
			fr.forget()
			if resumeAt == 0 {
				// The box's own offset request: passed through as before,
				// only not resumable.
				return 0, nil
			}
			return 0, fmt.Errorf("unusable Content-Range %q", resp.Header.Get("Content-Range"))
		}
		if resumeAt > 0 && (start != resumeAt || total != fr.total) {
			prevTotal := fr.total
			fr.forget()
			return 0, fmt.Errorf("asked for offset %d of %d bytes, got %d of %d", resumeAt, prevTotal, start, total)
		}
		*fr = fileResume{station: station, finite: total > 0, total: total, next: start}
		return start, nil
	}
	if resumeAt > 0 {
		// The server ignored the range and sent the file from the top. Same
		// file only: a different size means it changed under us, and the box
		// cannot be continued into another file.
		if resp.ContentLength != fr.total {
			fr.forget()
			return 0, fmt.Errorf("file size changed from %d to %d", fr.total, resp.ContentLength)
		}
		if _, err := io.CopyN(io.Discard, resp.Body, resumeAt); err != nil {
			fr.forget()
			return 0, fmt.Errorf("skipping to offset %d: %w", resumeAt, err)
		}
		return resumeAt, nil
	}
	finite := resp.ContentLength > 0 && icyMetaint(resp.Header) == 0 &&
		strings.EqualFold(strings.TrimSpace(resp.Header.Get("Accept-Ranges")), "bytes")
	*fr = fileResume{station: station, finite: finite}
	if finite {
		fr.total = resp.ContentLength
	}
	return 0, nil
}

// delivered records that the box has every byte of station up to off.
func (fr *fileResume) delivered(station string, off int64) {
	if fr == nil || fr.station != station || off <= fr.next {
		return
	}
	fr.next = off
}

// complete reports whether every byte of station's file has gone to the box.
func (fr *fileResume) complete(station string, off int64) bool {
	return fr != nil && fr.finite && fr.station == station && fr.total > 0 && off >= fr.total
}

// forget drops the state, so the next fetch starts over from byte 0.
func (fr *fileResume) forget() {
	*fr = fileResume{}
}

// parseContentRange reads "bytes start-end/total". total is 0 when the server
// sent "*".
func parseContentRange(h string) (start, total int64, ok bool) {
	v := strings.TrimSpace(h)
	if !strings.HasPrefix(strings.ToLower(v), "bytes ") {
		return 0, 0, false
	}
	v = strings.TrimSpace(v[len("bytes "):])
	slash := strings.IndexByte(v, '/')
	dash := strings.IndexByte(v, '-')
	if slash < 0 || dash < 0 || dash > slash {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(v[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	if t := strings.TrimSpace(v[slash+1:]); t != "*" {
		total, err = strconv.ParseInt(t, 10, 64)
		if err != nil || total < 0 {
			return 0, 0, false
		}
	}
	return start, total, true
}

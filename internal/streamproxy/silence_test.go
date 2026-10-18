package streamproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A speaker woken for a group that fetches a key which is gone gets silence,
// not a 404: the 404 surfaced as a firmware error after the zone join and took
// the group down (Küche, 2026-10-17).
func TestMissingSlotServesSilenceDuringGroupWake(t *testing.T) {
	prevSlot, prevAt := lastMiss.slot, lastMiss.at
	t.Cleanup(func() { lastMiss.slot, lastMiss.at = prevSlot, prevAt })
	lastMiss.slot, lastMiss.at = 0, time.Time{}

	s := New(presets.New(), silentLogger())
	s.SetHoldMissingSlotFn(func() bool { return true })
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	s.handle(w, httptest.NewRequest(http.MethodGet, "/stream/2", nil).WithContext(ctx))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("status %d, type %q, want 200 audio/mpeg", w.Code, w.Header().Get("Content-Type"))
	}
	body := w.Body.Bytes()
	if len(body) < 8*len(silenceFrame) || len(body)%len(silenceFrame) != 0 {
		t.Fatalf("body is %d bytes, want whole silent frames", len(body))
	}
	if body[0] != 0xFF || body[1]&0xE0 != 0xE0 {
		t.Fatalf("body does not start on an MP3 frame sync: % x", body[:4])
	}
	if _, _, ok := LastSlotMiss(); ok {
		t.Error("silence is not a refusal, no slot miss may be recorded")
	}
}

// Outside a group wake the missing key is refused as before.
func TestMissingSlotStill404sOutsideGroupWake(t *testing.T) {
	for _, fn := range []func() bool{nil, func() bool { return false }} {
		s := New(presets.New(), silentLogger())
		if fn != nil {
			s.SetHoldMissingSlotFn(fn)
		}
		w := httptest.NewRecorder()
		s.handle(w, httptest.NewRequest(http.MethodGet, "/stream/2", nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404", w.Code)
		}
	}
}

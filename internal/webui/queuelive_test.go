package webui

import (
	"testing"
	"time"
)

// TestQueueLiveURL pins the streamproxy-facing resolver for a box-native
// /stream/<slot> fetch of a queue preset: it must (1) return nothing for an
// unarmed slot, (2) signal "hold" (recalling=true) once armed while the first
// track is still loading, (3) never let one slot's fetch borrow another slot's
// queue, (4) resolve the current track once the queue loads, and (5) stop
// signalling "hold" after the in-flight window while still serving the track.
func TestQueueLiveURL(t *testing.T) {
	s := &Server{queue: newPlayQueue()}

	if url, rec := s.QueueLiveURL(6); url != "" || rec {
		t.Fatalf("unarmed slot must return empty/false, got %q/%v", url, rec)
	}

	// Armed, but the queue has not loaded a track yet: hold.
	s.armQueueRecall(6)
	if url, rec := s.QueueLiveURL(6); url != "" || !rec {
		t.Fatalf("armed slot with empty queue must return empty/true, got %q/%v", url, rec)
	}
	// A fetch for a different slot must never borrow slot 6's recall.
	if url, rec := s.QueueLiveURL(3); url != "" || rec {
		t.Fatalf("other slot must return empty/false, got %q/%v", url, rec)
	}

	// Queue loads its first track: resolve to it, still within the hold window.
	s.queue.load([]queueItem{{URL: "http://nas/track0.flac"}}, 0, false, repeatOff)
	if url, rec := s.QueueLiveURL(6); url != "http://nas/track0.flac" || !rec {
		t.Fatalf("loaded queue must return track0/true, got %q/%v", url, rec)
	}

	// Past the in-flight window: the track still resolves (a steady source
	// switch is still served) but the proxy is no longer told to hold.
	s.queueMu.Lock()
	s.queueRecallAt = time.Now().Add(-2 * queueRecallInFlight)
	s.queueMu.Unlock()
	if url, rec := s.QueueLiveURL(6); url != "http://nas/track0.flac" || rec {
		t.Fatalf("after the in-flight window: track0/false expected, got %q/%v", url, rec)
	}
}

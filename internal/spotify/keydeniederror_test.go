package spotify

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A play refused for the audio key is told by the error itself, whichever of
// the two messages explains it. The app recall used to look for the words
// "audio key denied", which the back-off message does not contain, and so
// answered a refused play with another play (discussion #1947).
func TestRefusedKeyPlayErrorsAreRecognisedWithoutTheirWording(t *testing.T) {
	play := func(t *testing.T, failLine string) error {
		t.Helper()
		var m *Manager
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/player/play" {
				m.mu.Lock()
				m.lastPlayFailLine, m.lastPlayFailAt = failLine, time.Now()
				m.mu.Unlock()
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(ts.Close)
		m = newTestManagerAt(t, ts.URL)
		err := m.Play(context.Background(), "spotify:playlist:abc", PlayOptions{})
		if err == nil {
			t.Fatal("a play the engine answered with 500 reported success")
		}
		return err
	}
	t.Run("refused during the back-off", func(t *testing.T) {
		err := play(t, strings.ToLower(`level=error msg="failed handling request play" error="spotify refused the audio key for several tracks in a row: not asking again for 25s"`))
		if !strings.Contains(err.Error(), keyDeniedBackoffHint) || strings.Contains(err.Error(), "audio key denied") {
			t.Fatalf("setup: not the back-off message: %v", err)
		}
		if !IsAudioKeyDenied(err) || !IsAudioKeyDenied(fmt.Errorf("recall: %w", err)) {
			t.Fatalf("the back-off refusal is not recognised as a refused key: %v", err)
		}
	})
	t.Run("a single refused key", func(t *testing.T) {
		err := play(t, `failed retrieving aes key with code 1`)
		if !strings.Contains(err.Error(), keyDeniedItemHint) {
			t.Fatalf("setup: not the single-key message: %v", err)
		}
		if !IsAudioKeyDenied(err) {
			t.Fatalf("a refused key is not recognised: %v", err)
		}
	})
	t.Run("a connection that was not ready is no refusal", func(t *testing.T) {
		err := play(t, `failed retrieving audio key: accesspoint closed`)
		if !strings.Contains(err.Error(), "again") {
			t.Fatalf("setup: no hint attached: %v", err)
		}
		if IsAudioKeyDenied(err) {
			t.Fatalf("a closed connection was taken for a refused key: %v", err)
		}
	})
	t.Run("an unexplained failure is no refusal", func(t *testing.T) {
		if err := play(t, ""); IsAudioKeyDenied(err) {
			t.Fatalf("a bare failure was taken for a refused key: %v", err)
		}
		if IsAudioKeyDenied(nil) || IsAudioKeyDenied(ErrNoSpotifySession) {
			t.Fatal("nil or the no-session error was taken for a refused key")
		}
	})
}

// The recall the engine accepts and then refuses to load (the other place the
// back-off message is returned from) carries the same mark.
func TestBackoffRefusalAfterAnAcceptedPlayIsARefusedKey(t *testing.T) {
	err := NewAudioKeyDeniedError(nil, keyDeniedBackoffHint)
	if err.Error() != "spotify play refused: "+keyDeniedBackoffHint {
		t.Fatalf("message changed: %q", err.Error())
	}
	if !IsAudioKeyDenied(err) {
		t.Fatal("not recognised as a refused key")
	}
}

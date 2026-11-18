package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/webhooks"
)

func likeKeyEvent(name string, prod boxlog.Producer, state boxlog.State) boxlog.KeyEvent {
	return boxlog.KeyEvent{Key: boxlog.KeyNumber(name), Name: name, Producer: prod,
		State: state, Origin: boxlog.OriginTrace, At: time.Now()}
}

// settleLikes gives the handler's goroutines time to run.
func settleLikes() { time.Sleep(150 * time.Millisecond) }

// TestThumbsUpReachesTheLikeHandler: a physical press of thumbs up with
// nothing on the key reaches the like handler once; a release, thumbs down,
// the other transport keys and an app-sent key do not (#1165).
func TestThumbsUpReachesTheLikeHandler(t *testing.T) {
	wh, err := webhooks.Load(filepath.Join(t.TempDir(), "webhooks.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var likes atomic.Int32
	h := &presetWsHandler{logger: slog.Default(), webhooks: wh, onLikeKey: func() { likes.Add(1) }}

	h.OnKeyEvent(likeKeyEvent("THUMBS_UP", boxlog.ProducerIRRemote, boxlog.StatePressed))
	settleLikes()
	if got := likes.Load(); got != 1 {
		t.Fatalf("thumbs up reached the like handler %d times, want 1", got)
	}

	h.OnKeyEvent(likeKeyEvent("THUMBS_UP", boxlog.ProducerIRRemote, boxlog.StateReleased))
	h.OnKeyEvent(likeKeyEvent("THUMBS_UP", boxlog.ProducerGabbo, boxlog.StatePressed))
	for _, other := range []string{"THUMBS_DOWN", "NEXT_TRACK", "PREV_TRACK", "PLAY_PAUSE", "PRESET_1"} {
		h.OnKeyEvent(likeKeyEvent(other, boxlog.ProducerIRRemote, boxlog.StatePressed))
	}
	settleLikes()
	if got := likes.Load(); got != 1 {
		t.Fatalf("something other than a thumbs up press liked a song: %d calls", got)
	}
}

// A handler without a webhook store still likes: nil-safe.
func TestThumbsUpLikesWithoutAWebhookStore(t *testing.T) {
	var likes atomic.Int32
	h := &presetWsHandler{logger: slog.Default(), onLikeKey: func() { likes.Add(1) }}
	h.OnKeyEvent(likeKeyEvent("THUMBS_UP", boxlog.ProducerIRRemote, boxlog.StatePressed))
	settleLikes()
	if got := likes.Load(); got != 1 {
		t.Fatalf("likes = %d, want 1", got)
	}
}

// TestThumbsUpKeepsWhatItCarries: a key action or a webhook on thumbs up
// keeps the key; the like default only fills an empty key.
func TestThumbsUpKeepsWhatItCarries(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	t.Run("its own webhook", func(t *testing.T) {
		hits.Store(0)
		wh, err := webhooks.Load(filepath.Join(t.TempDir(), "webhooks.json"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := wh.Set(webhooks.Config{Buttons: map[string]webhooks.Trigger{
			webhooks.KeyThumbsUp: {Action: webhooks.Action{Enabled: true, URL: target.URL + "/up"}},
		}}); err != nil {
			t.Fatal(err)
		}
		var likes atomic.Int32
		h := &presetWsHandler{logger: slog.Default(), webhooks: wh, onLikeKey: func() { likes.Add(1) }}
		h.OnKeyEvent(likeKeyEvent("THUMBS_UP", boxlog.ProducerIRRemote, boxlog.StatePressed))
		deadline := time.Now().Add(2 * time.Second)
		for hits.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if hits.Load() != 1 || likes.Load() != 0 {
			t.Fatalf("webhook fired %d times, likes = %d; want 1 and 0", hits.Load(), likes.Load())
		}
	})

	t.Run("the shared thumbs webhook", func(t *testing.T) {
		hits.Store(0)
		wh, err := webhooks.Load(filepath.Join(t.TempDir(), "webhooks.json"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := wh.Set(webhooks.Config{Thumb: webhooks.Action{Enabled: true, URL: target.URL + "/thumb"}}); err != nil {
			t.Fatal(err)
		}
		var likes atomic.Int32
		h := &presetWsHandler{logger: slog.Default(), webhooks: wh, onLikeKey: func() { likes.Add(1) }}
		h.OnKeyEvent(likeKeyEvent("THUMBS_UP", boxlog.ProducerIRRemote, boxlog.StatePressed))
		deadline := time.Now().Add(2 * time.Second)
		for hits.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if hits.Load() != 1 || likes.Load() != 0 {
			t.Fatalf("webhook fired %d times, likes = %d; want 1 and 0", hits.Load(), likes.Load())
		}
	})

	t.Run("a webhook on thumbs down only", func(t *testing.T) {
		hits.Store(0)
		wh, err := webhooks.Load(filepath.Join(t.TempDir(), "webhooks.json"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := wh.Set(webhooks.Config{Buttons: map[string]webhooks.Trigger{
			webhooks.KeyThumbsDown: {Action: webhooks.Action{Enabled: true, URL: target.URL + "/down"}},
		}}); err != nil {
			t.Fatal(err)
		}
		var likes atomic.Int32
		h := &presetWsHandler{logger: slog.Default(), webhooks: wh, onLikeKey: func() { likes.Add(1) }}
		h.OnKeyEvent(likeKeyEvent("THUMBS_UP", boxlog.ProducerIRRemote, boxlog.StatePressed))
		settleLikes()
		if likes.Load() != 1 || hits.Load() != 0 {
			t.Fatalf("likes = %d, webhook hits = %d; want 1 and 0", likes.Load(), hits.Load())
		}
	})

	t.Run("a key action", func(t *testing.T) {
		ka, err := keyactions.Load(filepath.Join(t.TempDir(), "key-actions.json"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := ka.Set(keyactions.Document{Keys: map[string]string{keyactions.KeyThumbsUp: keyactions.ActionShuffle}}, nil); err != nil {
			t.Fatal(err)
		}
		var likes, modes atomic.Int32
		h := &presetWsHandler{logger: slog.Default(), keyActions: ka,
			onPlayModeKey: func(string) { modes.Add(1) }, onLikeKey: func() { likes.Add(1) }}
		h.OnKeyEvent(likeKeyEvent("THUMBS_UP", boxlog.ProducerIRRemote, boxlog.StatePressed))
		settleLikes()
		if modes.Load() != 1 || likes.Load() != 0 {
			t.Fatalf("shuffle ran %d times, likes = %d; want 1 and 0", modes.Load(), likes.Load())
		}
	})
}

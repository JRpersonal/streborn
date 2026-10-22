package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/webhooks"
)

// TestSnoozeKeySkipsWebhook: a thumbs key that carries snooze hands every
// physical press to the alarm clock and does NOT fire its webhook; an
// app-sent key does nothing (discussion #1415).
func TestSnoozeKeySkipsWebhook(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	wh, err := webhooks.Load(filepath.Join(t.TempDir(), "webhooks.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := wh.Set(webhooks.Config{Buttons: map[string]webhooks.Trigger{
		webhooks.KeyThumbsDown: {Action: webhooks.Action{Enabled: true, URL: target.URL + "/down"}},
	}}); err != nil {
		t.Fatal(err)
	}
	ka, err := keyactions.Load(filepath.Join(t.TempDir(), "key-actions.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ka.Set(keyactions.Document{Keys: map[string]string{keyactions.KeyThumbsDown: keyactions.ActionSnooze}}, nil); err != nil {
		t.Fatal(err)
	}
	pressed := make(chan string, 4)
	h := &presetWsHandler{logger: slog.Default(), webhooks: wh, keyActions: ka,
		onSnoozeKey: func(key string) { pressed <- key }}
	press := func(prod boxlog.Producer) {
		h.OnKeyEvent(boxlog.KeyEvent{Key: boxlog.KeyNumber("THUMBS_DOWN"), Name: "THUMBS_DOWN", Producer: prod,
			State: boxlog.StatePressed, Origin: boxlog.OriginTrace, At: time.Now()})
	}
	press(boxlog.ProducerIRRemote)
	select {
	case key := <-pressed:
		if key != keyactions.KeyThumbsDown {
			t.Errorf("snooze handed key %q", key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the snooze key never reached the alarm clock")
	}
	press(boxlog.ProducerGabbo)
	time.Sleep(150 * time.Millisecond)
	if len(pressed) != 0 {
		t.Error("an app-sent key reached the snooze")
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("the webhook fired %d times on a key that carries snooze", got)
	}
}

// TestAuxSnoozeSkipsWebhook: AUX bound to snooze never fires the aux webhook
// (the key action wins), and the source change still reaches the alarm clock,
// which decides whether an alarm is ringing.
func TestAuxSnoozeSkipsWebhook(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	wh, err := webhooks.Load(filepath.Join(t.TempDir(), "webhooks.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := wh.Set(webhooks.Config{Buttons: map[string]webhooks.Trigger{
		"aux": {Action: webhooks.Action{Enabled: true, URL: target.URL + "/aux"}},
	}}); err != nil {
		t.Fatal(err)
	}
	ka, err := keyactions.Load(filepath.Join(t.TempDir(), "key-actions.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen [][2]string
	h := &presetWsHandler{logger: slog.Default(), webhooks: wh, keyActions: ka,
		onAlarmSource: func(from, to string) {
			mu.Lock()
			seen = append(seen, [2]string{from, to})
			mu.Unlock()
		}}

	// Unbound: the webhook fires as before.
	h.OnSourceAux(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Fatal("the aux webhook did not fire on an unbound AUX")
	}

	if err := ka.Set(keyactions.Document{Keys: map[string]string{keyactions.KeyAux: keyactions.ActionSnooze}}, nil); err != nil {
		t.Fatal(err)
	}
	// The aux webhook has a 2 s per-trigger gap; wait it out so a fire here
	// would really show.
	time.Sleep(2100 * time.Millisecond)
	h.OnSourceAux(context.Background())
	time.Sleep(200 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Errorf("the aux webhook fired on an AUX that carries snooze: %d hits", got)
	}

	h.OnSourceChanged(context.Background(), "UPNP", "AUX")
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != [2]string{"UPNP", "AUX"} {
		t.Errorf("the alarm clock saw %v, want the one UPNP -> AUX change", seen)
	}
}

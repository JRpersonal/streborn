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
	"github.com/JRpersonal/streborn/internal/groupkeys"
	"github.com/JRpersonal/streborn/internal/webhooks"
)

// TestKeyTraceExplainsBareFrame: a transport key named by the trace explains
// the bare gabbo frame (heuristic stands down); a preset key does not (the
// #342 dead-key path must keep running); an app-sent key does not either.
func TestKeyTraceExplainsBareFrame(t *testing.T) {
	h := &presetWsHandler{}
	if h.keyTraceExplainsBareFrame() {
		t.Fatal("nil reader must not explain anything")
	}
	push := func(name string, prod boxlog.Producer) {
		r := boxlog.New(nil, nil, nil)
		ev := boxlog.KeyEvent{Key: boxlog.KeyNumber(name), Name: name, Producer: prod, State: boxlog.StatePressed, Origin: boxlog.OriginTrace, At: time.Now()}
		r.Inject(ev)
		h.keyTrace = r
	}
	push("THUMBS_UP", boxlog.ProducerIRRemote)
	if !h.keyTraceExplainsBareFrame() {
		t.Fatal("remote thumbs up must explain the bare frame")
	}
	push("PRESET_3", boxlog.ProducerIRRemote)
	if h.keyTraceExplainsBareFrame() {
		t.Fatal("a preset key must leave the #342 path active")
	}
	push("NEXT_TRACK", boxlog.ProducerGabbo)
	if h.keyTraceExplainsBareFrame() {
		t.Fatal("an app-sent key is not a physical press")
	}
}

// TestBoxFailureAttrs: the firmware's own playback failure reason is appended
// to the recall-exhausted warning only when it arrived inside the window.
func TestBoxFailureAttrs(t *testing.T) {
	h := &presetWsHandler{}
	if h.boxFailureAttrs() != nil {
		t.Fatal("nil reader must add nothing")
	}
	r := boxlog.New(nil, nil, nil)
	h.keyTrace = r
	if h.boxFailureAttrs() != nil {
		t.Fatal("no failure yet must add nothing")
	}
	const line = `Sep  6 18:02:12 taigan local0.err APServer[1877]: [(002101):AudioIF:ERROR]SERVER ERROR: Server state has terminal error = BAD_URL and m_nMaxRetryAttempt =3`
	r.InjectLine(line, time.Now().Add(-2*boxReasonWindow))
	if h.boxFailureAttrs() != nil {
		t.Fatal("a stale reason must add nothing")
	}
	r.InjectLine(line, time.Now())
	attrs := h.boxFailureAttrs()
	if len(attrs) != 4 || attrs[0] != "boxReason" || attrs[1] != string(boxlog.ClassPlayServerError) {
		t.Fatalf("attrs %v", attrs)
	}
}

// toggleSpy is a groupkeys.MasterClient that only records that a press
// reached the toggle.
type toggleSpy struct {
	mu    sync.Mutex
	zones int
}

func (t *toggleSpy) LiveZone(context.Context, groupkeys.Member) (groupkeys.LiveZone, error) {
	t.mu.Lock()
	t.zones++
	t.mu.Unlock()
	return groupkeys.LiveZone{}, nil
}
func (t *toggleSpy) Form(context.Context, groupkeys.Template) error   { return nil }
func (t *toggleSpy) Dissolve(context.Context, groupkeys.Member) error { return nil }
func (t *toggleSpy) Idle(context.Context, groupkeys.Member) (bool, error) {
	return false, nil
}
func (t *toggleSpy) PlayLast(context.Context, groupkeys.Member) error { return nil }

func (t *toggleSpy) reads() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.zones
}

// TestBoundKeySkipsWebhook: a physical press of a thumbs key that carries a
// saved group (#863) toggles the group and does NOT fire the key's webhook,
// while the other thumbs key, unbound, still fires its webhook.
func TestBoundKeySkipsWebhook(t *testing.T) {
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
		webhooks.KeyThumbsUp:   {Action: webhooks.Action{Enabled: true, URL: target.URL + "/up"}},
		webhooks.KeyThumbsDown: {Action: webhooks.Action{Enabled: true, URL: target.URL + "/down"}},
	}}); err != nil {
		t.Fatal(err)
	}
	gk, err := groupkeys.Load(filepath.Join(t.TempDir(), "group-keys.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := gk.Set(groupkeys.Document{
		Templates: []groupkeys.Template{{Name: "Evening", Master: groupkeys.Member{DeviceID: "AAAA", IP: "192.0.2.10"},
			Members: []groupkeys.Member{{DeviceID: "BBBB", IP: "192.0.2.11"}}}},
		Bindings: map[string]string{groupkeys.KeyThumbsUp: "Evening"},
	}); err != nil {
		t.Fatal(err)
	}
	spy := &toggleSpy{}
	gk.SetClient(spy)

	h := &presetWsHandler{logger: slog.Default(), webhooks: wh, groupKeys: gk}
	press := func(name string) {
		h.OnKeyEvent(boxlog.KeyEvent{Key: boxlog.KeyNumber(name), Name: name, Producer: boxlog.ProducerIRRemote,
			State: boxlog.StatePressed, Origin: boxlog.OriginTrace, At: time.Now()})
	}
	waitFor := func(cond func() bool) bool {
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(10 * time.Millisecond)
		}
		return true
	}

	press("THUMBS_UP")
	if !waitFor(func() bool { return spy.reads() >= 1 }) {
		t.Fatal("the bound key never reached the group toggle")
	}
	press("THUMBS_DOWN")
	if !waitFor(func() bool { return hits.Load() >= 1 }) {
		t.Fatal("the unbound key's webhook never fired")
	}
	// Settle, then make sure the bound key's webhook stayed silent.
	time.Sleep(150 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Fatalf("webhook target hit %d times, want exactly 1 (the unbound key)", got)
	}
}

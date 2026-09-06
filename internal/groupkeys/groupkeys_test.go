package groupkeys

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func evening() Template {
	return Template{
		Name:   "Evening",
		Master: Member{DeviceID: "AAAA", IP: "192.0.2.10"},
		Members: []Member{
			{DeviceID: "BBBB", IP: "192.0.2.11"},
			{DeviceID: "CCCC", IP: "192.0.2.12"},
		},
	}
}

func party() Template {
	return Template{
		Name:    "Party",
		Master:  Member{DeviceID: "DDDD", IP: "192.0.2.20"},
		Members: []Member{{DeviceID: "AAAA", IP: "192.0.2.10"}},
	}
}

// fakeMaster records the calls and answers from a scripted live zone.
type fakeMaster struct {
	mu    sync.Mutex
	zones map[string]LiveZone // keyed by master deviceID
	idle  bool
	calls []string
	// block makes Form wait on the channel, to hold a toggle in flight.
	block chan struct{}
	// formErr is returned by Form when set.
	formErr error
}

func (f *fakeMaster) rec(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeMaster) LiveZone(_ context.Context, m Member) (LiveZone, error) {
	f.rec("zone:" + m.DeviceID)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.zones[m.DeviceID], nil
}

func (f *fakeMaster) Form(_ context.Context, t Template) error {
	f.rec("form:" + t.Name)
	if f.block != nil {
		<-f.block
	}
	return f.formErr
}

func (f *fakeMaster) Dissolve(_ context.Context, m Member) error {
	f.rec("dissolve:" + m.DeviceID)
	f.mu.Lock()
	delete(f.zones, m.DeviceID)
	f.mu.Unlock()
	return nil
}

func (f *fakeMaster) Idle(_ context.Context, m Member) (bool, error) {
	f.rec("idle:" + m.DeviceID)
	return f.idle, nil
}

func (f *fakeMaster) PlayLast(_ context.Context, m Member) error {
	f.rec("play:" + m.DeviceID)
	return nil
}

func (f *fakeMaster) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newStore(t *testing.T, doc Document) *Store {
	t.Helper()
	s, err := Load(filepath.Join(t.TempDir(), "group-keys.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(doc); err != nil {
		t.Fatal(err)
	}
	return s
}

func equalCalls(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestStoreRoundTrip: a saved document comes back from the file byte-for-byte
// in meaning, and a missing file is an empty document.
func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group-keys.json")
	s, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); len(got.Templates) != 0 || len(got.Bindings) != 0 {
		t.Fatalf("fresh store must be empty, got %+v", got)
	}
	doc := Document{
		Templates: []Template{evening(), party()},
		Bindings:  map[string]string{KeyThumbsUp: "Evening", KeyThumbsDown: "Party"},
	}
	doc.Templates[0].Permanent = true
	if err := s.Set(doc); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Get()
	if len(got.Templates) != 2 || got.Templates[0].Name != "Evening" || !got.Templates[0].Permanent ||
		len(got.Templates[0].Members) != 2 || got.Templates[1].Master.IP != "192.0.2.20" {
		t.Fatalf("templates did not round-trip: %+v", got.Templates)
	}
	if got.Bindings[KeyThumbsUp] != "Evening" || got.Bindings[KeyThumbsDown] != "Party" {
		t.Fatalf("bindings did not round-trip: %+v", got.Bindings)
	}
	if !s2.Bound(KeyThumbsUp) || !s2.Bound(KeyThumbsDown) || s2.Bound("prev") {
		t.Fatal("Bound must follow the bindings")
	}
}

// TestValidateRejects: a save that would leave a press with nothing to do is
// refused instead of stored.
func TestValidateRejects(t *testing.T) {
	cases := map[string]Document{
		"no name":          {Templates: []Template{{Master: Member{IP: "192.0.2.1"}, Members: []Member{{IP: "192.0.2.2"}}}}},
		"no members":       {Templates: []Template{{Name: "x", Master: Member{IP: "192.0.2.1"}}}},
		"no master":        {Templates: []Template{{Name: "x", Members: []Member{{IP: "192.0.2.2"}}}}},
		"duplicate name":   {Templates: []Template{evening(), evening()}},
		"unknown template": {Templates: []Template{evening()}, Bindings: map[string]string{KeyThumbsUp: "Nope"}},
		"wrong key":        {Templates: []Template{evening()}, Bindings: map[string]string{"prev": "Evening"}},
	}
	for name, doc := range cases {
		if err := doc.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	ok := Document{Templates: []Template{evening()}, Bindings: map[string]string{KeyThumbsUp: " evening ", KeyThumbsDown: ""}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid document refused: %v", err)
	}
	if ok.Bindings[KeyThumbsUp] != "evening" {
		t.Fatalf("binding name must be trimmed, got %q", ok.Bindings[KeyThumbsUp])
	}
	if _, still := ok.Bindings[KeyThumbsDown]; still {
		t.Fatal("an empty binding must be dropped")
	}
}

// TestToggleFormsWhenIdle: no live group and an idle main speaker -> form,
// then bring the last station back so the press ends in music.
func TestToggleFormsWhenIdle(t *testing.T) {
	s := newStore(t, Document{Templates: []Template{evening()}, Bindings: map[string]string{KeyThumbsUp: "Evening"}})
	f := &fakeMaster{zones: map[string]LiveZone{}, idle: true}
	s.SetClient(f)
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeFormed {
		t.Fatalf("outcome %s", got)
	}
	want := []string{"zone:AAAA", "idle:AAAA", "form:Evening", "play:AAAA"}
	if got := f.list(); !equalCalls(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// TestToggleFormsWithoutResumeWhenPlaying: a main speaker already playing
// keeps its music; the press only forms the group.
func TestToggleFormsWithoutResumeWhenPlaying(t *testing.T) {
	s := newStore(t, Document{Templates: []Template{evening()}, Bindings: map[string]string{KeyThumbsUp: "Evening"}})
	f := &fakeMaster{zones: map[string]LiveZone{}, idle: false}
	s.SetClient(f)
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeFormed {
		t.Fatalf("outcome %s", got)
	}
	want := []string{"zone:AAAA", "idle:AAAA", "form:Evening"}
	if got := f.list(); !equalCalls(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// TestToggleDissolvesWhenLive: the template's group is live -> dissolve, no
// form, no resume. A superset zone (an extra member) still counts as live,
// and members match by address when the firmware id differs from the
// template's (two-chip chassis).
func TestToggleDissolvesWhenLive(t *testing.T) {
	s := newStore(t, Document{Templates: []Template{evening()}, Bindings: map[string]string{KeyThumbsUp: "Evening"}})
	f := &fakeMaster{zones: map[string]LiveZone{
		"AAAA": {Master: "AAAA", Members: []Member{
			{DeviceID: "BBBB", IP: "192.0.2.11"},
			{DeviceID: "FIRMWARE-ID", IP: "192.0.2.12"},
			{DeviceID: "EEEE", IP: "192.0.2.30"},
		}},
	}, idle: true}
	s.SetClient(f)
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeDissolved {
		t.Fatalf("outcome %s", got)
	}
	want := []string{"zone:AAAA", "dissolve:AAAA"}
	if got := f.list(); !equalCalls(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// TestToggleMasterKnownUnderFirmwareID: the live zone names the main speaker
// by its firmware id while the template carries the discovery MAC; SelfID
// bridges the two, so the press dissolves instead of re-forming forever.
func TestToggleMasterKnownUnderFirmwareID(t *testing.T) {
	s := newStore(t, Document{Templates: []Template{evening()}, Bindings: map[string]string{KeyThumbsUp: "Evening"}})
	f := &fakeMaster{zones: map[string]LiveZone{
		"AAAA": {Master: "SCM-AAAA", SelfID: "SCM-AAAA", Members: []Member{
			{DeviceID: "BBBB", IP: "192.0.2.11"}, {DeviceID: "CCCC", IP: "192.0.2.12"},
		}},
	}}
	s.SetClient(f)
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeDissolved {
		t.Fatalf("outcome %s", got)
	}
}

// TestToggleIncompleteGroupForms: the main speaker leads a zone that lacks a
// template member -> form (the form replaces the membership), not dissolve.
func TestToggleIncompleteGroupForms(t *testing.T) {
	s := newStore(t, Document{Templates: []Template{evening()}, Bindings: map[string]string{KeyThumbsUp: "Evening"}})
	f := &fakeMaster{zones: map[string]LiveZone{
		"AAAA": {Master: "AAAA", Members: []Member{{DeviceID: "BBBB", IP: "192.0.2.11"}}},
	}, idle: false}
	s.SetClient(f)
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeFormed {
		t.Fatalf("outcome %s", got)
	}
	want := []string{"zone:AAAA", "idle:AAAA", "form:Evening"}
	if got := f.list(); !equalCalls(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// TestToggleSwitchesFromOtherTemplate: the other key's template is live on a
// different main speaker -> dissolve it there, then form this one.
func TestToggleSwitchesFromOtherTemplate(t *testing.T) {
	s := newStore(t, Document{
		Templates: []Template{evening(), party()},
		Bindings:  map[string]string{KeyThumbsUp: "Evening", KeyThumbsDown: "Party"},
	})
	f := &fakeMaster{zones: map[string]LiveZone{
		"DDDD": {Master: "DDDD", Members: []Member{{DeviceID: "AAAA", IP: "192.0.2.10"}}},
	}, idle: true}
	s.SetClient(f)
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeSwitched {
		t.Fatalf("outcome %s", got)
	}
	want := []string{"zone:AAAA", "zone:DDDD", "dissolve:DDDD", "idle:AAAA", "form:Evening", "play:AAAA"}
	if got := f.list(); !equalCalls(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
}

// TestToggleUnboundAndBusyAndRateLimit: an unbound key does nothing; a press
// while the previous one runs is ignored; a press inside the 3 s gap is
// ignored too.
func TestToggleUnboundAndBusyAndRateLimit(t *testing.T) {
	s := newStore(t, Document{Templates: []Template{evening()}, Bindings: map[string]string{KeyThumbsUp: "Evening"}})
	f := &fakeMaster{zones: map[string]LiveZone{}, idle: false, block: make(chan struct{})}
	s.SetClient(f)
	if got := s.Toggle(context.Background(), KeyThumbsDown); got != OutcomeUnbound {
		t.Fatalf("unbound key: outcome %s", got)
	}
	done := make(chan Outcome, 1)
	go func() { done <- s.Toggle(context.Background(), KeyThumbsUp) }()
	// Wait until the first press is inside Form.
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls := f.list()
		if len(calls) > 0 && calls[len(calls)-1] == "form:Evening" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first press never reached Form")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeBusy {
		t.Fatalf("second press while in flight: outcome %s", got)
	}
	close(f.block)
	if got := <-done; got != OutcomeFormed {
		t.Fatalf("first press: outcome %s", got)
	}
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeRateLimit {
		t.Fatalf("press inside the gap: outcome %s", got)
	}
	// Turn the clock back on the stamp and the key acts again.
	s.togMu.Lock()
	s.lastAt[KeyThumbsUp] = time.Now().Add(-toggleMinGap)
	s.togMu.Unlock()
	f.block = nil
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeFormed {
		t.Fatalf("press after the gap: outcome %s", got)
	}
}

// TestToggleFailureIsRecorded: a failed form lands in the debug snapshot and
// does not leave the key marked in flight.
func TestToggleFailureIsRecorded(t *testing.T) {
	s := newStore(t, Document{Templates: []Template{evening()}, Bindings: map[string]string{KeyThumbsUp: "Evening"}})
	f := &fakeMaster{zones: map[string]LiveZone{}, idle: false, formErr: errors.New("setZone: boom")}
	s.SetClient(f)
	if got := s.Toggle(context.Background(), KeyThumbsUp); got != OutcomeFailed {
		t.Fatalf("outcome %s", got)
	}
	snap := s.Snapshot().(map[string]any)
	le, ok := snap["last_error"].(Action)
	if !ok || le.Outcome != OutcomeFailed || le.Error == "" {
		t.Fatalf("last_error not recorded: %+v", snap["last_error"])
	}
	s.togMu.Lock()
	inFlight := s.inFlight[KeyThumbsUp]
	s.togMu.Unlock()
	if inFlight {
		t.Fatal("key left in flight after a failure")
	}
}

// TestTemplateLiveMatching pins the matching rules on their own.
func TestTemplateLiveMatching(t *testing.T) {
	tpl := evening()
	if templateLive(tpl, LiveZone{}) {
		t.Fatal("standalone must not match")
	}
	if templateLive(tpl, LiveZone{Master: "ZZZZ", Members: []Member{{DeviceID: "BBBB"}, {DeviceID: "CCCC"}}}) {
		t.Fatal("a zone led by another speaker must not match")
	}
	if !templateLive(tpl, LiveZone{Master: "aaaa", Members: []Member{{DeviceID: "bbbb"}, {DeviceID: "cccc"}}}) {
		t.Fatal("ids must match case-insensitively")
	}
}

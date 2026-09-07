package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// The preset stash (#882): a removal keeps the speaker's keys on this PC, the
// next install puts them back onto an empty store, and nothing in between
// throws them away.

var stashKeys = []Preset{
	{Slot: 1, Name: "101 SMOOTH JAZZ", StreamURL: "https://a.example/smooth", Type: "radio", Bitrate: 128},
	{Slot: 6, Name: "Exclusively Rush", StreamURL: "https://b.example/rush", Type: "radio", Homepage: "https://b.example"},
}

func quietApp() *App {
	a := newTestApp()
	a.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return a
}

func TestPresetStashRoundTrip(t *testing.T) {
	isolateConfigDir(t)
	if err := savePresetStash("DEV-A", "192.0.2.10", stashKeys); err != nil {
		t.Fatal(err)
	}
	st, ok := loadPresetStash("DEV-A", "")
	if !ok || len(st.Presets) != 2 || st.Presets[1].Name != "Exclusively Rush" || st.Presets[0].Bitrate != 128 {
		t.Fatalf("round trip lost something: ok=%v %+v", ok, st)
	}
	if _, ok := loadPresetStash("DEV-B", ""); ok {
		t.Fatal("another speaker's id must find nothing")
	}
	// The app never learned the deviceID at removal time: the IP keys it, and
	// the install (which does know the id by then) still finds it.
	if err := savePresetStash("", "192.0.2.11", stashKeys); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPresetStash("DEV-C", "192.0.2.11"); !ok {
		t.Fatal("the IP-keyed fallback must be found when the id was unknown at removal")
	}
	dropPresetStash("DEV-A", "192.0.2.10")
	dropPresetStash("DEV-C", "192.0.2.11")
	if _, ok := loadPresetStash("DEV-A", "192.0.2.10"); ok {
		t.Fatal("dropped stash still loads")
	}
	if _, ok := loadPresetStash("", "192.0.2.11"); ok {
		t.Fatal("dropped IP-keyed stash still loads")
	}
}

// fakeAgent is an STR agent as far as the stash is concerned: a preset store
// that can be read and written, and the hardware sync.
type fakeAgent struct {
	mu     sync.Mutex
	store  []Preset
	puts   []int
	synced int
}

func (f *fakeAgent) serve(t *testing.T) (*httptest.Server, string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/presets":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.store)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/presets/"):
			var p Preset
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			f.store = append(f.store, p)
			f.puts = append(f.puts, p.Slot)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(p)
		case r.Method == http.MethodPost && r.URL.Path == "/api/box/sync-presets":
			f.synced++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","synced":2,"failed":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, "127.0.0.1", listenPort(t, srv)
}

func TestRestorePresetStashFillsAnEmptyStore(t *testing.T) {
	dir := isolateConfigDir(t)
	t.Setenv("LOCALAPPDATA", dir) // the OTA journal lives under it on Windows
	if err := savePresetStash("DEV-A", "", stashKeys); err != nil {
		t.Fatal(err)
	}
	agent := &fakeAgent{}
	_, host, port := agent.serve(t)
	a := quietApp()
	if n := a.restorePresetStash(host, port, "DEV-A"); n != 2 {
		t.Fatalf("restored = %d, want 2", n)
	}
	sort.Ints(agent.puts)
	if len(agent.puts) != 2 || agent.puts[0] != 1 || agent.puts[1] != 6 {
		t.Fatalf("PUTs = %v, want slots 1 and 6", agent.puts)
	}
	if agent.synced != 1 {
		t.Fatalf("the hardware keys must be re-synced once, got %d", agent.synced)
	}
	if _, ok := loadPresetStash("DEV-A", ""); ok {
		t.Fatal("a restored stash must be dropped")
	}
}

// A fresh store that already holds presets is left alone: kept keys must never
// overwrite what the user set since. The stash is dropped as no longer needed.
func TestRestorePresetStashIsNoOpOnNonEmptyStore(t *testing.T) {
	isolateConfigDir(t)
	if err := savePresetStash("DEV-A", "", stashKeys); err != nil {
		t.Fatal(err)
	}
	agent := &fakeAgent{store: []Preset{{Slot: 2, Name: "Kept", StreamURL: "https://k.example/s", Type: "radio"}}}
	_, host, port := agent.serve(t)
	a := quietApp()
	if n := a.restorePresetStash(host, port, "DEV-A"); n != 0 {
		t.Fatalf("restored = %d, want 0", n)
	}
	if len(agent.puts) != 0 || agent.synced != 0 {
		t.Fatalf("nothing may be written to a non-empty store: puts=%v synced=%d", agent.puts, agent.synced)
	}
	if _, ok := loadPresetStash("DEV-A", ""); ok {
		t.Fatal("a stash the store does not need must be dropped")
	}
}

// An unreadable store keeps the stash for a later install.
func TestRestorePresetStashKeepsStashWhenStoreUnreadable(t *testing.T) {
	isolateConfigDir(t)
	if err := savePresetStash("DEV-A", "", stashKeys); err != nil {
		t.Fatal(err)
	}
	a := quietApp()
	if n := a.restorePresetStash("127.0.0.1", deadPort(t), "DEV-A"); n != 0 {
		t.Fatalf("restored = %d, want 0", n)
	}
	if _, ok := loadPresetStash("DEV-A", ""); !ok {
		t.Fatal("the stash must survive an install whose store could not be read")
	}
}

// The removal reads the keys through the agent, and through the store file
// over SSH when the agent does not answer (an ST10's firewall drops the agent
// ports from the LAN; SSH is what the removal runs on anyway).
func TestStashPresetsBeforeUninstallFallsBackToSSH(t *testing.T) {
	isolateConfigDir(t)
	orig := stashSSHRead
	t.Cleanup(func() { stashSSHRead = orig })
	sshReads := 0
	stashSSHRead = func(string) (string, error) {
		sshReads++
		return `{"presets":[{"slot":3,"name":"WDCB Jazz","stream_url":"https://wdcb.example/s","type":"radio"},{"slot":4,"name":"","stream_url":"","type":"radio"}]}`, nil
	}
	a := quietApp()
	if n := a.stashPresetsBeforeUninstall("127.0.0.1", deadPort(t), "DEV-A"); n != 1 {
		t.Fatalf("kept = %d, want 1 (the nameless slot is not a key)", n)
	}
	if sshReads != 1 {
		t.Fatalf("the SSH fallback must be used once when the agent does not answer, got %d", sshReads)
	}
	st, ok := loadPresetStash("DEV-A", "127.0.0.1")
	if !ok || len(st.Presets) != 1 || st.Presets[0].Name != "WDCB Jazz" {
		t.Fatalf("stash: ok=%v %+v", ok, st)
	}

	// The agent answers: SSH is not consulted.
	agent := &fakeAgent{store: stashKeys}
	srv, host, port := agent.serve(t)
	if n := a.stashPresetsBeforeUninstall(host, port, "DEV-B"); n != 2 {
		t.Fatalf("kept = %d, want 2", n)
	}
	if sshReads != 1 {
		t.Fatalf("SSH must not be read when the agent answered, got %d reads", sshReads)
	}

	// Nothing anywhere: no stash, no failure. A fresh App, because the one
	// above has cached the fake agent's port for this host, and the fake is
	// closed so nothing answers.
	srv.Close()
	a = quietApp()
	stashSSHRead = func(string) (string, error) { return "", errors.New("no such file") }
	if n := a.stashPresetsBeforeUninstall("127.0.0.1", deadPort(t), "DEV-C"); n != 0 {
		t.Fatalf("kept = %d, want 0", n)
	}
	if _, ok := loadPresetStash("DEV-C", "127.0.0.1"); ok {
		t.Fatal("an empty read must not write a stash")
	}
}

// The app's memory of a removed speaker is purged, the stash is not: it is
// the record the reinstall needs.
func TestPurgeSpeakerStateKeepsPresetStash(t *testing.T) {
	isolateConfigDir(t)
	if err := savePresetStash("DEV-GONE", "192.0.2.10", stashKeys); err != nil {
		t.Fatal(err)
	}
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.purgeSpeakerState("192.0.2.10", "DEV-GONE")
	if _, ok := loadPresetStash("DEV-GONE", "192.0.2.10"); !ok {
		t.Fatal("purgeSpeakerState must leave the preset stash in place")
	}
}

func TestParseStoreFileAcceptsBothShapes(t *testing.T) {
	wrapped, err := parseStoreFile(`{"presets":[{"slot":1,"name":"A","stream_url":"https://a/s","type":"radio"}]}`)
	if err != nil || len(wrapped) != 1 || wrapped[0].Name != "A" {
		t.Fatalf("wrapper form: %v %+v", err, wrapped)
	}
	bare, err := parseStoreFile(`[{"slot":2,"name":"B","stream_url":"https://b/s","type":"radio"}]`)
	if err != nil || len(bare) != 1 || bare[0].Slot != 2 {
		t.Fatalf("bare form: %v %+v", err, bare)
	}
	if _, err := parseStoreFile("  "); err == nil {
		t.Fatal("an empty file must be an error, not an empty stash")
	}
}

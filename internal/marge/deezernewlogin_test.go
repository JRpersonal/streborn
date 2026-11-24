package marge

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// A new login: the attempt records the firmware's answer, every stand-in
// request in its window, the registration and the source status, and none
// of it carries the login or the account.
func TestDeezerAttemptRecordsWhatTheFirmwareDid(t *testing.T) {
	f := newDeezerFixture(t, "")
	s := f.server(true)
	if s.LastDeezerAttempt() != nil || s.DeezerLoginSnapshot().LastAttempt != nil {
		t.Fatal("an attempt is reported before any was made")
	}
	// Before the attempt: an unrelated request is not recorded.
	serve(s, http.MethodGet, "/streaming/account/stick@local/presets/all", "", "127.0.0.1:4000")

	s.BeginDeezerAttempt("login")
	s.NoteDeezerFirmwareAnswer(http.StatusOK, "", "")
	serve(s, http.MethodGet, "/streaming/account/stick@local/provider_settings", "", "127.0.0.1:4000")
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", deezerAddSourceWithLogin, "127.0.0.1:4000")
	serve(s, http.MethodGet, "/streaming/account/stick@local/sources", "", "127.0.0.1:4000")
	s.NoteDeezerSourceStatus("READY", true)

	a := s.LastDeezerAttempt()
	if a == nil || a.Kind != "login" || a.FirmwareStatus != http.StatusOK {
		t.Fatalf("attempt = %+v", a)
	}
	if !a.Registered || !a.RegisteredCredential {
		t.Fatalf("the registration is not recorded: %+v", a)
	}
	if a.SourceStatus != "READY" || !a.SourceHasAccount || a.SourceCheckedAt.IsZero() {
		t.Fatalf("source status not recorded: %+v", a)
	}
	var paths []string
	named := 0
	for _, h := range a.StandInRequests {
		paths = append(paths, h.Method+" "+h.Path)
		if h.Deezer {
			named++
		}
	}
	want := []string{
		"GET /streaming/account/stick@local/provider_settings",
		"POST /streaming/account/stick@local/source",
		"GET /streaming/account/stick@local/sources",
	}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("recorded requests = %v, want %v", paths, want)
	}
	if named != 1 {
		t.Fatalf("%d requests marked as naming Deezer, want 1 (the registration)", named)
	}
	// The record, the section and the log never carry the login or the account.
	js, _ := json.Marshal(s.DeezerLoginSnapshot())
	for _, leak := range []string{testDeezerSecret, testDeezerAccount} {
		if strings.Contains(string(js), leak) {
			t.Fatalf("debug section carries %q: %s", leak, js)
		}
		if strings.Contains(f.logs.String(), leak) {
			t.Fatalf("log carries %q:\n%s", leak, f.logs.String())
		}
	}
	if !strings.Contains(f.logs.String(), "deezer: stand-in request during the login attempt") {
		t.Fatalf("the window's requests are not logged:\n%s", f.logs.String())
	}
	// The section's copy is a copy: a later request does not change it.
	snap := s.DeezerLoginSnapshot().LastAttempt
	serve(s, http.MethodGet, "/streaming/account/stick@local/full", "", "127.0.0.1:4000")
	if len(snap.StandInRequests) != 3 {
		t.Fatalf("the snapshot changed after the fact: %d", len(snap.StandInRequests))
	}
}

// Outside the window nothing is recorded, and only a request that names
// Deezer gets a log line.
func TestDeezerAttemptWindowCloses(t *testing.T) {
	f := newDeezerFixture(t, "")
	s := f.server(true)
	s.BeginDeezerAttempt("login")
	s.deezerMu.Lock()
	s.deezerAttempt.At = time.Now().Add(-deezerAttemptWindow - time.Second)
	s.deezerMu.Unlock()
	serve(s, http.MethodGet, "/streaming/account/stick@local/sources", "", "127.0.0.1:4000")
	serve(s, http.MethodPost, "/streaming/account/stick@local/source", deezerAddSourceWithLogin, "127.0.0.1:4000")
	a := s.LastDeezerAttempt()
	if len(a.StandInRequests) != 0 || a.Registered {
		t.Fatalf("recorded after the window: %+v", a)
	}
	if !strings.Contains(f.logs.String(), "marge: Deezer request from the box") {
		t.Fatalf("a Deezer registration outside the window is not logged:\n%s", f.logs.String())
	}
}

func TestDeezerAttemptIsBounded(t *testing.T) {
	f := newDeezerFixture(t, "")
	s := f.server(true)
	s.BeginDeezerAttempt("login")
	for i := 0; i < deezerAttemptHitCap+5; i++ {
		serve(s, http.MethodGet, "/streaming/account/stick@local/sources", "", "127.0.0.1:4000")
	}
	a := s.LastDeezerAttempt()
	if len(a.StandInRequests) != deezerAttemptHitCap || a.StandInDropped != 5 {
		t.Fatalf("kept %d, dropped %d", len(a.StandInRequests), a.StandInDropped)
	}
}

// Disconnect forgets the kept login and the carried-over account, so /full
// stops naming Deezer.
func TestClearKeptDeezerForgetsTheLogin(t *testing.T) {
	f := newDeezerFixture(t, deezerSourcesXML)
	CaptureKeptDeezerLogin(f.glob(), f.keptPath, f.logger)
	f.writeReflect(t, `[{"source":"DEEZER","account":"`+testDeezerAccount+`","name":"Deezer"}]`)
	s := f.server(true)
	if s.KeptDeezerAccount() != testDeezerAccount || s.CarriedOverDeezerAccount() != testDeezerAccount {
		t.Fatalf("kept %q, carried over %q", s.KeptDeezerAccount(), s.CarriedOverDeezerAccount())
	}
	if !strings.Contains(fullFrom(t, s, "127.0.0.1:4000"), "<sourcename>DEEZER</sourcename>") {
		t.Fatal("/full does not name Deezer before the removal")
	}
	if err := s.ClearKeptDeezer(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.keptPath); !os.IsNotExist(err) {
		t.Fatalf("the kept login is still on disk: %v", err)
	}
	if s.KeptDeezerAccount() != "" || s.CarriedOverDeezerAccount() != "" {
		t.Fatal("an account is still reported after the removal")
	}
	if strings.Contains(fullFrom(t, s, "127.0.0.1:4000"), "DEEZER") {
		t.Fatal("/full still names Deezer after the removal")
	}
	// Twice is fine.
	if err := s.ClearKeptDeezer(); err != nil {
		t.Fatal(err)
	}
}

// The firmware's availability answer already offers Deezer: a new login needs
// no switch (Deezer is not geo-restricted like Pandora).
func TestDeezerIsAvailableByDefault(t *testing.T) {
	s := newDeezerFixture(t, "").server(false)
	for _, svc := range s.services() {
		if svc.Type == "DEEZER" {
			if !svc.Available {
				t.Fatal("DEEZER is served unavailable")
			}
			return
		}
	}
	t.Fatal("DEEZER is missing from the availability list")
}

// A recreate records its trigger and the notification's answer, and the
// firmware's relogin retry code reaches the debug section with its meaning.
func TestDeezerRecreateAndRetryAreReported(t *testing.T) {
	f := newDeezerFixture(t, "")
	s := f.server(true)
	if s.DeezerLoginSnapshot().LastRetry != nil {
		t.Fatal("a retry is reported without a source")
	}
	seen := time.Date(2026, 11, 22, 1, 46, 51, 0, time.UTC)
	code := ""
	s.SetDeezerRetrySource(func() (string, time.Time, bool) { return code, seen, code != "" })
	if s.DeezerLoginSnapshot().LastRetry != nil {
		t.Fatal("a retry is reported before the firmware logged one")
	}
	code = "3330"
	r := s.DeezerLoginSnapshot().LastRetry
	if r == nil || r.Code != "3330" || !strings.Contains(r.Meaning, "rejected") || !r.At.Equal(seen) {
		t.Fatalf("retry = %+v", r)
	}
	s.NoteDeezerRecreate("start", 0, "")
	if s.LastDeezerAttempt() != nil {
		t.Fatal("a recreate note without an attempt made one up")
	}
	s.BeginDeezerAttempt("recreate")
	s.NoteDeezerFirmwareAnswer(http.StatusOK, "", "")
	s.NoteDeezerRecreate("check-again", http.StatusOK, "")
	a := s.LastDeezerAttempt()
	if a.Kind != "recreate" || a.Trigger != "check-again" || a.NotifyStatus != http.StatusOK {
		t.Fatalf("attempt = %+v", a)
	}
	if DeezerRetryMeaning("3351") == DeezerRetryMeaning("9") {
		t.Fatal("3351 has no meaning of its own")
	}
}

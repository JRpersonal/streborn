package marge

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The firmware drops its hardware-key registrations every time it reads its
// account document, so the agent re-checks the keys after the last read of a
// boot. The hook must fire for that read and for nothing else the same
// responder also answers.
func TestAccountFetchHookFiresOnlyForTheAccountRead(t *testing.T) {
	calls := 0
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("0000000001A1"),
		WithAccountFetchHook(func() { calls++ }))
	s.SetAccount(&AccountInfo{AccountEmail: "stick@local"})

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/streaming/account/stick@local/full", 1},
		{http.MethodGet, "/streaming/account/stick@local/full/", 1},
		{http.MethodPost, "/streaming/account/stick@local/full", 0},
		{http.MethodGet, "/streaming/account/stick@local", 0},
		{http.MethodGet, "/streaming/auth/whatever", 0},
		{http.MethodGet, "/streaming/account/stick@local/sources", 0},
	}
	for _, c := range cases {
		calls = 0
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(c.method, c.path, strings.NewReader("")))
		if calls != c.want {
			t.Errorf("%s %s: hook called %d times, want %d", c.method, c.path, calls, c.want)
		}
	}
}

// Without a hook the account read answers as it always did.
func TestAccountReadWithoutHook(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("0000000001A1"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/streaming/account/stick@local/full", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<account>") {
		t.Fatalf("account read answered %d: %s", w.Code, w.Body.String())
	}
}

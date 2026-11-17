package main

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// infoApp builds an App whose /info call answers with the given body (any host,
// any port - the custom transport ignores the URL), so the margeAccountUUID
// probes can be exercised without a live speaker. A nil body makes every request
// fail, standing in for a box that cannot be reached.
func infoApp(body string, fail bool) *App {
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if fail {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{},
		}, nil
	})}
	return a
}

func TestMargeUUIDFromInfo(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantVal   string
		wantFound bool
	}{
		{"populated", `<info><margeAccountUUID>abc-123</margeAccountUUID></info>`, "abc-123", true},
		{"empty", `<info><margeAccountUUID></margeAccountUUID></info>`, "", true},
		{"missing", `<info><type>Wave SoundTouch</type></info>`, "", false},
		{"whitespace trimmed", `<info><margeAccountUUID>  u  </margeAccountUUID></info>`, "u", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, found := margeUUIDFromInfo(c.body)
			if v != c.wantVal || found != c.wantFound {
				t.Fatalf("margeUUIDFromInfo(%q) = (%q,%v), want (%q,%v)", c.body, v, found, c.wantVal, c.wantFound)
			}
		})
	}
}

// boxMargeAccountEmpty must be POSITIVE: only a box that answered /info AND
// reports an empty margeAccountUUID counts as "no account". A box with an
// account, a box whose /info lacks the tag, and a box we could not reach all
// read false, so the account-replacing bootstrap never runs on a box we have not
// confirmed is empty.
func TestBoxMargeAccountEmpty(t *testing.T) {
	cases := []struct {
		name string
		body string
		fail bool
		want bool
	}{
		{"fresh box, empty account", `<info><margeAccountUUID></margeAccountUUID></info>`, false, true},
		{"used box, has account", `<info><margeAccountUUID>bose-uuid</margeAccountUUID></info>`, false, false},
		{"tag missing", `<info><type>Wave SoundTouch</type></info>`, false, false},
		{"info unreachable", "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := infoApp(c.body, c.fail).boxMargeAccountEmpty("192.0.2.10"); got != c.want {
				t.Fatalf("boxMargeAccountEmpty = %v, want %v", got, c.want)
			}
		})
	}
}

// shouldTryBootstrapUnlock is the whole decision: the uninstall always falls
// through, the OTA never does, and the install falls through only on a box
// positively confirmed to carry no account.
func TestShouldTryBootstrapUnlock(t *testing.T) {
	const freshBox = `<info><margeAccountUUID></margeAccountUUID></info>`
	const usedBox = `<info><margeAccountUUID>bose-uuid</margeAccountUUID></info>`

	cases := []struct {
		name string
		opts sshUnlockOptions
		body string
		fail bool
		want bool
	}{
		// Install path (bootstrapIfNoAccount): account-driven.
		{"install, account empty -> bootstrap", sshUnlockOptions{bootstrapIfNoAccount: true}, freshBox, false, true},
		{"install, account present -> no bootstrap", sshUnlockOptions{bootstrapIfNoAccount: true}, usedBox, false, false},
		{"install, box unreachable -> no bootstrap", sshUnlockOptions{bootstrapIfNoAccount: true}, "", true, false},
		// Uninstall path (bootstrap): unconditional, no /info read needed.
		{"uninstall always bootstraps, even with an account", sshUnlockOptions{bootstrap: true}, usedBox, false, true},
		// OTA / plain path: neither option -> never.
		{"no options -> never bootstrap, even on a fresh box", sshUnlockOptions{}, freshBox, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := infoApp(c.body, c.fail).shouldTryBootstrapUnlock("192.0.2.10", c.opts); got != c.want {
				t.Fatalf("shouldTryBootstrapUnlock = %v, want %v", got, c.want)
			}
		})
	}
}

// margeUUIDState is log-safe: a populated UUID is hashed (never printed raw),
// and the empty / unreachable cases are named distinctly so a transcript tells
// "seed did not take" from "could not ask".
func TestMargeUUIDStateIsLogSafe(t *testing.T) {
	const raw = "secret-account-uuid"
	present := infoApp(`<info><margeAccountUUID>`+raw+`</margeAccountUUID></info>`, false).margeUUIDState("192.0.2.10")
	if strings.Contains(present, raw) {
		t.Fatalf("margeUUIDState leaked the raw account id: %q", present)
	}
	if !strings.Contains(present, "MARGE#") {
		t.Fatalf("a populated account should be reported hashed, got %q", present)
	}

	empty := infoApp(`<info><margeAccountUUID></margeAccountUUID></info>`, false).margeUUIDState("192.0.2.10")
	if !strings.Contains(empty, "empty") {
		t.Fatalf("an empty account should say so, got %q", empty)
	}

	unreachable := infoApp("", true).margeUUIDState("192.0.2.10")
	if !strings.Contains(unreachable, "unknown") {
		t.Fatalf("an unreachable box should read as unknown, got %q", unreachable)
	}
}

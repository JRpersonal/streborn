package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// Which Spotify account a key belongs to. A real save of what plays right now
// takes the live account; every other write (rename, move, restore, copy)
// keeps the account the key already has, even while that key plays under a
// different account (ST30, 2026-10-09: a recall whose account switch was
// overridden played the public playlist under the other account, and the next
// write moved the key to it for good).

const accountTestURI = "spotify:playlist:37i9dQZF1DX0AMssoUKCz7"

func newAccountStampServer(t *testing.T, liveCtx, liveUser string) *Server {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	return &Server{
		presets:        store,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifyContext: func() string { return liveCtx },
		spotifyUser:    func(context.Context) string { return liveUser },
	}
}

func putPresetQuery(t *testing.T, s *Server, slot int, query, body string) {
	t.Helper()
	r := httptest.NewRequest("PUT", "/api/presets/"+strconv.Itoa(slot)+query, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handlePresetSlot(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
}

func TestPresetLiveSaveTakesThePlayingAccount(t *testing.T) {
	s := newAccountStampServer(t, accountTestURI, "second-user")
	putPresetQuery(t, s, 3, "?save=live",
		`{"name":"Tropical House","type":"spotify","uri":"`+accountTestURI+`","account":"first-user"}`)
	if p, _ := s.presets.Get(3); p.Account != "second-user" {
		t.Fatalf("account = %q, want the live account second-user", p.Account)
	}
}

func TestPresetRewriteWhilePlayingKeepsItsAccount(t *testing.T) {
	s := newAccountStampServer(t, accountTestURI, "first-user")
	putPresetQuery(t, s, 3, "",
		`{"name":"Tropical House renamed","type":"spotify","uri":"`+accountTestURI+`","account":"second-user"}`)
	if p, _ := s.presets.Get(3); p.Account != "second-user" {
		t.Fatalf("account = %q, want the key's own second-user kept", p.Account)
	}
}

func TestPresetWithoutAccountIsFilledFromTheLiveLogin(t *testing.T) {
	s := newAccountStampServer(t, "spotify:playlist:SOMETHING_ELSE", "first-user")
	putPresetQuery(t, s, 3, "", `{"name":"Tropical House","type":"spotify","uri":"`+accountTestURI+`"}`)
	if p, _ := s.presets.Get(3); p.Account != "first-user" {
		t.Fatalf("account = %q, want an empty account filled with first-user", p.Account)
	}
}

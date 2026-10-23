package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/sticksetup"
)

// fakeLangBox is the /setup + /language pair the gate talks to.
type fakeLangBox struct {
	setup   string // /setup state attribute
	sys     string // /setup systemstate attribute
	readErr error
	postErr error
	ignore  bool // accept the POST but keep the gate open
	posts   []int
	reads   int
}

func newTestLangGate(box *fakeLangBox, lang int, from string) (*boxLanguageGate, *bytes.Buffer, *int) {
	var buf bytes.Buffer
	waits := 0
	g := &boxLanguageGate{
		logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		setupState: func(context.Context) (string, string, error) {
			box.reads++
			if box.readErr != nil {
				return "", "", box.readErr
			}
			return box.setup, box.sys, nil
		},
		setLanguage: func(_ context.Context, n int) error {
			if box.postErr != nil {
				return box.postErr
			}
			box.posts = append(box.posts, n)
			if !box.ignore && n != 0 {
				box.sys = "SETUP_LANG_SET"
			}
			return nil
		},
		resolve: func() (int, string) { return lang, from },
		wait: func(context.Context, time.Duration) bool {
			waits++
			return true
		},
	}
	return g, &buf, &waits
}

// TestBoxLanguageGateClosesTheGate is the 2026-10-22 SoundTouch 20 case:
// systemstate SETUP_LANG_NOT_SET, sysLanguage 0, region DE. One POST of the
// resolved language closes the gate, and the gate stops for this boot.
func TestBoxLanguageGateClosesTheGate(t *testing.T) {
	box := &fakeLangBox{setup: "SETUP_INACTIVE", sys: "SETUP_LANG_NOT_SET"}
	g, log, waits := newTestLangGate(box, 2, "country DE")
	if !g.run(context.Background()) {
		t.Fatalf("gate must report closed\n%s", log)
	}
	if len(box.posts) != 1 || box.posts[0] != 2 {
		t.Fatalf("posts = %v, want exactly [2]", box.posts)
	}
	if *waits != 1 {
		t.Fatalf("a gate closed on the first check must not look again, waited %d times", *waits)
	}
	if !strings.Contains(log.String(), "sysLanguage=2") || !strings.Contains(log.String(), `from="country DE"`) {
		t.Fatalf("the log must say what was set and why:\n%s", log)
	}
}

// TestBoxLanguageGateLeavesASetLanguageAlone: a box past the gate keeps the
// language its owner chose; nothing is written.
func TestBoxLanguageGateLeavesASetLanguageAlone(t *testing.T) {
	box := &fakeLangBox{setup: "SETUP_INACTIVE", sys: "SETUP_LANG_SET"}
	g, _, _ := newTestLangGate(box, 3, "default")
	if !g.run(context.Background()) {
		t.Fatal("a closed gate must report closed")
	}
	if len(box.posts) != 0 {
		t.Fatalf("posts = %v, want none", box.posts)
	}
	if box.reads != 1 {
		t.Fatalf("one /setup read is all a closed gate costs, got %d", box.reads)
	}
}

// TestBoxLanguageGateIsBounded: a box that never takes the language, or never
// answers, gets a fixed number of looks per boot and then is left alone. No
// polling loop on a config endpoint.
func TestBoxLanguageGateIsBounded(t *testing.T) {
	maxChecks := 1 + len(boxLanguageRetryDelays)

	box := &fakeLangBox{setup: "SETUP_INACTIVE", sys: "SETUP_LANG_NOT_SET", ignore: true}
	g, log, waits := newTestLangGate(box, 3, "default")
	if g.run(context.Background()) {
		t.Fatal("a gate that stays open must not report closed")
	}
	if len(box.posts) != maxChecks || *waits != maxChecks {
		t.Fatalf("posts=%d waits=%d, want %d each", len(box.posts), *waits, maxChecks)
	}
	if !strings.Contains(log.String(), "leaving it until the next boot") {
		t.Fatalf("giving up must be said once:\n%s", log)
	}

	box = &fakeLangBox{readErr: errors.New("connection refused")}
	g, _, _ = newTestLangGate(box, 3, "default")
	g.run(context.Background())
	if box.reads != maxChecks || len(box.posts) != 0 {
		t.Fatalf("unreadable box: reads=%d posts=%d, want %d reads and no posts", box.reads, len(box.posts), maxChecks)
	}

	box = &fakeLangBox{sys: "SETUP_LANG_NOT_SET", postErr: errors.New("500")}
	g, _, _ = newTestLangGate(box, 3, "default")
	g.run(context.Background())
	if box.reads != maxChecks {
		t.Fatalf("failed POSTs: reads=%d, want %d", box.reads, maxChecks)
	}
}

// TestBoxLanguageGateStaysOutOfProvisioning: in the setup access point the
// provisioning owns the gate and sets the wizard's language itself.
func TestBoxLanguageGateStaysOutOfProvisioning(t *testing.T) {
	box := &fakeLangBox{setup: "SETUP_AP_OOB", sys: "SETUP_LANG_NOT_SET"}
	g, _, _ := newTestLangGate(box, 3, "default")
	g.run(context.Background())
	if len(box.posts) != 0 {
		t.Fatalf("posts = %v during provisioning, want none", box.posts)
	}
}

// TestBoxLanguageGateNeverSendsZero: 0 is the factory "unset" value and a
// no-op for the gate; an invalid resolution falls back to English.
func TestBoxLanguageGateNeverSendsZero(t *testing.T) {
	for _, bad := range []int{0, 14, 26, -1} {
		box := &fakeLangBox{setup: "SETUP_INACTIVE", sys: "SETUP_LANG_NOT_SET"}
		g, _, _ := newTestLangGate(box, bad, "broken")
		g.run(context.Background())
		if len(box.posts) != 1 || box.posts[0] != sysLanguageEnglish {
			t.Fatalf("resolved %d: posts = %v, want [%d]", bad, box.posts, sysLanguageEnglish)
		}
	}
}

// TestBoxLanguageGateStopsOnContextEnd: agent shutdown ends the wait.
func TestBoxLanguageGateStopsOnContextEnd(t *testing.T) {
	box := &fakeLangBox{sys: "SETUP_LANG_NOT_SET"}
	g, _, _ := newTestLangGate(box, 3, "default")
	g.wait = func(context.Context, time.Duration) bool { return false }
	if g.run(context.Background()) || box.reads != 0 {
		t.Fatalf("a cancelled wait must end the gate without touching the box (reads=%d)", box.reads)
	}
}

func TestResolveBoxLanguageOrder(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "lang.conf")
	txt := filepath.Join(dir, "lang.txt")
	de := func() string { return "de" }

	if n, from := resolveBoxLanguage(conf, txt, func() string { return "" }); n != 3 || from != "default" {
		t.Fatalf("nothing known: got %d (%s), want English 3", n, from)
	}
	if n, from := resolveBoxLanguage(conf, txt, func() string { return "XX" }); n != 3 || from != "default" {
		t.Fatalf("unknown country: got %d (%s), want English 3", n, from)
	}
	if n, from := resolveBoxLanguage(conf, txt, de); n != 2 || from != "country DE" {
		t.Fatalf("country DE: got %d (%s), want 2", n, from)
	}
	if err := os.WriteFile(txt, []byte("5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, from := resolveBoxLanguage(conf, txt, de); n != 5 || from != "lang.txt" {
		t.Fatalf("lang.txt beats the country: got %d (%s)", n, from)
	}
	if err := os.WriteFile(conf, []byte(`{"locale":"nl","country":"DE","sysLanguage":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, from := resolveBoxLanguage(conf, txt, de); n != 7 || from != "lang.conf" {
		t.Fatalf("the wizard's choice beats everything: got %d (%s)", n, from)
	}
	// An unusable stored value falls through, it is never sent.
	if err := os.WriteFile(conf, []byte(`{"sysLanguage":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(txt, []byte("14"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, _ := resolveBoxLanguage(conf, txt, de); n != 2 {
		t.Fatalf("invalid stored values must fall through to the country, got %d", n)
	}
}

// TestSysLanguageForCountryParity keeps the agent's copy of the country table
// identical to sticksetup's (which in turn is pinned to run.sh's
// lang_int_for_cc by sticksetup/langparity_test.go).
func TestSysLanguageForCountryParity(t *testing.T) {
	for a := 'A'; a <= 'Z'; a++ {
		for b := 'A'; b <= 'Z'; b++ {
			cc := string([]rune{a, b})
			if got, want := sysLanguageForCountry(cc), sticksetup.SysLanguageForCountry(cc); got != want {
				t.Errorf("%s: agent %d, sticksetup %d", cc, got, want)
			}
		}
	}
	if sysLanguageForCountry(" de ") != 2 {
		t.Error("the agent table must normalise case and space like sticksetup")
	}
}

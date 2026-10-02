package spotify

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func namesManager(t *testing.T) *Manager {
	t.Helper()
	return &Manager{
		credStore: t.TempDir(),
		logger:    slog.New(slog.NewTextHandler(os.NewFile(0, os.DevNull), nil)),
	}
}

// The point of remembering: once an account has been seen, its name survives
// everything that would make Spotify unreachable later.
func TestANameSurvivesARestart(t *testing.T) {
	m := namesManager(t)
	m.rememberAccountName("qm4xvp7z2bnkd6rt1ys8hgwj3", "Eileen")

	// A second manager over the same store is what a reboot looks like.
	m2 := &Manager{credStore: m.credStore, logger: m.logger}
	if got := m2.AccountNames()["qm4xvp7z2bnkd6rt1ys8hgwj3"]; got != "Eileen" {
		t.Fatalf("the name did not survive: %q", got)
	}
}

// The speaker's flash is the part that wears out, so a name already known must
// not be rewritten every time Spotify answers.
func TestKnownNameIsNotWrittenAgain(t *testing.T) {
	m := namesManager(t)
	path := filepath.Join(m.credStore, accountNamesFile)

	m.rememberAccountName("qm4xvp7z2bnkd6rt1ys8hgwj3", "Eileen")
	first, err := os.Stat(path)
	if err != nil {
		t.Fatalf("nothing was written at all: %v", err)
	}
	// Mark the file so a rewrite is detectable without depending on timestamps,
	// which on a fast machine are identical either way.
	if err := os.WriteFile(path, []byte(`{"qm4xvp7z2bnkd6rt1ys8hgwj3":"Eileen"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marked, _ := os.Stat(path)

	m.rememberAccountName("qm4xvp7z2bnkd6rt1ys8hgwj3", "Eileen")
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != marked.Size() {
		t.Errorf("the same name was written again (%d -> %d bytes, first write %d)",
			marked.Size(), after.Size(), first.Size())
	}
}

// A name that CHANGED is news and must land, or somebody who renames their
// Spotify profile is stuck with the old one for ever.
func TestAChangedNameIsWritten(t *testing.T) {
	m := namesManager(t)
	m.rememberAccountName("qm4xvp7z2bnkd6rt1ys8hgwj3", "Eileen")
	m.rememberAccountName("qm4xvp7z2bnkd6rt1ys8hgwj3", "Eileen W")
	if got := m.AccountNames()["qm4xvp7z2bnkd6rt1ys8hgwj3"]; got != "Eileen W" {
		t.Fatalf("the new name did not land: %q", got)
	}
}

// Several household accounts each keep their own name, because telling them
// apart is the reason this line exists on a preset tile at all.
func TestTwoAccountsKeepTheirOwnNames(t *testing.T) {
	m := namesManager(t)
	m.rememberAccountName("qm4xvp7z2bnkd6rt1ys8hgwj3", "Eileen")
	m.rememberAccountName("48qpzlmxtreb9vkd2yhsn6wc3gfu", "Sam")
	names := m.AccountNames()
	if names["qm4xvp7z2bnkd6rt1ys8hgwj3"] != "Eileen" || names["48qpzlmxtreb9vkd2yhsn6wc3gfu"] != "Sam" {
		t.Fatalf("one account overwrote the other: %v", names)
	}
}

// Nothing a name is not gets stored. The value arrives from the network and
// ends up on a tile and in a JSON payload.
func TestRubbishIsNotStored(t *testing.T) {
	m := namesManager(t)
	m.rememberAccountName("", "Eileen")
	m.rememberAccountName("qm4xvp7z2bnkd6rt1ys8hgwj3", "")
	m.rememberAccountName("qm4xvp7z2bnkd6rt1ys8hgwj3", "   ")
	if n := len(m.AccountNames()); n != 0 {
		t.Fatalf("stored %d names it should have refused: %v", n, m.AccountNames())
	}

	m.rememberAccountName("a", "Eileen\nmsg=\"injected\"")
	if got := m.AccountNames()["a"]; got != "Eileen" {
		t.Errorf("a newline was kept in a label: %q", got)
	}

	m.rememberAccountName("b", strings.Repeat("x", 500))
	if got := len([]rune(m.AccountNames()["b"])); got != maxAccountNameLen {
		t.Errorf("an unbounded name was stored: %d runes", got)
	}
}

// A half-written store must cost the names, not the boot.
func TestABrokenStoreReadsAsEmpty(t *testing.T) {
	m := namesManager(t)
	if err := os.WriteFile(filepath.Join(m.credStore, accountNamesFile), []byte(`{"a":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := len(m.AccountNames()); n != 0 {
		t.Fatalf("a broken file produced %d names", n)
	}
}

// The store is a label store. A credential must never end up in it, so the file
// it writes is checked for shape rather than trusted.
func TestTheStoreHoldsNothingButNames(t *testing.T) {
	m := namesManager(t)
	m.rememberAccountName("qm4xvp7z2bnkd6rt1ys8hgwj3", "Eileen")
	data, err := os.ReadFile(filepath.Join(m.credStore, accountNamesFile))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("the store is not a flat id-to-name map: %v", err)
	}
	if len(got) != 1 || got["qm4xvp7z2bnkd6rt1ys8hgwj3"] != "Eileen" {
		t.Fatalf("unexpected contents: %v", got)
	}
}

package main

import (
	"io"
	"log/slog"
	"testing"
)

// The three exits of UpdateBoxAgent that return nil without knowing the upload
// landed write one journal line each. The failure report and the journal
// reader key on the UNCONFIRMED word and on the closing clause, and the lines
// are what users paste into issues, so they are pinned character for character.
func TestUnconfirmedOutcomeLinesAreUnchanged(t *testing.T) {
	lines := []struct{ what, want string }{
		{"box answered neither HTTP nor SSH; nothing was uploaded, deferred to the version poll",
			"UNCONFIRMED: box answered neither HTTP nor SSH; nothing was uploaded, deferred to the version poll — this line is NOT a success"},
		{"full binary delivered, HTTP reply lost to the reboot; deferred to the version poll",
			"UNCONFIRMED: full binary delivered, HTTP reply lost to the reboot; deferred to the version poll — this line is NOT a success"},
		{"HTTP dropped mid-upload (the agent may be applying it) and SSH raced the reboot; deferred to the version poll",
			"UNCONFIRMED: HTTP dropped mid-upload (the agent may be applying it) and SSH raced the reboot; deferred to the version poll — this line is NOT a success"},
	}
	for _, l := range lines {
		p := &agentPush{a: &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, host: "192.0.2.1"}
		if err := p.unconfirmed(l.what, "test"); err != nil {
			t.Errorf("unconfirmed returned %v, want nil: the version poll decides, not this call", err)
		}
		if p.outcomeNote != l.want {
			t.Errorf("outcome note\n got %q\nwant %q", p.outcomeNote, l.want)
		}
	}
}

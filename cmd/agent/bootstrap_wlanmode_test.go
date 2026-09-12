package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The bootstrap reboot is stacked on the OTA's own reboot, and on a chassis
// whose Wi-Fi is programmed into the BCO coprocessor a soft reboot can leave
// the chip unassociated with no way back except pulling the plug. So the gate
// has to answer "coprocessor" for exactly the two modes run.sh writes for such
// a box, and "" for everything else INCLUDING the cases it cannot classify:
// a chassis we do not recognise must keep the old behaviour rather than
// silently stop refreshing its boot path.
func TestCoprocessorWLANMode(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"bco", "bco\n", "bco"},
		{"taigan", "taigan-bco\n", "taigan-bco"},
		{"no trailing newline", "bco", "bco"},
		{"surrounding whitespace", "  taigan-bco  \n", "taigan-bco"},
		{"wlan0 is kernel driven", "wlan0\n", ""},
		{"wlan1 is kernel driven", "wlan1\n", ""},
		{"ethernet only", "ethernet-only\n", ""},
		{"unknown value", "something-new\n", ""},
		{"empty file", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "wlan-mode")
			if err := os.WriteFile(p, []byte(c.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := coprocessorWLANModeAt(p); got != c.want {
				t.Errorf("content %q: got %q, want %q", c.content, got, c.want)
			}
		})
	}
}

// A missing file is the developer machine, a box installed before run.sh wrote
// this file, and any read error. All of them must read as "not classified", not
// as "coprocessor": the gate may only ever suppress the reboot on a box we have
// positively identified.
func TestCoprocessorWLANModeMissingFileIsNotCoprocessor(t *testing.T) {
	p := filepath.Join(t.TempDir(), "does-not-exist")
	if got := coprocessorWLANModeAt(p); got != "" {
		t.Errorf("missing file: got %q, want empty", got)
	}
}

package webhooks

import (
	"path/filepath"
	"testing"
)

func TestValidWOLMAC(t *testing.T) {
	for _, mac := range []string{"AA:BB:CC:DD:EE:FF", "aa-bb-cc-dd-ee-ff", " AA:BB:CC:DD:EE:FF ", "aabb.ccdd.eeff"} {
		if !ValidWOLMAC(mac) {
			t.Errorf("%q must be valid", mac)
		}
	}
	// Five octets (the field case), an 8-byte EUI-64, junk and empty.
	for _, mac := range []string{"AA:BB:CC:DD:EE", "AA:BB:CC:DD:EE:FF:00:11", "not a mac", ""} {
		if ValidWOLMAC(mac) {
			t.Errorf("%q must be invalid", mac)
		}
	}
}

// A Wake-on-LAN action with an address that cannot be parsed never fires. It
// must not count as configured (it lit the key and triggered the group-key
// warning), and an enabled one must be refused on save, while switching it
// off must always be possible.
func TestWOLWithAnInvalidMAC(t *testing.T) {
	bad := Action{Enabled: true, Type: "wol", MAC: "AA:BB:CC:DD:EE"}
	if bad.Configured() {
		t.Fatal("an unparsable MAC counts as configured")
	}
	s, err := Load(filepath.Join(t.TempDir(), "webhooks.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(Config{Thumb: bad}); err == nil {
		t.Fatal("an enabled WOL action with a bad MAC was saved")
	}
	if err := s.Set(Config{Buttons: map[string]Trigger{"thumbsDown": {Action: bad}}}); err == nil {
		t.Fatal("an enabled per-key WOL action with a bad MAC was saved")
	}
	off := bad
	off.Enabled = false
	if err := s.Set(Config{Thumb: off}); err != nil {
		t.Fatalf("switching a broken entry off was refused: %v", err)
	}
	good := Action{Enabled: true, Type: "wol", MAC: "AA:BB:CC:DD:EE:FF"}
	if err := s.Set(Config{Thumb: good}); err != nil || !good.Configured() {
		t.Fatalf("a valid WOL action was refused: %v", err)
	}
}

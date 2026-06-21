package boxapi

import (
	"encoding/xml"
	"testing"
)

// The Bose /info XML declares UTF-8 but can carry a Latin-1 umlaut byte. ensureUTF8
// must repair that so xml.Unmarshal does not reject the whole document (which would
// lose the deviceID + name and defeat zone master-id resolution).
func TestEnsureUTF8(t *testing.T) {
	// "Küche" with a lone Latin-1 0xFC for "ü".
	latin1 := []byte("K\xfcche")
	got := string(ensureUTF8(latin1))
	if got != "Küche" {
		t.Errorf("ensureUTF8(latin1) = %q, want %q", got, "Küche")
	}
	// Already valid UTF-8 must pass through byte-for-byte.
	utf8In := []byte("Küche")
	if got := ensureUTF8(utf8In); string(got) != "Küche" {
		t.Errorf("ensureUTF8(utf8) = %q, want %q", string(got), "Küche")
	}
	// Plain ASCII is untouched.
	if got := string(ensureUTF8([]byte("Kitchen"))); got != "Kitchen" {
		t.Errorf("ensureUTF8(ascii) = %q, want Kitchen", got)
	}
}

// A Latin-1 /info body must parse after the ensureUTF8 repair, recovering both the
// deviceID and the umlaut name instead of erroring on invalid UTF-8.
func TestGetInfoBodyLatin1Parses(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8" ?><info deviceID="ABCDEF012345"><name>K` + "\xfc" + `che</name><type>SoundTouch 20</type></info>`)
	var raw struct {
		DeviceID string `xml:"deviceID,attr"`
		Name     string `xml:"name"`
	}
	if err := xml.Unmarshal(ensureUTF8(body), &raw); err != nil {
		t.Fatalf("unmarshal repaired body: %v", err)
	}
	if raw.DeviceID != "ABCDEF012345" {
		t.Errorf("deviceID = %q, want ABCDEF012345", raw.DeviceID)
	}
	if raw.Name != "Küche" {
		t.Errorf("name = %q, want Küche", raw.Name)
	}
	// Without the repair the raw Latin-1 body must fail, proving the fix matters.
	if err := xml.Unmarshal(body, &raw); err == nil {
		t.Errorf("raw Latin-1 body unexpectedly parsed; repair would be a no-op")
	}
}

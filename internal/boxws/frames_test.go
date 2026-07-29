package boxws

import (
	"encoding/xml"
	"testing"
)

// A bass change on the speaker or its remote must be recognised: it is a person
// at the speaker, and until 2026-07-29 each one was logged as an unrecognized
// frame, seven in 400 ms, which buried the signal it actually carried.
func TestBassUpdatedIsRecognised(t *testing.T) {
	var f gabboFrame
	raw := []byte(`<updates deviceID='DEV#03b2d6b9'><bassUpdated></bassUpdated></updates>`)
	if err := xml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.BassUpdated == nil {
		t.Fatal("a bassUpdated frame must parse into BassUpdated, or it falls through as unknown")
	}
	if f.VolumeUpdated != nil {
		t.Error("bass must not be mistaken for a volume change")
	}
}

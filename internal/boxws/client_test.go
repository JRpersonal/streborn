package boxws

import (
	"io"
	"log/slog"
	"testing"
)

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A box error that follows a preset selection must be recorded WITH what the
// box was acting on. Issue #600: a hardware press activated a native radio
// preset and the speaker answered 4502 BMX_JSON_PARSE_ERROR 700 ms later, and
// the bundle could not connect the two.
func TestBoxErrorIsRecordedAgainstTheSelectionItFollowed(t *testing.T) {
	c := &Client{logger: quietTestLogger()}

	c.NoteSelection("/station?data=eyJuYW1lIjoiUmFkaW8gTm9vcmQifQ")
	c.noteBoxError("4502", "BMX_JSON_PARSE_ERROR", "Json parse error: Line 1, Column 12")

	got := c.BoxErrors()
	if len(got) != 1 {
		t.Fatalf("want one recorded error, got %d", len(got))
	}
	if got[0].Value != "4502" || got[0].Name != "BMX_JSON_PARSE_ERROR" {
		t.Errorf("error mis-recorded: %+v", got[0])
	}
	if got[0].ActingOn == "" {
		t.Error("the error must carry the location the box was acting on")
	}
	if got[0].SinceSelectionMs < 0 {
		t.Errorf("SinceSelectionMs = %d, want the gap to the selection", got[0].SinceSelectionMs)
	}
}

// An error with no preset press before it must not invent an attribution.
func TestBoxErrorWithoutASelectionCarriesNoAttribution(t *testing.T) {
	c := &Client{logger: quietTestLogger()}

	c.noteBoxError("3101", "AUDIO_ERROR_BAD_URL", "")

	got := c.BoxErrors()
	if len(got) != 1 || got[0].ActingOn != "" {
		t.Errorf("unattributed error should stay unattributed: %+v", got)
	}
}

// The ring is bounded so a storming speaker cannot grow the debug payload.
func TestBoxErrorRingIsBounded(t *testing.T) {
	c := &Client{logger: quietTestLogger()}
	for i := 0; i < maxBoxErrors*3; i++ {
		c.noteBoxError("1036", "UNABLE_TO_PROCESS_NOT_LOGGED_IN", "")
	}
	if n := len(c.BoxErrors()); n != maxBoxErrors {
		t.Errorf("ring holds %d entries, want it capped at %d", n, maxBoxErrors)
	}
}

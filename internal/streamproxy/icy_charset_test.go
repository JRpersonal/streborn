package streamproxy

import (
	"testing"
	"unicode/utf8"
)

// A Shoutcast/Icecast StreamTitle with a Latin-1 umlaut byte (ü = 0xFC) must be
// decoded to valid UTF-8, otherwise json.Marshal to the app replaces the byte
// with U+FFFD and the song shows up garbled.
func TestTitleToUTF8DecodesLatin1Umlauts(t *testing.T) {
	latin1 := string([]byte{'F', 0xFC, 'r', 's', 't', 'e', 'n', 'f', 'e', 'l', 'd'})
	if utf8.ValidString(latin1) {
		t.Fatal("test input should be invalid UTF-8 Latin-1 bytes")
	}
	got := titleToUTF8(latin1)
	if got != "Fürstenfeld" {
		t.Fatalf("latin1 title not decoded: got %q want %q", got, "Fürstenfeld")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
}

// A title that is already valid UTF-8 (a modern station) must pass through byte
// for byte, so a correct umlaut is never double-encoded.
func TestTitleToUTF8LeavesValidUTF8Unchanged(t *testing.T) {
	in := "Über den Wolken (Reinhard Mey)"
	if got := titleToUTF8(in); got != in {
		t.Fatalf("valid UTF-8 title was altered: got %q want %q", got, in)
	}
}

package streamproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
)

// FuzzStreamTitleJSON feeds arbitrary bytes as an ICY StreamTitle through the
// title-building path (metadata block -> parseStreamTitle -> setTitle ->
// /api/stream/title) and asserts the JSON the endpoint emits is always valid,
// contains no raw control character, and round-trips to the stored title. The
// pre-fix writeJSONString escaped only \ " \n \r \t and leaked the remaining
// U+0000..U+001F bytes raw into the response, so a garbled or hostile station
// could blank the live title in the app.
func FuzzStreamTitleJSON(f *testing.F) {
	f.Add([]byte("a\x01b"))
	f.Add([]byte(""))
	f.Add([]byte("Artist - Song"))
	f.Add([]byte(`He said "hi" - Live`))
	f.Add([]byte("back\\slash\nand\tcontrols\r"))
	f.Add([]byte("F\xfcrstenfeld")) // Latin-1 high byte, invalid UTF-8
	f.Add([]byte("\x00\x1f\x7f"))

	f.Fuzz(func(t *testing.T, data []byte) {
		// The raw endpoint writer must produce valid JSON for ANY value, even
		// one that never went through setTitle's normalisation.
		var direct bytes.Buffer
		writeJSONString(&direct, "title", string(data))
		assertCleanTitleJSON(t, direct.Bytes())

		// Full path: the bytes arrive inside an ICY metadata block, are parsed,
		// stored, and served by the /api/stream/title handler.
		s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		meta := "StreamTitle='" + string(data) + "';"
		title, ok := parseStreamTitle(meta)
		if !ok {
			t.Fatalf("parseStreamTitle(%q) found no StreamTitle field", meta)
		}
		s.noteStreamStart("http://192.0.2.1/stream")
		s.setTitle("http://192.0.2.1/stream", title)

		rec := httptest.NewRecorder()
		s.handleTitle(rec, httptest.NewRequest("GET", "/api/stream/title", nil))
		body := rec.Body.Bytes()
		got := assertCleanTitleJSON(t, body)

		// setTitle stores valid UTF-8 only (titleToUTF8), so the endpoint must
		// round-trip the stored title exactly.
		if want := s.CurrentTitle(); got != want {
			t.Fatalf("round-trip title = %q, want stored %q (body %q)", got, want, body)
		}
	})
}

// assertCleanTitleJSON checks body is valid JSON of shape {"title":...} with
// no raw control character anywhere, and returns the decoded title.
func assertCleanTitleJSON(t *testing.T, body []byte) string {
	t.Helper()
	for i, b := range body {
		if b < 0x20 {
			t.Fatalf("raw control byte 0x%02x at offset %d in %q", b, i, body)
		}
	}
	if !json.Valid(body) {
		t.Fatalf("invalid JSON: %q", body)
	}
	var out struct {
		Title *string `json:"title"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if out.Title == nil {
		t.Fatalf("no \"title\" key in %q", body)
	}
	return *out.Title
}

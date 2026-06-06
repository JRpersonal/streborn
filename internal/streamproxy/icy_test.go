package streamproxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseStreamTitle(t *testing.T) {
	cases := []struct {
		name string
		meta string
		want string
		ok   bool
	}{
		{"normal", "StreamTitle='Artist - Song';StreamUrl='http://x';", "Artist - Song", true},
		{"padded with NULs", "StreamTitle='Hello';\x00\x00\x00", "Hello", true},
		{"empty title", "StreamTitle='';", "", true},
		{"only stream title", "StreamTitle='Just This';", "Just This", true},
		{"no semicolon, lone quote", "StreamTitle='Trailing'", "Trailing", true},
		{"quotes in title", "StreamTitle='AC';DC';", "AC", true}, // closes at first ';
		{"no field", "StreamUrl='http://x';", "", false},
		{"garbage", "not metadata at all", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseStreamTitle(c.meta)
			if ok != c.ok || got != c.want {
				t.Fatalf("parseStreamTitle(%q) = (%q,%v), want (%q,%v)", c.meta, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestICYMetaint(t *testing.T) {
	mk := func(v string) http.Header {
		h := http.Header{}
		if v != "" {
			h.Set("icy-metaint", v)
		}
		return h
	}
	if got := icyMetaint(mk("16384")); got != 16384 {
		t.Fatalf("icyMetaint(16384) = %d", got)
	}
	if got := icyMetaint(mk("")); got != 0 {
		t.Fatalf("icyMetaint(absent) = %d, want 0", got)
	}
	if got := icyMetaint(mk("0")); got != 0 {
		t.Fatalf("icyMetaint(0) = %d, want 0", got)
	}
	if got := icyMetaint(mk("nonsense")); got != 0 {
		t.Fatalf("icyMetaint(nonsense) = %d, want 0", got)
	}
}

// buildICYStream interleaves audio and one metadata block at metaint spacing,
// the exact wire format the proxy de-interleaves: metaint audio bytes, one
// length byte (block size / 16, rounded up), then the padded metadata block.
func buildICYStream(metaint int, audio []byte, meta string) []byte {
	var out bytes.Buffer
	// pad metadata to a 16-byte multiple as Icecast does
	padded := meta
	if rem := len(padded) % 16; rem != 0 {
		padded += strings.Repeat("\x00", 16-rem)
	}
	lenByte := byte(len(padded) / 16)
	for off := 0; off < len(audio); off += metaint {
		end := off + metaint
		if end > len(audio) {
			end = len(audio)
		}
		chunk := audio[off:end]
		out.Write(chunk)
		// a metadata block follows every full metaint chunk
		if len(chunk) == metaint {
			out.WriteByte(lenByte)
			out.WriteString(padded)
		}
	}
	return out.Bytes()
}

func TestICYReaderDeinterleavesAndParses(t *testing.T) {
	metaint := 8
	// 3 full chunks of 8 audio bytes -> 3 metadata blocks
	audio := []byte("AAAAAAAABBBBBBBBCCCCCCCC")
	stream := buildICYStream(metaint, audio, "StreamTitle='Now Playing';")

	var titles []string
	r := newICYReader(bytes.NewReader(stream), metaint, func(meta string) {
		if title, ok := parseStreamTitle(meta); ok {
			titles = append(titles, title)
		}
	})

	// Read through a deliberately small buffer so a metadata boundary is
	// crossed mid-buffer: the reader must never leak a length byte or a
	// metadata byte into the audio it yields.
	got, err := io.ReadAll(&smallReader{r: r, n: 3})
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, audio) {
		t.Fatalf("de-interleaved audio = %q, want %q", got, audio)
	}
	if len(titles) == 0 {
		t.Fatalf("no StreamTitle parsed")
	}
	for _, ti := range titles {
		if ti != "Now Playing" {
			t.Fatalf("parsed title = %q, want %q", ti, "Now Playing")
		}
	}
}

// smallReader forces Read to return at most n bytes per call, exercising the
// icyReader across buffer boundaries.
type smallReader struct {
	r io.Reader
	n int
}

func (s *smallReader) Read(p []byte) (int, error) {
	if len(p) > s.n {
		p = p[:s.n]
	}
	return s.r.Read(p)
}

func TestICYReaderNoMetadataBlocks(t *testing.T) {
	// metaint larger than the whole stream: no metadata block is ever read,
	// all bytes are audio, no title fires.
	metaint := 1024
	audio := []byte("plain audio with no metadata block yet")
	fired := false
	r := newICYReader(bytes.NewReader(audio), metaint, func(string) { fired = true })
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, audio) {
		t.Fatalf("audio = %q, want %q", got, audio)
	}
	if fired {
		t.Fatalf("onMeta fired with no metadata block present")
	}
}

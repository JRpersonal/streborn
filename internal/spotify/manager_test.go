package spotify

import (
	"bytes"
	"testing"
)

// oggBOS is a minimal Ogg page header that begins a logical bitstream:
// "OggS", version 0, header_type 0x02 (BOS).
var oggBOS = []byte{'O', 'g', 'g', 'S', 0x00, 0x02, 0, 0, 0, 0, 0, 0, 0, 0}

func TestFindOggBOS(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want int
	}{
		{"at start", oggBOS, 0},
		{"after garbage", append([]byte{0x01, 0x02, 0x03, 0xff, 0x00}, oggBOS...), 5},
		{"none", []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}, -1},
		{"OggS but not BOS (0x00 header_type)", []byte{'O', 'g', 'g', 'S', 0x00, 0x00, 0, 0}, -1},
		{"OggS but EOS only (0x04)", []byte{'O', 'g', 'g', 'S', 0x00, 0x04, 0, 0}, -1},
		{"too short", []byte{'O', 'g', 'g', 'S', 0x00}, -1},
	}
	for _, c := range cases {
		if got := findOggBOS(c.in); got != c.want {
			t.Errorf("%s: findOggBOS = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestResyncAcrossChunks simulates the drain's resync: stale bytes then a
// BOS, fed in arbitrary chunk splits, must yield exactly the bytes from the
// BOS onward (the box must receive a stream starting at a track header).
func TestResyncAcrossChunks(t *testing.T) {
	stale := bytes.Repeat([]byte{0xAB}, 5000)
	track := append(append([]byte{}, oggBOS...), bytes.Repeat([]byte{0xCD}, 3000)...)
	full := append(append([]byte{}, stale...), track...)

	for _, chunkSize := range []int{1, 3, 7, 13, 4096, 16384} {
		var carry, out []byte
		resync := true
		for off := 0; off < len(full); off += chunkSize {
			end := off + chunkSize
			if end > len(full) {
				end = len(full)
			}
			chunk := full[off:end]
			if resync {
				data := append(carry, chunk...)
				if idx := findOggBOS(data); idx >= 0 {
					resync = false
					carry = nil
					out = append(out, data[idx:]...)
				} else {
					carry = tailBytes(data, 6)
				}
			} else {
				out = append(out, chunk...)
			}
		}
		if !bytes.Equal(out, track) {
			t.Errorf("chunkSize=%d: resync output len=%d want=%d (must start at BOS)", chunkSize, len(out), len(track))
		}
	}
}

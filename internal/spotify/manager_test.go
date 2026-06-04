package spotify

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
)

// makeOggPage builds a minimal valid Ogg page (single segment, body < 255).
func makeOggPage(headerType byte, granule int64, body []byte) []byte {
	p := make([]byte, 0, 27+1+len(body))
	p = append(p, 'O', 'g', 'g', 'S')
	p = append(p, 0) // version
	p = append(p, headerType)
	var g [8]byte
	binary.LittleEndian.PutUint64(g[:], uint64(granule))
	p = append(p, g[:]...)
	p = append(p, 1, 2, 3, 4)             // serial
	p = append(p, 0, 0, 0, 0)             // page seq
	p = append(p, 0xDE, 0xAD, 0xBE, 0xEF) // crc (unchecked here)
	p = append(p, 1)                      // page_segments = 1
	p = append(p, byte(len(body)))
	p = append(p, body...)
	return p
}

func TestReadOggPageRoundtrip(t *testing.T) {
	pages := [][]byte{
		makeOggPage(0x02, 0, []byte("idheader")),
		makeOggPage(0x00, 0, []byte("setup")),
		makeOggPage(0x00, 1234, []byte("audio1")),
	}
	var stream []byte
	// prepend junk to exercise the OggS sync
	stream = append(stream, 0x11, 0x22, 0x33)
	for _, p := range pages {
		stream = append(stream, p...)
	}
	r := bufio.NewReader(bytes.NewReader(stream))
	for i, want := range pages {
		got, err := readOggPage(r)
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("page %d mismatch: got %d bytes want %d", i, len(got), len(want))
		}
	}
}

// TestHeaderCapture replicates the drain's header-capture switch and checks
// it keeps exactly the BOS + granule<=0 pages and stops at the first audio
// page (granule>0).
func TestHeaderCapture(t *testing.T) {
	bos := makeOggPage(0x02, 0, []byte("id"))
	setup := makeOggPage(0x00, 0, []byte("comment+setup"))
	audio1 := makeOggPage(0x00, 100, []byte("audioA"))
	audio2 := makeOggPage(0x00, 200, []byte("audioB"))
	bos2 := makeOggPage(0x02, 0, []byte("id2")) // next track resets capture

	stream := bytes.Join([][]byte{bos, setup, audio1, audio2, bos2}, nil)
	r := bufio.NewReader(bytes.NewReader(stream))

	var hdr []byte
	capturing := false
	var committed []byte
	for {
		page, err := readOggPage(r)
		if err != nil {
			break
		}
		htype := page[5]
		gran := int64(binary.LittleEndian.Uint64(page[6:14]))
		switch {
		case htype&0x02 != 0:
			hdr = append([]byte(nil), page...)
			capturing = true
		case capturing && gran > 0:
			committed = hdr
			capturing = false
		case capturing:
			hdr = append(hdr, page...)
		}
	}
	want := append(append([]byte(nil), bos...), setup...)
	if !bytes.Equal(committed, want) {
		t.Errorf("captured headers = %d bytes, want %d (BOS+setup only)", len(committed), len(want))
	}
}

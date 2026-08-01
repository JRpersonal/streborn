package spotify

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// A mid-page passthrough source handoff in go-librespot truncates the old
// track's page and lets its declared body length swallow the next track's
// BOS and header pages (field trace 2026-08-01: no boundary log, skip-cut
// never disarmed, new audio spliced into the old logical stream). The page
// reader must detect the checksum mismatch and recover the swallowed pages.
func TestReadPageResyncsSplicedBOS(t *testing.T) {
	// The old track's page as originally emitted: 200-byte body, valid CRC.
	oldBody := bytes.Repeat([]byte{0xAA}, 200)
	oldPage := makeOggPage(0x00, 5000, oldBody)

	// The new track's pages that the splice buries.
	bos := makeOggPage(0x02, 0, []byte("new-track-id-header"))
	setup := makeOggPage(0x00, 0, []byte("new-track-setup"))
	audio := makeOggPage(0x00, 777, []byte("new-track-audio"))
	// Enough follow-up data that the swallowed 200-byte body can be fully
	// assembled before the checksum exposes it (in the field the stream just
	// keeps flowing; only the end of this TEST stream is near).
	audio2 := makeOggPage(0x00, 888, bytes.Repeat([]byte{0xBB}, 120))

	// The splice: the old page is cut off after 60 body bytes and the new
	// track's stream begins right there. The old header still declares 200
	// body bytes, so a length-trusting reader consumes the BOS and setup
	// pages as body.
	spliced := append([]byte{}, oldPage[:27+1+60]...)
	spliced = append(spliced, bos...)
	spliced = append(spliced, setup...)
	spliced = append(spliced, audio...)
	spliced = append(spliced, audio2...)

	r := newOggPageReader(bytes.NewReader(spliced), nil)

	first, err := r.ReadPage()
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first[5]&0x02 == 0 {
		t.Fatalf("first recovered page is not the BOS (header_type=%#x)", first[5])
	}
	if !bytes.Equal(first, bos) {
		t.Fatalf("recovered BOS differs from the emitted one")
	}
	second, err := r.ReadPage()
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if !bytes.Equal(second, setup) {
		t.Fatalf("recovered setup page differs")
	}
	third, err := r.ReadPage()
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if !bytes.Equal(third, audio) {
		t.Fatalf("recovered audio page differs")
	}
	if gran := int64(binary.LittleEndian.Uint64(third[6:14])); gran != 777 {
		t.Fatalf("audio granule = %d, want 777", gran)
	}
	fourth, err := r.ReadPage()
	if err != nil || !bytes.Equal(fourth, audio2) {
		t.Fatalf("fourth page mangled (err=%v)", err)
	}
	if _, err := r.ReadPage(); !errors.Is(err, io.EOF) && err == nil {
		t.Fatalf("expected end of stream, got another page")
	}
}

// A clean handoff at an exact page boundary must pass through untouched:
// every page checksums correctly and nothing is dropped.
func TestReadPageCleanBoundaryUntouched(t *testing.T) {
	oldAudio := makeOggPage(0x00, 4242, []byte("old-final-audio"))
	bos := makeOggPage(0x02, 0, []byte("next-track-id"))
	stream := append(append([]byte{}, oldAudio...), bos...)

	r := newOggPageReader(bytes.NewReader(stream), nil)
	got1, err := r.ReadPage()
	if err != nil || !bytes.Equal(got1, oldAudio) {
		t.Fatalf("clean page 1 mangled (err=%v)", err)
	}
	got2, err := r.ReadPage()
	if err != nil || !bytes.Equal(got2, bos) {
		t.Fatalf("clean page 2 mangled (err=%v)", err)
	}
}

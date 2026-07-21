package streamproxy

import "testing"

func TestPickMasterVariantLowestBandwidth(t *testing.T) {
	master := "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=320000,CODECS=\"mp4a.40.2\"\n" +
		"high/playlist.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=48000,CODECS=\"mp4a.40.2\"\n" +
		"low/playlist.m3u8\n"
	got := pickMasterVariant(master, "https://cdn.example.com/bbc/master.m3u8")
	want := "https://cdn.example.com/bbc/low/playlist.m3u8"
	if got != want {
		t.Errorf("pickMasterVariant = %q, want %q (lowest bandwidth, resolved)", got, want)
	}
}

func TestParseMediaPlaylistSegmentsAndFlags(t *testing.T) {
	media := "#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:6\n" +
		"#EXT-X-MEDIA-SEQUENCE:100\n" +
		"#EXTINF:6.0,\n" +
		"seg100.ts\n" +
		"#EXTINF:6.0,\n" +
		"seg101.ts\n"
	pl := parseMediaPlaylist(media, "https://cdn.example.com/bbc/low/playlist.m3u8")
	if len(pl.segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(pl.segments))
	}
	if pl.segments[0] != "https://cdn.example.com/bbc/low/seg100.ts" {
		t.Errorf("segment[0] = %q, want resolved seg100.ts", pl.segments[0])
	}
	if pl.targetDur != 6 {
		t.Errorf("targetDur = %d, want 6", pl.targetDur)
	}
	if pl.mediaSeq != 100 {
		t.Errorf("mediaSeq = %d, want 100 (EXT-X-MEDIA-SEQUENCE drives live-edge dedup)", pl.mediaSeq)
	}
	if !pl.hasMediaSeq {
		t.Errorf("hasMediaSeq = false, want true (tag was present)")
	}
	if pl.endList {
		t.Errorf("endList = true, want false (live)")
	}
	if pl.isFMP4 {
		t.Errorf("isFMP4 = true, want false (TS segments)")
	}
}

// A live playlist that omits EXT-X-MEDIA-SEQUENCE must set hasMediaSeq=false so
// serveHLS falls back to URL dedup instead of trusting a sequence cursor that
// would read every refresh's segment[0] as sequence 0 (stall/replay). mediaSeq
// itself defaults to 0, which is why the separate presence flag is needed.
func TestParseMediaPlaylistNoMediaSequence(t *testing.T) {
	media := "#EXTM3U\n" +
		"#EXT-X-TARGETDURATION:4\n" +
		"#EXTINF:4.0,\n" +
		"a.ts\n" +
		"#EXTINF:4.0,\n" +
		"b.ts\n"
	pl := parseMediaPlaylist(media, "https://x/y/playlist.m3u8")
	if pl.hasMediaSeq {
		t.Errorf("hasMediaSeq = true, want false (no EXT-X-MEDIA-SEQUENCE tag)")
	}
	if pl.mediaSeq != 0 {
		t.Errorf("mediaSeq = %d, want 0 default", pl.mediaSeq)
	}
	if pl.endList {
		t.Errorf("endList = true, want false (live)")
	}
}

func TestParseMediaPlaylistDetectsFMP4AndVOD(t *testing.T) {
	media := "#EXTM3U\n" +
		"#EXT-X-MAP:URI=\"init.mp4\"\n" +
		"#EXTINF:4.0,\n" +
		"seg0.m4s\n" +
		"#EXT-X-ENDLIST\n"
	pl := parseMediaPlaylist(media, "https://x/y/playlist.m3u8")
	if !pl.isFMP4 {
		t.Errorf("isFMP4 = false, want true (EXT-X-MAP present)")
	}
	if !pl.endList {
		t.Errorf("endList = false, want true (ENDLIST present)")
	}
}

func TestDemuxSegmentFormatDetection(t *testing.T) {
	// fMP4 box-structured segment -> unsupported (stage 3).
	fmp4 := append([]byte{0, 0, 0, 0x18}, []byte("ftypiso5")...)
	if _, ok := demuxSegment(append(fmp4, make([]byte, 16)...)); ok {
		t.Errorf("fMP4 segment reported playable, want unsupported")
	}
	// Raw ADTS-AAC (sync 0xFFF, layer 00) -> pass through.
	adts := []byte{0xFF, 0xF1, 0x50, 0x80, 0x00, 0x1F, 0xFC}
	if out, ok := demuxSegment(adts); !ok || len(out) == 0 {
		t.Errorf("raw ADTS not passed through (ok=%v)", ok)
	}
	// Raw MP3 (sync 0xFFE) -> pass through.
	mp3 := []byte{0xFF, 0xFB, 0x90, 0x00}
	if _, ok := demuxSegment(mp3); !ok {
		t.Errorf("raw MP3 not passed through")
	}
}

func TestTSExtractAudioRoundTrip(t *testing.T) {
	seg := buildTSWithAAC([]byte{0xFF, 0xF1, 0x4C, 0x80, 0x02, 0x1F, 0xFC, 0xDE, 0xAD})
	out := tsExtractAudio(seg)
	if len(out) == 0 {
		t.Fatalf("tsExtractAudio returned no audio")
	}
	// The extracted ES must begin with the ADTS sync we fed in.
	if out[0] != 0xFF || out[1] != 0xF1 {
		t.Errorf("extracted ES does not start with the ADTS frame: %x", out[:min(4, len(out))])
	}
}

// buildTSWithAAC builds a minimal 3-packet MPEG-TS segment (PAT, PMT with one
// ADTS-AAC stream on PID 0x100, and one PES packet carrying payload) so the
// demuxer can be exercised without a real capture.
func buildTSWithAAC(payload []byte) []byte {
	const pktLen = 188
	pad := func(p []byte) []byte {
		for len(p) < pktLen {
			p = append(p, 0xFF)
		}
		return p[:pktLen]
	}

	// PAT on PID 0: one program (number 1) -> PMT PID 0x20.
	pat := []byte{0x47, 0x40, 0x00, 0x10, 0x00} // sync, PUSI+PID0, payload-only CC0, pointer_field 0
	patSection := []byte{
		0x00,       // table_id PAT
		0xB0, 0x0D, // section_syntax + length 13
		0x00, 0x01, // transport_stream_id
		0xC1,       // version/current
		0x00, 0x00, // section_number / last
		0x00, 0x01, // program_number 1
		0xE0, 0x20, // reserved + PMT PID 0x20
		0x00, 0x00, 0x00, 0x00, // CRC (not validated by the demuxer)
	}
	pat = append(pat, patSection...)

	// PMT on PID 0x20: one ES, stream_type 0x0F (ADTS-AAC), PID 0x100.
	pmt := []byte{0x47, 0x40, 0x20, 0x10, 0x00} // PUSI + PID 0x20, pointer 0
	pmtSection := []byte{
		0x02,       // table_id PMT
		0xB0, 0x12, // length 18
		0x00, 0x01, // program_number
		0xC1, 0x00, 0x00,
		0xE1, 0x00, // PCR PID 0x100
		0x00, 0x00, // program_info_length 0 (bytes 10..11 low 12 bits = 0)
		0x0F,       // stream_type ADTS-AAC
		0xE1, 0x00, // elementary PID 0x100
		0x00, 0x00, // ES_info_length 0
		0x00, 0x00, 0x00, 0x00, // CRC
	}
	pmt = append(pmt, pmtSection...)

	// PES on PID 0x100: PES start code, stream_id audio, len, flags, hdrlen 0,
	// then the elementary payload.
	pes := []byte{0x47, 0x41, 0x00, 0x10} // PUSI + PID 0x100, payload-only
	pesHdr := []byte{0x00, 0x00, 0x01, 0xC0, 0x00, 0x00, 0x80, 0x00, 0x00}
	pes = append(pes, pesHdr...)
	pes = append(pes, payload...)

	out := make([]byte, 0, pktLen*3)
	out = append(out, pad(pat)...)
	out = append(out, pad(pmt)...)
	out = append(out, pad(pes)...)
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

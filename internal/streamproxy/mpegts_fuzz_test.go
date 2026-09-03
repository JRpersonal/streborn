package streamproxy

import (
	"testing"
	"time"
)

// FuzzDemuxSegment feeds arbitrary bytes into tsExtractAudio, the MPEG-TS
// demux entry point for HLS segments. The demuxer indexes into TS packets,
// PSI sections, and PES headers based on length fields read from the input,
// so the target asserts it never panics (a panic, e.g. a slice out-of-range,
// fails the fuzz run), returns quickly, and never emits more bytes than it
// was given (pass 2 only copies payload slices out of the input).
func FuzzDemuxSegment(f *testing.F) {
	// Empty input.
	f.Add([]byte{})
	// Truncated buffer, shorter than one 188-byte TS packet.
	f.Add([]byte{0x47, 0x40, 0x00})
	// A single valid TS packet: sync byte, PUSI, PID 0, PAT payload.
	f.Add(fuzzPATPacket())
	// PAT -> PMT -> audio PES packet whose header_data_length is 255, which
	// pushes the elementary-stream offset (9+255) past the 184-byte payload.
	f.Add(fuzzSegmentWithPESHeaderLen(0xFF))
	// Same chain with a sane PES header, so the append path is covered too.
	f.Add(fuzzSegmentWithPESHeaderLen(0x00))
	// Adaptation-field length that pushes the payload offset to the packet end.
	adaptEdge := make([]byte, tsPacketLen)
	adaptEdge[0] = 0x47
	adaptEdge[1] = 0x41
	adaptEdge[2] = 0x01
	adaptEdge[3] = 0x30 // adaptation field + payload
	adaptEdge[4] = 183  // 5+183 == 188, one past the last index
	f.Add(adaptEdge)
	// Random bytes (fixed, so the seed corpus is deterministic).
	f.Add([]byte("\x8f\x47\x00\x12\xfeexample random bytes \x00\xff\x10\x47\x47\x47junk"))

	f.Fuzz(func(t *testing.T, data []byte) {
		begin := time.Now()
		out := tsExtractAudio(data)
		if d := time.Since(begin); d > 5*time.Second {
			t.Fatalf("tsExtractAudio took %v on %d input bytes", d, len(data))
		}
		if len(out) > len(data) {
			t.Fatalf("tsExtractAudio returned %d bytes from %d input bytes", len(out), len(data))
		}
	})
}

// fuzzPATPacket builds one 188-byte TS packet on PID 0 carrying a PAT that
// maps program 1 to PMT PID 0x0100.
func fuzzPATPacket() []byte {
	p := make([]byte, tsPacketLen)
	for i := range p {
		p[i] = 0xFF // stuffing
	}
	p[0] = 0x47 // sync
	p[1] = 0x40 // PUSI set, PID 0 (high bits)
	p[2] = 0x00 // PID 0 (low bits)
	p[3] = 0x10 // payload only, cc 0
	p[4] = 0x00 // pointer_field
	copy(p[5:], []byte{
		0x00,       // table_id: PAT
		0xB0, 0x0D, // section_length 13
		0x00, 0x01, // transport_stream_id
		0xC1,       // version 0, current
		0x00, 0x00, // section_number, last_section_number
		0x00, 0x01, // program_number 1
		0xE1, 0x00, // PMT PID 0x0100
		0x00, 0x00, 0x00, 0x00, // CRC (not checked by the demuxer)
	})
	return p
}

// fuzzSegmentWithPESHeaderLen builds a three-packet segment: PAT (PMT PID
// 0x0100), PMT (ADTS-AAC audio on PID 0x0101), and one audio PES packet with
// the given header_data_length byte.
func fuzzSegmentWithPESHeaderLen(hdrLen byte) []byte {
	pmt := make([]byte, tsPacketLen)
	for i := range pmt {
		pmt[i] = 0xFF
	}
	pmt[0] = 0x47
	pmt[1] = 0x41 // PUSI set, PID 0x0100 (high bits)
	pmt[2] = 0x00 // PID 0x0100 (low bits)
	pmt[3] = 0x10
	pmt[4] = 0x00 // pointer_field
	copy(pmt[5:], []byte{
		0x02,       // table_id: PMT
		0xB0, 0x12, // section_length 18
		0x00, 0x01, // program_number 1
		0xC1,       // version 0, current
		0x00, 0x00, // section_number, last_section_number
		0xE1, 0x01, // PCR PID
		0xF0, 0x00, // program_info_length 0
		0x0F,       // stream_type: ADTS-AAC
		0xE1, 0x01, // elementary PID 0x0101
		0xF0, 0x00, // ES_info_length 0
		0x00, 0x00, 0x00, 0x00, // CRC (not checked by the demuxer)
	})

	pes := make([]byte, tsPacketLen)
	for i := range pes {
		pes[i] = 0xAA // stand-in elementary-stream bytes
	}
	pes[0] = 0x47
	pes[1] = 0x41 // PUSI set, PID 0x0101 (high bits)
	pes[2] = 0x01 // PID 0x0101 (low bits)
	pes[3] = 0x10
	copy(pes[4:], []byte{
		0x00, 0x00, 0x01, // PES start code
		0xC0,       // stream_id: audio
		0x00, 0x00, // PES_packet_length (unbounded)
		0x80, 0x80, // flags
		hdrLen, // header_data_length
	})

	seg := fuzzPATPacket()
	seg = append(seg, pmt...)
	seg = append(seg, pes...)
	return seg
}

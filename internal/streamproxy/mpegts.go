package streamproxy

// Minimal MPEG-TS audio demuxer for HLS (#124, stage 2). HLS segments for BBC
// and similar stations are MPEG-TS containers carrying AAC (in ADTS framing) or
// MP3. The Bose player wants the bare audio elementary stream, so this pulls the
// audio PID's PES payloads out of the 188-byte TS packets and returns them as a
// continuous ADTS/MP3 byte run. It is deliberately small and tolerant: anything
// it cannot parse yields an empty result and the caller falls back to
// not-playable rather than feeding the box garbage.
//
// It handles the common single-program radio case: PAT -> first program's PMT
// -> first audio elementary stream (stream_type 0x0F ADTS-AAC, 0x03/0x04 MP3).
// LATM-AAC (0x11) and other types are ignored (the box cannot decode them).

const tsPacketLen = 188

// tsExtractAudio returns the audio elementary stream bytes (ADTS-AAC or MP3)
// demuxed from an MPEG-TS segment, or nil if no playable audio PID was found.
func tsExtractAudio(data []byte) []byte {
	// Align to the first sync byte; some segments carry a few leading bytes.
	start := 0
	for start < len(data) && data[start] != 0x47 {
		start++
	}
	data = data[start:]

	pmtPID := -1
	audioPID := -1

	// Pass 1: PAT -> PMT PID, then PMT -> audio PID.
	for off := 0; off+tsPacketLen <= len(data); off += tsPacketLen {
		p := data[off : off+tsPacketLen]
		if p[0] != 0x47 {
			continue
		}
		pid := int(p[1]&0x1F)<<8 | int(p[2])
		payload, pusi, ok := tsPayload(p)
		if !ok {
			continue
		}
		if pid == 0 && pmtPID < 0 {
			pmtPID = parsePAT(payload, pusi)
		} else if pmtPID >= 0 && pid == pmtPID && audioPID < 0 {
			audioPID = parsePMT(payload, pusi)
		}
		if audioPID >= 0 {
			break
		}
	}
	if audioPID < 0 {
		return nil
	}

	// Pass 2: collect the audio PID's PES payloads (stripping PES headers) into a
	// continuous elementary stream. Preallocate to the segment size so a typical
	// few-second segment never reallocates: this is pure container demux (no
	// audio decode), so it stays cheap on the speaker's constrained CPU.
	out := make([]byte, 0, len(data))
	for off := 0; off+tsPacketLen <= len(data); off += tsPacketLen {
		p := data[off : off+tsPacketLen]
		if p[0] != 0x47 {
			continue
		}
		pid := int(p[1]&0x1F)<<8 | int(p[2])
		if pid != audioPID {
			continue
		}
		payload, pusi, ok := tsPayload(p)
		if !ok {
			continue
		}
		if pusi && len(payload) >= 9 && payload[0] == 0x00 && payload[1] == 0x00 && payload[2] == 0x01 {
			// PES header present: 00 00 01, stream_id, PES_len(2), flags(2),
			// header_data_length(1), then that many optional-header bytes, then
			// the elementary stream payload.
			hdrLen := int(payload[8])
			es := 9 + hdrLen
			if es <= len(payload) {
				out = append(out, payload[es:]...)
			}
		} else {
			out = append(out, payload...)
		}
	}
	return out
}

// tsPayload returns the payload bytes of a TS packet, whether the
// payload-unit-start-indicator is set, and false if the packet carries no
// payload (adaptation-only or malformed).
func tsPayload(p []byte) (payload []byte, pusi, ok bool) {
	afc := (p[3] >> 4) & 0x3
	if afc&0x1 == 0 {
		return nil, false, false // no payload
	}
	off := 4
	if afc&0x2 != 0 { // adaptation field present
		afl := int(p[4])
		off = 5 + afl
	}
	if off >= len(p) {
		return nil, false, false
	}
	return p[off:], p[1]&0x40 != 0, true
}

// parsePAT returns the PMT PID of the first real program in a PAT payload, or
// -1. pusi means a pointer_field precedes the section.
func parsePAT(payload []byte, pusi bool) int {
	pp := skipPointer(payload, pusi)
	if len(pp) < 8 {
		return -1
	}
	secLen := int(pp[1]&0x0F)<<8 | int(pp[2])
	end := 3 + secLen
	if end > len(pp) {
		end = len(pp)
	}
	// Program loop starts at byte 8 (after table_id..last_section_number); each
	// entry is 4 bytes; the trailing 4 bytes of the section are the CRC.
	for i := 8; i+4 <= end-4; i += 4 {
		prog := int(pp[i])<<8 | int(pp[i+1])
		pid := int(pp[i+2]&0x1F)<<8 | int(pp[i+3])
		if prog != 0 { // program 0 is the network PID, not a PMT
			return pid
		}
	}
	return -1
}

// parsePMT returns the first audio elementary-stream PID (ADTS-AAC or MP3) in a
// PMT payload, or -1.
func parsePMT(payload []byte, pusi bool) int {
	pp := skipPointer(payload, pusi)
	if len(pp) < 12 {
		return -1
	}
	secLen := int(pp[1]&0x0F)<<8 | int(pp[2])
	end := 3 + secLen
	if end > len(pp) {
		end = len(pp)
	}
	progInfoLen := int(pp[10]&0x0F)<<8 | int(pp[11])
	i := 12 + progInfoLen
	for i+5 <= end-4 {
		streamType := pp[i]
		espid := int(pp[i+1]&0x1F)<<8 | int(pp[i+2])
		esInfoLen := int(pp[i+3]&0x0F)<<8 | int(pp[i+4])
		switch streamType {
		case 0x0F, 0x03, 0x04: // ADTS-AAC, MP3 (MPEG-1/2 audio)
			return espid
		}
		i += 5 + esInfoLen
	}
	return -1
}

// skipPointer drops the pointer_field that precedes a PSI section when the
// packet's payload-unit-start-indicator is set.
func skipPointer(payload []byte, pusi bool) []byte {
	if !pusi {
		return payload
	}
	if len(payload) < 1 {
		return payload
	}
	ptr := int(payload[0])
	if 1+ptr > len(payload) {
		return payload[len(payload):]
	}
	return payload[1+ptr:]
}

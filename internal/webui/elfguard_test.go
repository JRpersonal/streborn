package webui

import "testing"

// A mis-built agent embed (hard-float, or wrong arch) used to be accepted by the
// OTA write path on the ELF magic alone: it would flash, pass the flash verify,
// reboot, and then SIGILL on every start, crash-looping the agent on a stickless
// box with no way back (#302). validSoftfloatARMELF is the gate that stops it.
func TestValidSoftfloatARMELF(t *testing.T) {
	// A minimal, valid 32-bit LE softfloat ARM ELF header (e_flags 0x05000002,
	// exactly what our GOARM=5 agent + engine carry), padded past the offsets.
	base := make([]byte, 64)
	copy(base, []byte{0x7f, 'E', 'L', 'F'})
	base[4] = 1 // ELFCLASS32
	base[5] = 1 // ELFDATA2LSB
	base[18] = 40
	base[19] = 0 // e_machine = EM_ARM
	// e_flags = 0x05000002 at offset 36 (little-endian)
	base[36], base[37], base[38], base[39] = 0x02, 0x00, 0x00, 0x05

	mut := func(f func([]byte)) []byte { b := append([]byte(nil), base...); f(b); return b }

	cases := []struct {
		name string
		body []byte
		ok   bool
	}{
		{"valid softfloat arm", base, true},
		{"hard-float (e_flags 0x400 set)", mut(func(b []byte) { b[37] = 0x04 }), false}, // 0x05000402 -> HARD bit set
		{"wrong arch (x86-64)", mut(func(b []byte) { b[18] = 62; b[19] = 0 }), false},
		{"64-bit class", mut(func(b []byte) { b[4] = 2 }), false},
		{"big-endian data", mut(func(b []byte) { b[5] = 2 }), false},
		{"not an ELF", mut(func(b []byte) { b[1] = 'X' }), false},
		{"too small", []byte{0x7f, 'E', 'L', 'F'}, false},
	}
	for _, c := range cases {
		_, ok := validSoftfloatARMELF(c.body)
		if ok != c.ok {
			t.Errorf("%s: ok=%v, want %v", c.name, ok, c.ok)
		}
	}
}

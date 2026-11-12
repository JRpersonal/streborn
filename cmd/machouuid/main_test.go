package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

const (
	cpuAmd64 = 0x01000007
	cpuArm64 = 0x0100000c
)

// slice builds a minimal 64-bit Mach-O image: a header, one filler load
// command, optionally an LC_UUID, and some payload behind the commands.
func slice(cpu uint32, uuid []byte) []byte {
	var cmds bytes.Buffer
	// LC_SOURCE_VERSION (0x2a), 16 bytes: something to walk past.
	_ = binary.Write(&cmds, binary.LittleEndian, []uint32{0x2a, 16, 0, 0})
	n := uint32(1)
	if uuid != nil {
		_ = binary.Write(&cmds, binary.LittleEndian, []uint32{lcUUID, 24})
		cmds.Write(uuid)
		n++
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, []uint32{machMagic, cpu, 3, 2, n, uint32(cmds.Len()), 0, 0})
	b.Write(cmds.Bytes())
	b.WriteString("payload-that-must-not-change")
	return b.Bytes()
}

// fat wraps slices in a universal header, each at a 64-byte boundary.
func fat(slices ...[]byte) []byte {
	head := 8 + 20*len(slices)
	var body bytes.Buffer
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, []uint32{fatMagic, uint32(len(slices))})
	off := (head + 63) / 64 * 64
	for _, s := range slices {
		cpu := binary.LittleEndian.Uint32(s[4:8])
		_ = binary.Write(&b, binary.BigEndian, []uint32{cpu, 3, uint32(off), uint32(len(s)), 6})
		pad := off - head - body.Len()
		body.Write(make([]byte, pad))
		body.Write(s)
		off = (off + len(s) + 63) / 64 * 64
	}
	b.Write(body.Bytes())
	return b.Bytes()
}

func uuidOf(t *testing.T, img []byte) [16]byte {
	t.Helper()
	f, err := macho.NewFile(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("debug/macho does not read the image back: %v", err)
	}
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) == 24 && binary.LittleEndian.Uint32(raw[0:4]) == lcUUID {
			var u [16]byte
			copy(u[:], raw[8:])
			return u
		}
	}
	t.Fatal("no LC_UUID in the image")
	return [16]byte{}
}

func TestPinThinImage(t *testing.T) {
	old := bytes.Repeat([]byte{0xAB}, 16)
	img := slice(cpuArm64, old)
	before := append([]byte(nil), img...)
	done, err := pin(img, "de.st-reborn.app")
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Arch != "arm64" || !bytes.Equal(done[0].Was[:], old) {
		t.Fatalf("report = %+v", done)
	}
	want := stableUUID("de.st-reborn.app", cpuArm64)
	if got := uuidOf(t, img); got != want {
		t.Errorf("UUID = %x, want %x", got, want)
	}
	// Nothing but the sixteen UUID bytes may differ.
	diff := 0
	for i := range img {
		if img[i] != before[i] {
			diff++
		}
	}
	if len(img) != len(before) || diff > 16 {
		t.Errorf("%d bytes changed in an image of %d (was %d), only the UUID may", diff, len(img), len(before))
	}
}

func TestPinUniversalImageGivesEachSliceItsOwn(t *testing.T) {
	img := fat(slice(cpuAmd64, bytes.Repeat([]byte{1}, 16)), slice(cpuArm64, bytes.Repeat([]byte{2}, 16)))
	done, err := pin(img, "de.st-reborn.app")
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 || done[0].Arch != "x86_64" || done[1].Arch != "arm64" {
		t.Fatalf("report = %+v", done)
	}
	if done[0].Now == done[1].Now {
		t.Error("both architectures got the same UUID")
	}
	ff, err := macho.NewFatFile(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("debug/macho does not read the universal image back: %v", err)
	}
	if len(ff.Arches) != 2 {
		t.Fatalf("%d slices after pinning", len(ff.Arches))
	}
}

// The whole point: the value depends on the app and the architecture and on
// nothing else, so the next release carries the same one.
func TestStableUUIDIsStableAndPerApp(t *testing.T) {
	a := stableUUID("de.st-reborn.app", cpuArm64)
	if a != stableUUID("de.st-reborn.app", cpuArm64) {
		t.Error("two calls disagree")
	}
	if a == stableUUID("de.st-reborn.app", cpuAmd64) || a == stableUUID("com.example.other", cpuArm64) {
		t.Error("another architecture or another app shares the UUID")
	}
	if a[6]>>4 != 8 || a[8]>>6 != 2 {
		t.Errorf("%x is not a version 8, RFC 4122 UUID", a)
	}
	// Pinned on purpose. Changing the derivation would give every Mac user one
	// more release that macOS sees as a different program.
	if got := fmt.Sprintf("%x", a); got != "f51afe14b55c878cb8fe685989565a4e" {
		t.Errorf("arm64 UUID = %s, the pinned value changed", got)
	}
	if got := fmt.Sprintf("%x", stableUUID("de.st-reborn.app", cpuAmd64)); got != "ec39cc91d90d827bb636938421febd32" {
		t.Errorf("x86_64 UUID = %s, the pinned value changed", got)
	}
}

func TestPinRefusesAnImageWithoutUUID(t *testing.T) {
	_, err := pin(slice(cpuArm64, nil), "de.st-reborn.app")
	if err == nil || !strings.Contains(err.Error(), "no LC_UUID") {
		t.Fatalf("err = %v, want the missing LC_UUID named", err)
	}
	if _, err := pin(fat(slice(cpuAmd64, bytes.Repeat([]byte{1}, 16)), slice(cpuArm64, nil)), "x"); err == nil {
		t.Error("a universal image with one slice lacking LC_UUID was accepted")
	}
}

func TestPinRefusesGarbage(t *testing.T) {
	for _, img := range [][]byte{nil, []byte("MZ\x90\x00 not a mach-o file at all........"), slice(cpuArm64, nil)[:20]} {
		if _, err := pin(img, "x"); err == nil {
			t.Errorf("accepted %q", img)
		}
	}
}

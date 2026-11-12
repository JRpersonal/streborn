package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// thinMachO is a minimal 64-bit Mach-O image with one LC_UUID.
func thinMachO(cpu uint32, uuid []byte) []byte {
	var cmds bytes.Buffer
	_ = binary.Write(&cmds, binary.LittleEndian, []uint32{0x1b, 24})
	cmds.Write(uuid)
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, []uint32{0xfeedfacf, cpu, 3, 2, 1, uint32(cmds.Len()), 0, 0})
	b.Write(cmds.Bytes())
	return b.Bytes()
}

func TestMachoUUIDOfPicksTheRunningArchitecture(t *testing.T) {
	const amd64, arm64 = 0x01000007, 0x0100000c
	a := thinMachO(amd64, bytes.Repeat([]byte{0x11}, 16))
	r := thinMachO(arm64, bytes.Repeat([]byte{0x22}, 16))
	var fat bytes.Buffer
	_ = binary.Write(&fat, binary.BigEndian, []uint32{0xcafebabe, 2,
		amd64, 3, 64, uint32(len(a)), 6,
		arm64, 3, 128, uint32(len(r)), 6})
	fat.Write(make([]byte, 64-fat.Len()))
	fat.Write(a)
	fat.Write(make([]byte, 128-fat.Len()))
	fat.Write(r)

	dir := t.TempDir()
	universal := filepath.Join(dir, "universal")
	thin := filepath.Join(dir, "thin")
	notMachO := filepath.Join(dir, "other")
	for p, data := range map[string][]byte{universal: fat.Bytes(), thin: r, notMachO: []byte("MZ, a Windows program")} {
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := machoUUIDOf(universal, "arm64"); got != "22222222222222222222222222222222" {
		t.Errorf("arm64 slice = %q", got)
	}
	if got := machoUUIDOf(universal, "amd64"); got != "11111111111111111111111111111111" {
		t.Errorf("amd64 slice = %q", got)
	}
	if got := machoUUIDOf(thin, "arm64"); got != "22222222222222222222222222222222" {
		t.Errorf("thin image = %q", got)
	}
	if got := machoUUIDOf(notMachO, "arm64"); got != "" {
		t.Errorf("a file that is not Mach-O gave %q", got)
	}
}

package sticksetup

// The firmware write runs against temp folders only, never a real drive.

import (
	"bytes"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestHasOtherFiles(t *testing.T) {
	stick := t.TempDir()
	if HasOtherFiles(stick) {
		t.Error("an empty stick has no other files")
	}
	for _, n := range []string{FirmwareFileName, ".DS_Store", "._Update.stu", "Update.stu.part"} {
		if err := os.WriteFile(filepath.Join(stick, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"System Volume Information", ".Spotlight-V100", ".fseventsd"} {
		if err := os.Mkdir(filepath.Join(stick, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if HasOtherFiles(stick) {
		t.Error("Update.stu and the PC's own entries are not other files")
	}
	if err := os.Mkdir(filepath.Join(stick, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasOtherFiles(stick) {
		t.Error("a hidden folder is another file")
	}
	if HasOtherFiles(filepath.Join(stick, "missing")) {
		t.Error("an unreadable root reports false")
	}
}

func fakeImage(n int) ([]byte, uint32) {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b, crc32.ChecksumIEEE(b)
}

func TestWriteFirmwareFileWritesAndChecks(t *testing.T) {
	stick := t.TempDir()
	src := filepath.Join(t.TempDir(), "Update.stu")
	data, crc := fakeImage(300_000)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// STR's own files and an old Update.stu from the step before.
	if err := os.WriteFile(filepath.Join(stick, "install.sh"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stick, FirmwareFileName), []byte("old step"), 0o644); err != nil {
		t.Fatal(err)
	}
	var last int64
	if err := WriteFirmwareFile(stick, src, int64(len(data)), crc, func(d int64) { last = d }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(stick, FirmwareFileName))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stick holds %d bytes, err %v", len(got), err)
	}
	if last != int64(len(data)) {
		t.Errorf("progress ended at %d", last)
	}
	if _, err := os.Stat(filepath.Join(stick, FirmwareFileName+".part")); !os.IsNotExist(err) {
		t.Error("the .part file must be gone")
	}
	if b, _ := os.ReadFile(filepath.Join(stick, "install.sh")); string(b) != "keep" {
		t.Error("STR's own files on the stick must stay untouched")
	}
	if ExistingFirmwareBytes(stick) != int64(len(data)) {
		t.Error("ExistingFirmwareBytes")
	}
}

type failAfter struct {
	r io.Reader
	n int
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("stick pulled")
	}
	if len(p) > f.n {
		p = p[:f.n]
	}
	n, err := f.r.Read(p)
	f.n -= n
	return n, err
}

func TestWriteFirmwarePartialLeavesNoUpdateStu(t *testing.T) {
	stick := t.TempDir()
	data, crc := fakeImage(200_000)
	if err := os.WriteFile(filepath.Join(stick, FirmwareFileName), []byte("old step"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeFirmwareFrom(stick, &failAfter{r: bytes.NewReader(data), n: 50_000}, int64(len(data)), crc, nil)
	if err == nil {
		t.Fatal("a broken copy must fail")
	}
	for _, name := range []string{FirmwareFileName, FirmwareFileName + ".part"} {
		if _, err := os.Stat(filepath.Join(stick, name)); !os.IsNotExist(err) {
			t.Errorf("%s must not be on the stick after a failed write", name)
		}
	}
}

func TestWriteFirmwareRefusesWrongSource(t *testing.T) {
	stick := t.TempDir()
	data, crc := fakeImage(10_000)
	// Too short, too long, and the right length with the wrong bytes.
	for _, src := range [][]byte{data[:9_000], append(append([]byte{}, data...), 1), append([]byte{0xff}, data[1:]...)} {
		err := writeFirmwareFrom(stick, bytes.NewReader(src), int64(len(data)), crc, nil)
		if !errors.Is(err, ErrFirmwareMismatch) {
			t.Errorf("want a mismatch, got %v", err)
		}
		if ExistingFirmwareBytes(stick) != 0 {
			t.Error("a mismatching file must not stay on the stick")
		}
	}
}

func TestWriteFirmwareReadBackMismatchFails(t *testing.T) {
	stick := t.TempDir()
	data, crc := fakeImage(10_000)
	orig := firmwareReadBack
	defer func() { firmwareReadBack = orig }()
	firmwareReadBack = func(string) (int64, uint32, error) { return int64(len(data)), crc ^ 1, nil }
	err := writeFirmwareFrom(stick, bytes.NewReader(data), int64(len(data)), crc, nil)
	if !errors.Is(err, ErrFirmwareMismatch) {
		t.Fatalf("a stick that reads back other bytes must fail, got %v", err)
	}
	if ExistingFirmwareBytes(stick) != 0 {
		t.Error("the bad file must be removed")
	}
}

func TestFirmwareStickProblem(t *testing.T) {
	ok := StickCheck{OK: true, FreeBytes: 200_000_000}
	if p := FirmwareStickProblem(ok, 100_000_000, 0); p != "" {
		t.Errorf("ok stick: %q", p)
	}
	if p := FirmwareStickProblem(StickCheck{Reason: "not-fat32"}, 1, 0); p != "not-fat32" {
		t.Errorf("not fat32: %q", p)
	}
	c := ok
	c.ClusterTooBig = true
	if p := FirmwareStickProblem(c, 1, 0); p != "cluster-too-big" {
		t.Errorf("cluster: %q", p)
	}
	small := StickCheck{OK: true, FreeBytes: 105_000_000}
	if p := FirmwareStickProblem(small, 100_000_000, 0); p != "no-space" {
		t.Errorf("space: %q", p)
	}
	// The old Update.stu is replaced, so its bytes count as free.
	if p := FirmwareStickProblem(small, 100_000_000, 90_000_000); p != "" {
		t.Errorf("space with an old file: %q", p)
	}
	tooLarge := ok
	tooLarge.TooLarge = true
	if p := FirmwareStickProblem(tooLarge, 1, 0); p != "" {
		t.Errorf("over 32 GB is a warning only: %q", p)
	}
}

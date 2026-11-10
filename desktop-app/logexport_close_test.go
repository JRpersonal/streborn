package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeZipFile stands in for the bundle's *os.File.
type fakeZipFile struct {
	*os.File
	statErr, closeErr error
}

func (f fakeZipFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.File.Stat()
}

func (f fakeZipFile) Close() error {
	err := f.File.Close()
	if f.closeErr != nil {
		return f.closeErr
	}
	return err
}

func newZipTestFile(t *testing.T) (*os.File, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bundle.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.WriteString("twelve bytes"); err != nil {
		t.Fatal(err)
	}
	return f, p
}

func TestCloseZipFileReportsTheSize(t *testing.T) {
	f, p := newZipTestFile(t)
	size, err := closeZipFile(f, p)
	if err != nil || size != 12 {
		t.Fatalf("size = %d err = %v, want 12", size, err)
	}
}

// A close that fails is the last bytes not reaching the disk: the export must
// not report a saved bundle.
func TestCloseZipFileFailsWhenTheCloseFails(t *testing.T) {
	f, p := newZipTestFile(t)
	full := errors.New("no space left on device")
	_, err := closeZipFile(fakeZipFile{File: f, closeErr: full}, p)
	if !errors.Is(err, full) {
		t.Fatalf("err = %v, want the close error", err)
	}
}

// A Stat that fails used to be a nil dereference on the very next line.
func TestCloseZipFileSurvivesAFailedStat(t *testing.T) {
	f, p := newZipTestFile(t)
	size, err := closeZipFile(fakeZipFile{File: f, statErr: errors.New("stat refused")}, p)
	if err != nil || size != 12 {
		t.Fatalf("size = %d err = %v, want the size read from the path", size, err)
	}
	f2, p2 := newZipTestFile(t)
	if _, err := closeZipFile(fakeZipFile{File: f2, statErr: errors.New("stat refused")}, p2+".gone"); err == nil {
		t.Fatal("a bundle that cannot be read back at all is not a saved bundle")
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// crcDownload builds a download of url into dst that expects payload.
func crcDownload(url, dst string, payload []byte) verifiedDownload {
	crc := crc32.NewIEEE()
	want := crc32.ChecksumIEEE(payload)
	return verifiedDownload{
		url: url, dst: dst, size: int64(len(payload)), source: "the test host",
		headerWait: 10 * time.Second, stallAfter: 10 * time.Second,
		sum: crc,
		alreadyThere: func() bool {
			return verifyFile(dst, fileFingerprint{size: int64(len(payload)), crc: want, checkCRC: true}) == nil
		},
		check: func(n int64) error {
			if n != int64(len(payload)) || crc.Sum32() != want {
				return fmt.Errorf("%w: %d bytes", errFingerprint, n)
			}
			return nil
		},
	}
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestFetchVerifiedDownloadsChecksAndRenames(t *testing.T) {
	payload := bytes.Repeat([]byte("firmware"), 20000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer srv.Close()
	dir := t.TempDir()
	dst := filepath.Join(dir, "Update.stu")
	d := crcDownload(srv.URL, dst, payload)
	var last int64
	d.onProgress = func(n int64) { last = n }
	n, err := fetchVerified(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) || last != n {
		t.Errorf("n = %d, progress ended at %d, want %d", n, last, len(payload))
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, payload) {
		t.Fatal("the file at dst is not the download")
	}
	if left := leftovers(t, dir); len(left) != 1 {
		t.Errorf("only the finished file may stay, found %v", left)
	}
}

func TestFetchVerifiedLeavesNothingAfterAFailedCheck(t *testing.T) {
	payload := []byte("not the expected image")
	// One byte too long as well: the reader stops one past the expected size.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(append(append([]byte{}, payload...), "and a lot more than was asked for"...))
	}))
	defer srv.Close()
	dir := t.TempDir()
	n, err := fetchVerified(context.Background(), crcDownload(srv.URL, filepath.Join(dir, "x.stu"), payload))
	if !errors.Is(err, errFingerprint) {
		t.Fatalf("want the check's own error, got %v", err)
	}
	if n != int64(len(payload))+1 {
		t.Errorf("read %d bytes, want exactly one more than the expected %d", n, len(payload))
	}
	if left := leftovers(t, dir); len(left) != 0 {
		t.Errorf("a failed download must leave nothing behind, found %v", left)
	}
}

func TestFetchVerifiedNamesTheSourceOnAnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	dir := t.TempDir()
	_, err := fetchVerified(context.Background(), crcDownload(srv.URL, filepath.Join(dir, "x.stu"), []byte("x")))
	if err == nil || err.Error() != "download from the test host: HTTP 404" {
		t.Fatalf("got %v", err)
	}
	if left := leftovers(t, dir); len(left) != 0 {
		t.Errorf("found %v", left)
	}
}

// Two speakers of the same model flashed at the same time fetch the same
// file. With one shared "<name>.part" both runs wrote into the same file and
// the second rename found it gone.
func TestFetchVerifiedTwoRunsOfTheSameFileDoNotCollide(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 40000)
	const runs = 2
	var arrived sync.WaitGroup
	arrived.Add(runs)
	bothIn := make(chan struct{})
	go func() { arrived.Wait(); close(bothIn) }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Half the body, then wait until every run is mid-download.
		_, _ = w.Write(payload[:len(payload)/2])
		w.(http.Flusher).Flush()
		arrived.Done()
		select {
		case <-bothIn:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write(payload[len(payload)/2:])
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "Update.stu")
	errs := make([]error, runs)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = fetchVerified(context.Background(), crcDownload(srv.URL, dst, payload))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d: %v", i, err)
		}
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, payload) {
		t.Fatal("the file at dst is not the download")
	}
	if left := leftovers(t, filepath.Dir(dst)); len(left) != 1 {
		t.Errorf("only the finished file may stay, found %v", left)
	}
}

func TestRemoveStalePartsKeepsARunningDownload(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "Update.stu")
	old := time.Now().Add(-2 * stalePartAge)
	write := func(name string, mod time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	staleLegacy := write("Update.stu.part", old)
	stale := write("Update.stu.123.part", old)
	running := write("Update.stu.456.part", time.Now())
	other := write("Other.stu.789.part", old)
	removeStaleParts(dst, time.Now())
	for _, p := range []string{staleLegacy, stale} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s is a dead download and should be gone", filepath.Base(p))
		}
	}
	for _, p := range []string{running, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must stay: %v", filepath.Base(p), err)
		}
	}
}

func TestVerifyFile(t *testing.T) {
	payload := []byte("an update file")
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	size, crc, sum := int64(len(payload)), crc32.ChecksumIEEE(payload), sha1hex(payload)
	good := []fileFingerprint{
		{size: size, crc: crc, checkCRC: true},
		{size: size, crc: crc, checkCRC: true, sha1: strings.ToUpper(sum)},
		{size: size, sha1: sum},
	}
	for _, fp := range good {
		if err := verifyFile(p, fp); err != nil {
			t.Errorf("%+v: %v", fp, err)
		}
	}
	bad := []fileFingerprint{
		{size: size + 1, crc: crc, checkCRC: true},
		{size: size, crc: crc ^ 1, checkCRC: true},
		{size: size, crc: crc, checkCRC: true, sha1: strings.Repeat("0", 40)},
		{size: size, sha1: strings.Repeat("0", 40)},
		{size: size}, // no checksum known: a size alone proves nothing
	}
	for _, fp := range bad {
		if err := verifyFile(p, fp); !errors.Is(err, errFingerprint) {
			t.Errorf("%+v: want a fingerprint refusal, got %v", fp, err)
		}
	}
	if err := verifyFile(p+".missing", good[0]); err == nil || errors.Is(err, errFingerprint) {
		t.Errorf("a missing file is not a fingerprint mismatch, got %v", err)
	}
}

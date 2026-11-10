package main

// One download-and-verify path for the firmware files the app fetches: Bose's
// own Update_*.stu (app_fwflash.go) and the Internet Archive zips of the
// firmware ladder (app_fwladder.go). Both used to carry their own copy of the
// same fifty lines, and three near-identical "does the cached file match"
// checks beside them.

import (
	"context"
	"crypto/sha1" // #nosec G505 -- integrity check against a published sha1, not a security boundary
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stalePartAge is how long an unfinished download may sit untouched before the
// next download of the same file removes it. A running download writes to its
// part file all the time, and the stall watchdog ends it after at most 90 s
// without a byte, so a part file this old belongs to a run that is gone.
const stalePartAge = 15 * time.Minute

// verifiedDownload describes one file to fetch and check.
type verifiedDownload struct {
	url string
	dst string // final path; the download lands beside it and is renamed in
	// size is the expected length. One byte more is read at most, so a file
	// that is too long shows up as a mismatch instead of filling the disk.
	size int64
	// source names where the file comes from, in error texts ("Bose", "the
	// Internet Archive").
	source string
	// headerWait bounds the wait for the response headers, stallAfter the
	// time without a single byte of body.
	headerWait time.Duration
	stallAfter time.Duration
	// checkRedirect is the http.Client's redirect policy; nil keeps the default.
	checkRedirect func(req *http.Request, via []*http.Request) error
	// sum receives every downloaded byte (the caller's hash).
	sum io.Writer
	// check is called with the byte count once the body is complete, and
	// decides from it and the caller's hash whether the file is the expected
	// one. Its error is returned as is.
	check func(n int64) error
	// alreadyThere reports whether d.dst already holds the expected file. Asked
	// only when the finished download cannot be moved into place.
	alreadyThere func() bool
	onProgress   func(int64)
}

// fetchVerified downloads d.url, checks it and moves it to d.dst. Nothing is
// left at d.dst unless the check passed. Each run writes to its own part file,
// so two downloads of the same file cannot write into each other.
func fetchVerified(ctx context.Context, d verifiedDownload) (int64, error) {
	removeStaleParts(d.dst, time.Now())
	out, err := os.CreateTemp(filepath.Dir(d.dst), filepath.Base(d.dst)+".*.part")
	if err != nil {
		return 0, err
	}
	part := out.Name()
	ok := false
	defer func() {
		if !ok {
			_ = out.Close()
			_ = os.Remove(part)
		}
	}()
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, d.url, nil)
	if err != nil {
		return 0, err
	}
	// The transport lives for this one download: close its connections with
	// it, or each download leaves an idle connection and its goroutines behind.
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: d.headerWait,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: d.checkRedirect}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download from %s: %w", d.source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download from %s: HTTP %d", d.source, resp.StatusCode)
	}
	body, beat := beatingReader(io.LimitReader(resp.Body, d.size+1))
	go watchStall(dctx, cancel, beat, d.stallAfter)
	n, err := io.Copy(io.MultiWriter(out, d.sum), &countingReader{r: body, onProgress: d.onProgress})
	if err != nil {
		return n, fmt.Errorf("download from %s stopped after %d of %d bytes: %w", d.source, n, d.size, err)
	}
	if err := d.check(n); err != nil {
		return n, err
	}
	if err := out.Close(); err != nil {
		return n, err
	}
	if err := placeDownload(part, d.dst, d.alreadyThere); err != nil {
		return n, err
	}
	ok = true
	return n, nil
}

// placeDownload renames a checked part file to dst. When a second run of the
// same download got there first, the rename can fail: on Windows it is refused
// while the other run's rename is under way, and for as long as the file the
// other run placed is open (it is, for the minutes an upload to a speaker
// reads it). The file at dst is then the same checked file, so this run
// drops its copy and counts as done.
func placeDownload(part, dst string, alreadyThere func() bool) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		if err = os.Rename(part, dst); err == nil {
			return nil
		}
		if alreadyThere != nil && alreadyThere() {
			_ = os.Remove(part)
			return nil
		}
	}
	return err
}

// removeStaleParts deletes part files of dst that no running download can
// still own: a crash or a killed app leaves one behind, and a firmware file is
// around 100 MB. Covers the fixed "<name>.part" of earlier versions too.
func removeStaleParts(dst string, now time.Time) {
	names, _ := filepath.Glob(dst + ".*.part")
	names = append(names, dst+".part")
	for _, p := range names {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || now.Sub(st.ModTime()) < stalePartAge {
			continue
		}
		_ = os.Remove(p)
	}
}

// fileFingerprint is what a firmware file must match: its size, and whichever
// of the two checksums is known for it.
type fileFingerprint struct {
	size     int64
	crc      uint32
	checkCRC bool
	sha1     string // hex; empty when not known
}

// verifyFile reports whether the file at p matches want. A mismatch wraps
// errFingerprint.
func verifyFile(p string, want fileFingerprint) error {
	fh, err := os.Open(p)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	if st.Size() != want.size {
		return fmt.Errorf("%w: size %d, expected %d", errFingerprint, st.Size(), want.size)
	}
	c := crc32.NewIEEE()
	s := sha1.New() // #nosec G401 -- integrity check
	if _, err := io.Copy(io.MultiWriter(c, s), fh); err != nil {
		return err
	}
	if want.checkCRC {
		return matchInner(want.size, want.size, c, s, want.crc, want.sha1)
	}
	if got := hex.EncodeToString(s.Sum(nil)); !strings.EqualFold(got, want.sha1) {
		return fmt.Errorf("%w: sha1 %s, expected %s", errFingerprint, got, want.sha1)
	}
	return nil
}

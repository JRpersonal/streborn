package main

// Bose firmware update from the app: the one-click form of the firmware update
// guide (frontend/src/fwguide.js).
//
// The route is Bose's own. Every SoundTouch serves an update page on
// :17008/update.html that takes Bose's Update_*.stu file and flashes it; Bose's
// support articles send the owner there through the speaker's setup network.
// The page answers on the home network too, so the app can do the whole thing
// for a speaker it already sees: download Bose's file for this model from Bose's
// host, check it against the size and CRC in Bose's catalogue, post it to the
// page, and wait for the speaker to come back on the new version. Verified
// 27.0.3 -> 27.0.6 on a stock Portable on 2026-10-13.
//
// On a speaker that runs STR, :17008 is redirected to the agent on the BCO
// chassis, so the file goes to the agent's /api/agent/bose-firmware, which
// hands it on to the update page over loopback. A flash rewrites the firmware
// only: /mnt/nv, and with it STR, came back untouched in the same test.
//
// The file is kept in the user's own cache folder so a second speaker of the
// same model does not download it again. It never leaves that machine: STR does
// not mirror or redistribute a Bose image.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"streborn-app/bosefw"
)

// FirmwareFlashResult is what the update screen shows when the flash is over.
type FirmwareFlashResult struct {
	Before string `json:"before"` // short version before, e.g. "27.0.3"
	After  string `json:"after"`  // short version the speaker reports afterwards
	Via    string `json:"via"`    // "agent" or "page"
}

// Phases of a flash, in order, as sent in "firmware:progress" events.
const (
	fwPhaseDownload = "download"
	fwPhaseVerify   = "verify"
	fwPhaseUpload   = "upload"
	fwPhaseInstall  = "install" // file is in, the speaker flashes and restarts
	fwPhaseDone     = "done"
)

// Timings of the wait after the upload. The Portable took about 50 s from
// COMPLETED to answering again on stock, about 90 s with STR.
var (
	fwRestartDownWait = 6 * time.Minute // until the speaker goes away to restart
	fwRestartUpWait   = 6 * time.Minute // until it answers again
	fwPollEvery       = 3 * time.Second
)

// fwUploadReplyWait bounds how long the update page may take to answer once
// the whole file is in. The page sometimes closes the connection without an
// answer instead; with every byte delivered that is not a failure.
var fwUploadReplyWait = 120 * time.Second

// fwDownloadPrefix is the only place a firmware file is fetched from; a var
// for tests.
var fwDownloadPrefix = "https://downloads.bose.com/"

// fwCacheDir is where downloaded Bose files are kept; a var for tests.
var fwCacheDir = func() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "streborn", "bosefw"), nil
}

// FlashBoseFirmware brings a speaker on the LAN to Bose's last firmware.
// force also reflashes a speaker that is already on it (used for testing; the
// app offers the update only to an outdated speaker).
func (a *App) FlashBoseFirmware(host string, force bool) (FirmwareFlashResult, error) {
	res, err := a.flashBoseFirmware(a.appCtx(), host, force)
	if err != nil {
		a.fwLog().Warn("bose firmware: update failed", "host", host, "err", err)
	} else {
		a.fwLog().Info("bose firmware: update done", "host", host, "before", res.Before, "after", res.After, "via", res.Via)
	}
	return res, err
}

func (a *App) flashBoseFirmware(ctx context.Context, host string, force bool) (FirmwareFlashResult, error) {
	var res FirmwareFlashResult
	host = strings.TrimSpace(host)
	if host == "" {
		return res, fmt.Errorf("host is required")
	}
	fi, err := a.GetBoxFirmware(host)
	if err != nil {
		return res, fmt.Errorf("the speaker does not answer: %w", err)
	}
	if !fi.Reachable {
		return res, fmt.Errorf("the speaker does not answer")
	}
	res.Before = fi.Short
	if !fi.Outdated && !force {
		return res, fmt.Errorf("the speaker already runs Bose's last firmware %s", latestBoseFirmware)
	}
	entries, _ := boseCatalogue.Entries(ctx)
	files := bosefw.Match(entries, fi.Model, fi.ModuleType, fi.Variant)
	if len(files) != 1 {
		return res, fmt.Errorf("no single Bose file matches this speaker (%s, %s, %s): use the steps in the guide", fi.Model, fi.ModuleType, fi.Variant)
	}
	file := files[0]
	if file.Length <= 0 || file.CRC == 0 {
		return res, fmt.Errorf("the catalogue from Bose gives no size and checksum for %s, so the app cannot check the download", file.Name)
	}
	a.fwLog().Info("bose firmware: update started", "host", host, "model", fi.Model, "moduleType", fi.ModuleType,
		"variant", fi.Variant, "from", fi.Firmware, "file", file.Name, "force", force)

	path, err := a.fetchBoseFile(ctx, host, file)
	if err != nil {
		return res, err
	}

	target, via, err := a.firmwareUploadTarget(ctx, host)
	if err != nil {
		return res, err
	}
	res.Via = via
	a.fwLog().Info("bose firmware: uploading", "host", host, "via", via)
	if err := a.uploadBoseFile(ctx, host, target, path, file.Name); err != nil {
		return res, err
	}

	a.emitFirmwareProgress(host, fwPhaseInstall, 0, 0)
	after, err := a.waitFirmwareRestart(ctx, host)
	if err != nil {
		return res, err
	}
	res.After = after
	a.emitFirmwareProgress(host, fwPhaseDone, 0, 0)
	return res, nil
}

// emitFirmwareProgress sends one "firmware:progress" event. Byte progress
// inside a phase goes through transferProgress with the same event name.
func (a *App) emitFirmwareProgress(host, phase string, done, total int64) {
	if a == nil || a.ctx == nil {
		return
	}
	wailsrt.EventsEmit(a.appCtx(), "firmware:progress", map[string]any{
		"host": host, "phase": phase, "pct": -1, "done": done, "total": total,
	})
}

// phaseProgress is a transferProgress that tags its events with a phase.
type phaseProgress struct {
	app   *App
	host  string
	phase string
	tp    *transferProgress
}

func (a *App) newPhaseProgress(host, phase string, total int64) *phaseProgress {
	return &phaseProgress{app: a, host: host, phase: phase, tp: newTransferProgress(nil, "", total, host)}
}

func (p *phaseProgress) report(done int64) {
	tp := p.tp
	now := time.Now()
	pct := -1
	if tp.total > 0 {
		pct = int(done * 100 / tp.total)
	}
	if pct == tp.lastPct && now.Sub(tp.lastEmit) < 250*time.Millisecond {
		return
	}
	tp.lastEmit, tp.lastPct = now, pct
	if p.app == nil || p.app.ctx == nil {
		return
	}
	var bps int64
	if el := now.Sub(tp.start).Seconds(); el > 0 {
		bps = int64(float64(done) / el)
	}
	wailsrt.EventsEmit(p.app.appCtx(), "firmware:progress", map[string]any{
		"host": p.host, "phase": p.phase, "pct": pct, "bytesPerSec": bps, "done": done, "total": tp.total,
	})
}

// fetchBoseFile returns the local path of Bose's file, downloading it when the
// cache does not already hold a copy that matches the catalogue.
func (a *App) fetchBoseFile(ctx context.Context, host string, f bosefw.File) (string, error) {
	if !strings.HasPrefix(f.URL, fwDownloadPrefix) || strings.ContainsAny(f.Name, `/\`) {
		return "", fmt.Errorf("refusing a firmware link outside Bose's download host")
	}
	dir, err := fwCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, f.Name)
	a.emitFirmwareProgress(host, fwPhaseVerify, 0, f.Length)
	if checkBoseFile(path, f.Length, f.CRC) == nil {
		a.fwLog().Info("bose firmware: using the copy already downloaded", "file", f.Name)
		return path, nil
	}

	part := path + ".part"
	_ = os.Remove(part)
	out, err := os.Create(part)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = out.Close()
			_ = os.Remove(part)
		}
	}()
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download from Bose: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download from Bose: HTTP %d", resp.StatusCode)
	}
	prog := a.newPhaseProgress(host, fwPhaseDownload, f.Length)
	body, beat := beatingReader(io.LimitReader(resp.Body, f.Length+1))
	go watchStall(dctx, cancel, beat, 60*time.Second)
	crc := crc32.NewIEEE()
	n, err := io.Copy(io.MultiWriter(out, crc), &countingReader{r: body, onProgress: prog.report})
	if err != nil {
		return "", fmt.Errorf("download from Bose stopped after %d of %d bytes: %w", n, f.Length, err)
	}
	if n != f.Length || crc.Sum32() != f.CRC {
		return "", fmt.Errorf("the downloaded file does not match Bose's catalogue (%d bytes, CRC %08x; expected %d bytes, CRC %08x)", n, crc.Sum32(), f.Length, f.CRC)
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(part, path); err != nil {
		return "", err
	}
	ok = true
	a.fwLog().Info("bose firmware: downloaded and checked", "file", f.Name, "bytes", n)
	return path, nil
}

// checkBoseFile reports whether the file at path has exactly the catalogue's
// size and CRC-32.
func checkBoseFile(path string, length int64, crc uint32) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	if st.Size() != length {
		return fmt.Errorf("size %d, expected %d", st.Size(), length)
	}
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, fh); err != nil {
		return err
	}
	if h.Sum32() != crc {
		return fmt.Errorf("CRC %08x, expected %08x", h.Sum32(), crc)
	}
	return nil
}

// firmwareUploadTarget picks where the file goes: the agent's hand-over
// endpoint when STR runs on the speaker and the update page answers behind it,
// otherwise Bose's update page itself.
func (a *App) firmwareUploadTarget(ctx context.Context, host string) (url, via string, err error) {
	if resp, err := a.boxDoTimeout(host, 8888, http.MethodGet, "/api/agent/bose-firmware", "", "", 8*time.Second); err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && strings.Contains(string(b), `"available":true`) {
			return a.baseURL(host, 8888) + "/api/agent/bose-firmware", "agent", nil
		}
		if resp.StatusCode == http.StatusOK && strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
			return "", "", fmt.Errorf("STR answers on the speaker, but Bose's update page behind it does not")
		}
		if resp.StatusCode == http.StatusOK {
			// An agent from before this endpoint serves its web UI for any
			// unknown path, and its port 17008 is not Bose's page any more.
			return "", "", fmt.Errorf("STR on this speaker is too old for the firmware update from the app: update STR first")
		}
	}
	page := fmt.Sprintf("http://%s:17008/update.html", host)
	if updatePageAnswers(ctx, page) {
		return page, "page", nil
	}
	return "", "", fmt.Errorf("the speaker's update page does not answer; if STR is installed on it, update STR first")
}

// updatePageAnswers reports whether url serves Bose's update page.
func updatePageAnswers(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == http.StatusOK && strings.Contains(string(b), "Update")
}

// uploadBoseFile posts the file the way the update page's own form does:
// multipart/form-data, field "filename", Bose's file name unchanged (the page
// reads the version from it). The body is streamed with an exact length so the
// agent can pass it on without holding it.
func (a *App) uploadBoseFile(ctx context.Context, host, url, path, name string) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	head, tail, ctype := multipartFrame("filename", name)
	total := int64(len(head)) + st.Size() + int64(len(tail))

	uctx, cancel := context.WithCancel(ctx)
	defer cancel()
	prog := a.newPhaseProgress(host, fwPhaseUpload, total)
	var sent atomic.Int64
	body := io.MultiReader(bytes.NewReader(head), fh, bytes.NewReader(tail))
	counted := &countingReader{r: body, onProgress: func(n int64) {
		sent.Store(n)
		prog.report(n)
	}}
	// Same trickle guard as the agent upload (streamPostBinary): every 30 s the
	// count must have grown by a real amount while the body is still going.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		last := int64(0)
		for {
			select {
			case <-uctx.Done():
				return
			case <-t.C:
				cur := sent.Load()
				if cur >= total {
					return
				}
				if cur-last < 64*1024 {
					cancel()
					return
				}
				last = cur
			}
		}
	}()
	req, err := http.NewRequestWithContext(uctx, http.MethodPost, url, counted)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	req.ContentLength = total
	client := &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ResponseHeaderTimeout: fwUploadReplyWait,
	}}
	resp, err := client.Do(req)
	if err != nil {
		if sent.Load() >= total && ctx.Err() == nil && uploadReplyLost(err) {
			// Every byte is in; the page dropped or kept the connection without
			// answering. The restart wait decides whether it took the file.
			a.fwLog().Info("bose firmware: file sent, the update page did not answer", "host", host, "err", err)
			return nil
		}
		return fmt.Errorf("upload to the speaker stopped after %d of %d bytes: %w", sent.Load(), total, err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the speaker refused the file (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(reply)))
	}
	a.fwLog().Info("bose firmware: file accepted", "host", host, "status", resp.StatusCode, "bytes", total)
	return nil
}

// uploadReplyLost reports whether err is the update page going quiet or
// closing after the body, as opposed to the upload itself failing.
func uploadReplyLost(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection reset") || strings.Contains(s, "EOF") ||
		strings.Contains(s, "timeout awaiting response headers") || strings.Contains(s, "forcibly closed")
}

// multipartFrame builds the multipart head and tail around one file field.
func multipartFrame(field, name string) (head, tail []byte, contentType string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, name))
	h.Set("Content-Type", "application/octet-stream")
	_, _ = mw.CreatePart(h)
	head = append([]byte(nil), buf.Bytes()...)
	buf.Reset()
	_ = mw.Close()
	tail = append([]byte(nil), buf.Bytes()...)
	return head, tail, mw.FormDataContentType()
}

// waitFirmwareRestart waits for the speaker to go away to flash and come back,
// and returns the version it then reports.
func (a *App) waitFirmwareRestart(ctx context.Context, host string) (string, error) {
	// Two misses in a row count as gone: one slow answer from a busy speaker
	// must not be read as the restart.
	down := time.Now().Add(fwRestartDownWait)
	misses := 0
	for {
		if _, err := a.GetBoxFirmware(host); err != nil {
			misses++
			if misses >= 2 {
				break
			}
		} else {
			misses = 0
		}
		if time.Now().After(down) {
			return "", fmt.Errorf("the speaker took the file but did not restart; leave it powered for a few minutes and check its version")
		}
		if err := sleepCtx(ctx, fwPollEvery); err != nil {
			return "", err
		}
	}
	a.fwLog().Info("bose firmware: speaker is restarting", "host", host)
	up := time.Now().Add(fwRestartUpWait)
	for {
		// Right after the restart :8090/info answers before it lists the
		// components, so an answer without a version is not "back" yet.
		if fi, err := a.GetBoxFirmware(host); err == nil && fi.Reachable && fi.Short != "" {
			return fi.Short, nil
		}
		if time.Now().After(up) {
			return "", fmt.Errorf("the speaker has not come back after its restart yet; leave it powered, it may still be installing")
		}
		if err := sleepCtx(ctx, fwPollEvery); err != nil {
			return "", err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// fwLog is the app logger, or slog's default before startup and in tests.
func (a *App) fwLog() *slog.Logger {
	if a != nil && a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

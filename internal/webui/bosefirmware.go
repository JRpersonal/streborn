package webui

// Bose firmware updates on a speaker that runs STR.
//
// Every SoundTouch has Bose's own update page on :17008/update.html: a form
// that takes Bose's Update_*.stu file and flashes it. On a stock speaker the
// app posts the file there directly. On a speaker with STR, port 17008 is
// REDIRECTed to the agent on the BCO chassis (Portable, ST20, ST30, Wave), and
// run.sh's watchdog puts that rule back every 30 s, so the page cannot be
// reached from the LAN. The rule skips loopback (`! -i lo`), so the agent
// hands the upload on to 127.0.0.1:17008 itself. The body is streamed, never
// held: the image is about 100 MB on a speaker with 120 MB of RAM.
//
// Verified on a Portable with STR on 2026-10-13: the flash rewrites the
// firmware only. /mnt/nv, and with it STR, its settings and the Spotify
// engine, came back untouched, and the agent started on its own.

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// boseUpdatePageURL is Bose's update page as the speaker itself reaches it.
var boseUpdatePageURL = "http://127.0.0.1:17008/update.html"

// boseFirmwareUploadBudget bounds one hand-over. An upload over Wi-Fi took
// 5 min 19 s for 106 MB on a Portable; the largest image is about twice that.
const boseFirmwareUploadBudget = 30 * time.Minute

// boseFirmwareMaxBytes is well above the largest image in Bose's catalogue
// (about 115 MB).
const boseFirmwareMaxBytes = 160 << 20

// handleBoseFirmware: GET says whether Bose's update page answers on the
// speaker ({"available": bool}); POST hands a multipart upload (the page's own
// form: field "filename", Bose's file name) on to it unchanged and answers
// with the page's status. The speaker restarts on its own once the image is
// in.
func (s *Server) handleBoseFirmware(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		s.refuseNonLAN(w, r, "bose-firmware")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"available": boseUpdatePageAnswers(r.Context())})
		return
	}
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		http.Error(w, "multipart/form-data expected", http.StatusBadRequest)
		return
	}
	// Bose's images are 95 to 115 MB. A body without a length cannot be passed
	// on unbuffered, and anything far larger is not a Bose image.
	if r.ContentLength <= 0 || r.ContentLength > boseFirmwareMaxBytes {
		http.Error(w, "a Bose update file with a known length is expected", http.StatusRequestEntityTooLarge)
		return
	}
	// Same preparation as an agent update: STR's own playback stops and a
	// speaker in standby lifts wireless power-save for the transfer.
	defer s.boostUploadThroughput("bose-firmware")()
	s.hushForUpload("bose-firmware")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), boseFirmwareUploadBudget)
	defer cancel()
	counted := &countingReader{r: r.Body}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, boseUpdatePageURL, counted)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", ct)
	req.ContentLength = r.ContentLength
	start := time.Now()
	s.logger.Info("bose firmware: handing an update file on to the speaker's own update page", "bytes", r.ContentLength)
	resp, err := (&http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 2 * time.Minute}}).Do(req)
	if err != nil && counted.n >= r.ContentLength {
		// Every byte reached the page and it hung up or stayed silent
		// instead of answering. The desktop app judges the result by the
		// restart that follows, so this is reported as delivered.
		s.logger.Info("bose firmware: update file handed over, the page closed without an answer",
			"sent", counted.n, "sec", int(time.Since(start).Seconds()), "err", err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "delivered, no answer from the update page")
		return
	}
	if err != nil {
		s.logger.Warn("bose firmware: the hand-over failed", "sent", counted.n, "of", r.ContentLength, "sec", int(time.Since(start).Seconds()), "err", err)
		http.Error(w, "update page: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	s.logger.Info("bose firmware: update file handed over, the speaker flashes and restarts on its own",
		"sent", counted.n, "status", resp.StatusCode, "sec", int(time.Since(start).Seconds()))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// boseUpdatePageAnswers reports whether Bose's update page is up on the
// speaker.
func boseUpdatePageAnswers(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, boseUpdatePageURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return resp.StatusCode == http.StatusOK && strings.Contains(string(b), "Update")
}

// countingReader counts what passed through, for the log.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

package main

// Shared transfer-progress plumbing for the two large transfers in the app: the
// app self-update DOWNLOAD (from GitHub) and the speaker-update UPLOAD (the ~10 MB
// agent to the box). Both emit a percentage AND a live throughput so the user can
// see the transfer is moving rather than frozen, which is the exact uncertainty
// that made users abandon a slow update and report it as broken.

import (
	"context"
	"io"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

// transferProgress throttles transfer-progress events to the frontend (at most
// ~4/s) and computes a live throughput in bytes/sec. The frontend formats the
// rate into KB/s or MB/s. total <= 0 means unknown size (pct stays -1).
type transferProgress struct {
	app      *App
	event    string // wails event name, e.g. "app:update:progress"
	host     string // box host this transfer targets ("" for the app self-update)
	total    int64
	start    time.Time
	lastEmit time.Time
	lastPct  int
}

// newTransferProgress builds a progress emitter. host identifies the target
// speaker so a multi-box "update all" overlay can route each event to the right
// row; pass "" for the app self-update (no box). The legacy single-box listener
// ignores the host field, so this is backward compatible.
func newTransferProgress(app *App, event string, total int64, host string) *transferProgress {
	now := time.Now()
	return &transferProgress{
		app:      app,
		event:    event,
		host:     host,
		total:    total,
		start:    now,
		lastEmit: now.Add(-time.Second), // so the first report always emits
		lastPct:  -1,
	}
}

// report emits a progress event for the cumulative bytes transferred so far,
// throttled to a quarter second unless the whole-number percent changed.
func (p *transferProgress) report(done int64) {
	if p == nil || p.app == nil {
		return
	}
	now := time.Now()
	pct := -1
	if p.total > 0 {
		pct = int(done * 100 / p.total)
	}
	if pct == p.lastPct && now.Sub(p.lastEmit) < 250*time.Millisecond {
		return
	}
	p.lastEmit = now
	p.lastPct = pct
	var bps int64
	if el := now.Sub(p.start).Seconds(); el > 0 {
		bps = int64(float64(done) / el)
	}
	wailsrt.EventsEmit(p.app.appCtx(), p.event, map[string]any{
		"host": p.host, "pct": pct, "bytesPerSec": bps, "done": done, "total": p.total,
	})
}

// countingReader wraps r and reports the cumulative bytes read after each Read.
// Used to add UPLOAD progress to an http request body: net/http reads the body as
// it streams it to the server, so the count tracks bytes actually sent.
type countingReader struct {
	r          io.Reader
	n          int64
	onProgress func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += int64(n)
		if c.onProgress != nil {
			c.onProgress(c.n)
		}
	}
	return n, err
}

// watchStall cancels via cancel() when no heartbeat arrives on beat within idle,
// turning a frozen transfer (connection alive but no bytes) into a prompt error
// the caller can retry, instead of waiting out a long overall deadline. Returns
// when ctx is done. Each chunk of progress should send on beat (non-blocking).
func watchStall(ctx context.Context, cancel context.CancelFunc, beat <-chan struct{}, idle time.Duration) {
	t := time.NewTimer(idle)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			t.Reset(idle)
		case <-t.C:
			cancel()
			return
		}
	}
}

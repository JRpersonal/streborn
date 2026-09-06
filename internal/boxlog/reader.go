package boxlog

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// logreadPath is busybox logread on every SoundTouch chassis.
	logreadPath = "/sbin/logread"
	// liveWindow is how recently a line must have arrived for the reader to
	// count as healthy. The ring receives the firmware's own chatter many times
	// a second, so a silent minute means the child or syslogd is gone.
	liveWindow = 90 * time.Second
	// maxBackoff caps the restart delay after the child exits.
	maxBackoff = 60 * time.Second
	// dedupWindow is how long a press from one origin masks the same press
	// reported by the other origin (the trace line and the analytics line
	// describe the same physical press on chassis that emit both).
	dedupWindow = 1500 * time.Millisecond
	// reassertGap rate-limits re-sending the loglevel commands when a press
	// arrives without the DEBUG trace, so a chassis that never emits the trace
	// (rhino) costs two TAP commands per ten minutes at most.
	reassertGap = 10 * time.Minute
	// historyLen is how many recent events the debug section keeps.
	historyLen = 24
)

// traceCommands raise the two key facilities to DEBUG (level 4). The setting
// lives in BoseApp's memory only and is gone after a reboot, which is why the
// reader re-sends it every time it (re)starts.
var traceCommands = []string{
	"loglevel IrDevice on 4",
	"loglevel ConsoleButtons on 4",
}

// traceRetryDelays paces the startup attempts. The agent starts before BoseApp
// has registered its commands with the TAP server, which then answers
// "Command not found" (seen live on the Portable at boot+60 s, 2026-09-06), so
// the first attempt usually fails. The delays add up to about ten minutes, then
// the reader stops trying: the analytics line and the gabbo fallback still
// work without the DEBUG trace, and a later press re-asserts once via
// NeedsReassert. Bounded on purpose, no standing timer.
var traceRetryDelays = []time.Duration{
	10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second,
	120 * time.Second, 120 * time.Second, 240 * time.Second,
}

// Handler receives every decoded key event, on the reader's goroutine. It
// must return quickly; anything slow belongs in a goroutine of its own.
type Handler func(KeyEvent)

// Reader follows the syslog ring buffer and dispatches key events.
type Reader struct {
	logger  *slog.Logger
	handler Handler
	// cli sends one TAP command and returns the reply. Injected so tests never
	// dial port 17000.
	cli func(ctx context.Context, cmd string) (string, error)
	// command starts the logread child. Injected for tests.
	command func(ctx context.Context) *exec.Cmd
	// available reports whether logread exists on this host.
	available func() bool
	// retryDelays paces enableTraceWithRetry; shortened by tests.
	retryDelays []time.Duration

	mu           sync.Mutex
	running      bool
	lastLine     time.Time
	lastTrace    time.Time
	lastStats    time.Time
	lastReassert time.Time
	traceOK      bool
	restarts     int
	linesSeen    uint64
	eventsSeen   uint64
	lastPress    map[int]KeyEvent
	history      []KeyEvent
}

// New returns a reader that will call handler for every key event.
func New(logger *slog.Logger, cli func(ctx context.Context, cmd string) (string, error), handler Handler) *Reader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reader{
		logger:  logger,
		handler: handler,
		cli:     cli,
		command: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, logreadPath, "-f")
		},
		available: func() bool {
			_, err := os.Stat(logreadPath)
			return err == nil
		},
		lastPress:   make(map[int]KeyEvent),
		retryDelays: traceRetryDelays,
	}
}

// Run follows the ring buffer until ctx is cancelled, restarting the child
// with backoff when it exits. On a host without logread (a dev build) it logs
// once and returns, so nothing loops.
func (r *Reader) Run(ctx context.Context) {
	if !r.available() {
		r.logger.Info("box log: logread not present on this host, key trace disabled")
		return
	}
	backoff := time.Second
	for {
		go r.enableTraceWithRetry(ctx)
		start := time.Now()
		err := r.follow(ctx)
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		r.restarts++
		r.mu.Unlock()
		// A child that ran for a while earned a fresh, short backoff; one that
		// died at once backs off further each time.
		if time.Since(start) > 5*time.Minute {
			backoff = time.Second
		}
		r.logger.Warn("box log: logread ended, restarting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// enableTraceWithRetry sends the loglevel commands until the speaker accepts
// them or traceRetryDelays is exhausted. Skipped when an earlier round already
// succeeded (a logread restart does not reset BoseApp's level).
func (r *Reader) enableTraceWithRetry(ctx context.Context) {
	r.mu.Lock()
	already := r.traceOK
	r.mu.Unlock()
	if already {
		return
	}
	for i := 0; ; i++ {
		if r.enableTrace(ctx) || i >= len(r.retryDelays) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.retryDelays[i]):
		}
	}
}

// enableTrace sends the loglevel commands once and reports whether the
// speaker accepted both. A refusal is logged, not fatal: the analytics line
// still carries key identity at the default level.
func (r *Reader) enableTrace(ctx context.Context) bool {
	if r.cli == nil {
		return false
	}
	ok := true
	for _, cmd := range traceCommands {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		reply, err := r.cli(cctx, cmd)
		cancel()
		if err != nil || !strings.Contains(reply, "OK") {
			ok = false
			r.logger.Warn("box log: could not raise the key trace level", "cmd", cmd, "err", err, "reply", strings.TrimSpace(reply))
			break
		}
	}
	r.mu.Lock()
	r.traceOK = ok
	r.lastReassert = time.Now()
	r.mu.Unlock()
	if ok {
		r.logger.Info("box log: key trace raised to DEBUG on the speaker (RAM only, re-sent on every start)")
	}
	return ok
}

// follow runs one logread child to completion.
func (r *Reader) follow(ctx context.Context) error {
	cmd := r.command(ctx)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	r.mu.Lock()
	r.running = true
	r.lastLine = time.Now()
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		r.handleLine(sc.Text(), time.Now())
	}
	waitErr := cmd.Wait()
	if scanErr := sc.Err(); scanErr != nil && !errors.Is(scanErr, os.ErrClosed) {
		return scanErr
	}
	return waitErr
}

// handleLine is the per-line hot path: stamp liveness, then only pay for a
// parse when the cheap substring checks inside ParseKeyLine say so.
func (r *Reader) handleLine(line string, now time.Time) {
	r.mu.Lock()
	r.lastLine = now
	r.linesSeen++
	r.mu.Unlock()
	ev, ok := ParseKeyLine(line, now)
	if !ok {
		return
	}
	if !r.accept(ev) {
		return
	}
	if r.handler != nil {
		r.handler(ev)
	}
}

// accept records the event and drops the duplicate a chassis produces when
// both the DEBUG trace and the analytics line describe the same press. It
// also decides when the loglevel needs re-asserting.
func (r *Reader) accept(ev KeyEvent) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventsSeen++
	switch ev.Origin {
	case OriginTrace:
		r.lastTrace = ev.At
	case OriginStats:
		r.lastStats = ev.At
	}
	if ev.State == StatePressed {
		if prev, ok := r.lastPress[ev.Key]; ok && prev.Origin != ev.Origin && ev.At.Sub(prev.At) < dedupWindow {
			return false
		}
		r.lastPress[ev.Key] = ev
	}
	r.history = append(r.history, ev)
	if len(r.history) > historyLen {
		r.history = r.history[len(r.history)-historyLen:]
	}
	return true
}

// Inject records an event as if it had been read from the ring, for tests of
// the consumers.
func (r *Reader) Inject(ev KeyEvent) {
	if r.accept(ev) && r.handler != nil {
		r.handler(ev)
	}
}

// NeedsReassert reports, and claims, one re-send of the loglevel commands:
// the caller saw a press with no DEBUG trace around it on a chassis that had
// the trace before, or never confirmed it. Rate-limited by reassertGap.
func (r *Reader) NeedsReassert(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(r.lastReassert) < reassertGap {
		return false
	}
	if !r.lastStats.IsZero() && now.Sub(r.lastTrace) > dedupWindow && now.Sub(r.lastStats) < dedupWindow {
		r.lastReassert = now
		return true
	}
	return false
}

// Reassert re-sends the loglevel commands; see NeedsReassert.
func (r *Reader) Reassert(ctx context.Context) { r.enableTrace(ctx) }

// Healthy reports whether the child is running and lines still arrive.
func (r *Reader) Healthy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running && time.Since(r.lastLine) < liveWindow
}

// KeyEventWithin reports whether any key event arrived in the last d.
func (r *Reader) KeyEventWithin(d time.Duration) bool {
	_, ok := r.LastKeyEventWithin(d)
	return ok
}

// LastKeyEventWithin returns the newest key event if it arrived in the last d.
func (r *Reader) LastKeyEventWithin(d time.Duration) (KeyEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.history) == 0 {
		return KeyEvent{}, false
	}
	last := r.history[len(r.history)-1]
	if time.Since(last.At) >= d {
		return KeyEvent{}, false
	}
	return last, true
}

// Snapshot is the diagnostic-bundle view: liveness, whether the DEBUG trace
// was accepted, and the last few events so a "the key does nothing" report
// shows what the speaker itself decoded.
func (r *Reader) Snapshot() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := make([]map[string]any, 0, len(r.history))
	for _, e := range r.history {
		events = append(events, map[string]any{
			"at":       e.At.Format(time.RFC3339Nano),
			"key":      e.Name,
			"producer": e.Producer.String(),
			"state":    e.State.String(),
			"origin":   string(e.Origin),
		})
	}
	last := ""
	if !r.lastLine.IsZero() {
		last = r.lastLine.Format(time.RFC3339)
	}
	return map[string]any{
		"running":      r.running,
		"healthy":      r.running && time.Since(r.lastLine) < liveWindow,
		"traceLevelOK": r.traceOK,
		"traceSeen":    !r.lastTrace.IsZero(),
		"statsSeen":    !r.lastStats.IsZero(),
		"lastLine":     last,
		"linesSeen":    r.linesSeen,
		"eventsSeen":   r.eventsSeen,
		"restarts":     r.restarts,
		"recentKeys":   events,
	}
}

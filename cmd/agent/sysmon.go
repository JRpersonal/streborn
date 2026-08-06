// System self-monitoring: clock forensics, resource health logging,
// the memory guard, box info polling, and the boot self-probe.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/discovery"
	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/clocksync"
	"github.com/JRpersonal/streborn/internal/spotify"
)

// clockForensicsState records the boot-time clock verdict. A plug-pulled
// speaker has no battery RTC and boots in 2015; the field capture behind #419
// Finding 4 showed such a boot with EVERY playback dying within 2-13 s for the
// whole boot even though the clock was later corrected - the Bose firmware
// itself stays poisoned until a soft reboot. These markers plus the
// clock_status debug section let a bundle separate the three cases: clock still
// bad (TLS to marge/CDNs failing), clock healed but firmware poisoned, and
// clock fine all along.
var clockForensicsState struct {
	mu                sync.Mutex
	startClock        time.Time
	implausibleAtBoot bool
	correctedAt       time.Time
}

func noteBootClock(logger *slog.Logger) {
	now := time.Now()
	bad := clocksync.Implausible(now)
	clockForensicsState.mu.Lock()
	clockForensicsState.startClock = now
	clockForensicsState.implausibleAtBoot = bad
	clockForensicsState.mu.Unlock()
	if bad {
		logger.Warn("clock forensics: implausible system clock at agent start (no battery RTC; plug-pull boot). TLS to marge and HTTPS radio will fail until the clock syncs, and the Bose firmware can stay broken for the whole boot even after it does (#419 Finding 4)",
			"clock", now.UTC().Format(time.RFC3339), "build", buildStamp)
	}
}

// noteClockHealed runs after RunUntilSynced returns. It marks the moment the
// clock became sane on a boot that started implausible: everything the Bose
// firmware did BEFORE this line ran on the bad clock, which is the correlation
// the dead-playback-until-soft-reboot reports need.
func noteClockHealed(logger *slog.Logger) {
	if clocksync.Implausible(time.Now()) {
		return // ctx canceled before a sync landed; verdict unchanged
	}
	clockForensicsState.mu.Lock()
	wasBad := clockForensicsState.implausibleAtBoot && clockForensicsState.correctedAt.IsZero()
	if wasBad {
		clockForensicsState.correctedAt = time.Now()
	}
	clockForensicsState.mu.Unlock()
	if wasBad {
		logger.Warn("clock forensics: clock corrected AFTER the Bose firmware booted on the bad clock; if playback keeps dying this boot, a soft reboot (not a plug pull) is the known cure (#419 Finding 4)")
	}
}

// clockStatusSnapshot is the clock_status debug/state section.
func clockStatusSnapshot() any {
	clockForensicsState.mu.Lock()
	defer clockForensicsState.mu.Unlock()
	out := map[string]any{
		"now":                        time.Now().UTC().Format(time.RFC3339),
		"agent_start_clock":          clockForensicsState.startClock.UTC().Format(time.RFC3339),
		"implausible_at_agent_start": clockForensicsState.implausibleAtBoot,
		"implausible_now":            clocksync.Implausible(time.Now()),
		"build_stamp":                buildStamp,
	}
	if !clockForensicsState.correctedAt.IsZero() {
		out["corrected_at"] = clockForensicsState.correctedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// pollBoxInfo polls the box /info regularly and keeps the mDNS TXT fields
// for FriendlyName and Model up to date. This way:
//
//  1. The desktop app knows the name as soon as the user renames the box
//     (e.g. via BoseApp HTTP), without a box reboot.
//  2. The model TXT field is promoted to the real value ("SoundTouch 10",
//     etc.) as soon as the Bose firmware serves /info on :8090. On the first
//     announce it still holds the generic fallback "SoundTouch" because :8090
//     typically comes up 20+ seconds after the agent start — the loop here
//     seals that race without blocking the boot.
//
// First round after a short delay, then with a short ticker until the model
// is detected (race recovery), after which the ticker drops back to 30s.
func pollBoxInfo(ctx context.Context, boxHost, region string, ann *discovery.Announcer, logger *slog.Logger) {
	if boxHost == "" || ann == nil {
		return
	}
	time.Sleep(2 * time.Second)
	client := boxapi.New(boxHost)
	var (
		lastName       string
		lastModel      string
		regionLogged   bool
		modelEverFound bool
	)
	doOne := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		s, err := client.LoadSettings(fetchCtx)
		if err != nil {
			logger.Debug("pollBoxInfo fail", "err", err)
			return
		}
		if model := strings.TrimSpace(s.Info.Type); model != "" {
			if !modelEverFound {
				logger.Info("box model detected", "type", model)
				modelEverFound = true
			}
			if model != lastModel {
				if err := ann.UpdateModel(model); err != nil {
					logger.Warn("mDNS UpdateModel failed", "err", err)
				} else {
					logger.Info("mDNS model updated", "model", model)
					lastModel = model
				}
			}
		}
		if name := strings.TrimSpace(s.Info.Name); name != "" && name != lastName {
			if err := ann.UpdateFriendlyName(name); err != nil {
				logger.Warn("mDNS UpdateFriendlyName failed", "err", err)
			} else {
				logger.Info("mDNS FriendlyName updated", "name", name)
				lastName = name
			}
		}
		if !regionLogged {
			// Bose's countryCode is set at the factory or during
			// the original Bose pairing flow and is rarely the
			// user's actual location after STR install. STR uses
			// region.txt (written by the setup wizard) for radio
			// defaults. Log the mismatch once so it is documented
			// in diagnostic bundles and not mistaken for a bug.
			boseCC := strings.ToUpper(strings.TrimSpace(s.Info.CountryCode))
			if region != "" && boseCC != "" && region != boseCC {
				logger.Info("Region: STR uses region.txt for radio defaults; Bose firmware countryCode is informational only",
					"strRegion", region, "boseCountryCode", boseCC)
			}
			regionLogged = true
		}
	}
	// Fast cadence until we have a real model, then back off to 30s.
	// Without backoff we'd keep hitting :8090 every 4s forever even
	// after model is stable — overkill, since name changes are rare
	// and 30s catches them within one UI refresh cycle.
	fast := time.NewTicker(4 * time.Second)
	defer fast.Stop()
	doOne()
	for !modelEverFound {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			doOne()
		}
	}
	slow := time.NewTicker(30 * time.Second)
	defer slow.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-slow.C:
			doOne()
		}
	}
}

// resource-health logging state. Guarded by its own mutex; the health loop is
// the only writer today, but the values are also read from the memory guard's
// goroutine in tests.
var (
	resHealthMu       sync.Mutex
	resHealthLastAt   time.Time
	resHealthLastMem  int64
	resHealthLastRSS  int64
	resHealthLastThr  int64
	resHealthHaveLast bool
)

// Thresholds for "something actually moved". Relative, because the interesting
// signal is a trend, not an absolute number, and absolute numbers differ per
// model.
const (
	resHealthMemDelta   = 0.08 // 8 % change in MemAvailable
	resHealthRSSDelta   = 0.15 // 15 % change in the agent's own RSS
	resHealthLowWater   = 0.20 // below 20 % free, every reading is interesting
	resHealthAnchorEach = time.Hour
)

// logResourceHealth records a snapshot of available memory and system load WHEN
// IT CHANGED. On this hardware (~120 MB RAM, no swap) a slow leak ends in an
// OOM freeze, and the trend leading up to it is the thing worth having.
//
// It used to write one line every five minutes regardless. That reads as
// harmless (12 lines an hour) until you look at what it costs where it matters:
// the NAND log is a 32 KB ring and the only log that survives a reboot on a box
// with no shell. Measured on a Portable 2026-08-06, routine heartbeats were
// 29.6 % of that window, and on an idle box the whole window reaches back only
// about seven hours. That is why the Lifestyle reboot-loop investigation ran
// out of history: the cause had already rolled out.
//
// So: log the first reading, log whenever memory or the agent's own footprint
// moves meaningfully, log every reading once free memory is low, and otherwise
// drop one anchor an hour so a flat box still shows a trend line. Everything
// else goes to Debug, where it costs nothing on NAND. No forensic signal is
// lost; the flat repeats that were burying it are.
func logResourceHealth(logger *slog.Logger) {
	avail, total := readMemKB()
	rss, threads := readSelfRSS()
	// The agent's own RSS and thread count travel with every line. If
	// memAvailable trends down while these stay flat, the leak is BoseApp's
	// (firmware); if these climb too, it is ours. That attributes the leak
	// preceding the recurring BoseApp freeze without guesswork.
	attrs := []any{
		"memAvailableKB", avail,
		"memTotalKB", total,
		"loadavg", readLoadAvg(),
		"agentRSSKB", rss,
		"agentThreads", threads,
	}
	why, worth := resourceHealthWorthLogging(avail, total, rss, threads, time.Now())
	if !worth {
		logger.Debug("resource health", attrs...)
		return
	}
	logger.Info("resource health", append(attrs, "why", why)...)
}

// resourceHealthWorthLogging decides, and records the new baseline when it says
// yes. Split out so the decision is testable without touching /proc.
func resourceHealthWorthLogging(avail, total, rss, threads int64, now time.Time) (string, bool) {
	resHealthMu.Lock()
	defer resHealthMu.Unlock()
	keep := func(why string) (string, bool) {
		resHealthLastAt, resHealthLastMem, resHealthLastRSS, resHealthLastThr = now, avail, rss, threads
		resHealthHaveLast = true
		return why, true
	}
	if !resHealthHaveLast {
		return keep("first")
	}
	// A box running low is the case this instrument exists for: never quiet it.
	if total > 0 && float64(avail) < resHealthLowWater*float64(total) {
		return keep("low-memory")
	}
	if moved(resHealthLastMem, avail, resHealthMemDelta) {
		return keep("memory-moved")
	}
	if moved(resHealthLastRSS, rss, resHealthRSSDelta) {
		return keep("agent-rss-moved")
	}
	if threads != resHealthLastThr {
		return keep("threads-changed")
	}
	if now.Sub(resHealthLastAt) >= resHealthAnchorEach {
		return keep("hourly-anchor")
	}
	return "", false
}

// moved reports a relative change of at least frac. A previous value of 0 (or a
// failed read, which logs -1) counts as moved so the first real reading lands.
func moved(prev, cur int64, frac float64) bool {
	if prev <= 0 {
		return cur > 0
	}
	d := float64(cur-prev) / float64(prev)
	if d < 0 {
		d = -d
	}
	return d >= frac
}

// memory-guard tunables. The Spotify Ogg path leaves a residual box-side
// firmware leak (~1.3 MB/min while playing) that only a reboot frees (pause,
// standby and re-push do not). The guard reboots the box ONLY when memory is
// critically low AND nothing is playing, so the leak is reset during idle and
// never causes an OOM mid-playback. When idle the leak does not grow, so the
// low reading is stable and there is no race with the 5-minute cycle.
const (
	// Live observation (2026-06-05, 35 min continuous Spotify with the 16 KB
	// flush fix): memAvail declined to a self-limiting floor of ~9 MB (brief
	// dips to ~4.4 MB) and the box did NOT OOM/reboot. So this is a true-OOM
	// backstop only: 6 MB sits below the normal ~9 MB idle floor (no reboot
	// after a normal session) yet above the danger zone. When idle the leak
	// does not grow, so a low reading is stable and the 5-min cycle is fine.
	memGuardThresholdKB = 6 * 1024
	// While Spotify is actively streaming the firmware leak is GROWING, so the
	// old "never interrupt playback" hold-off let it run to an uncontrolled OOM
	// (garbled audio then crash/reboot, live 2026-06-10). Below this critical
	// floor we reboot even during playback: a clean reboot + auto-resume beats
	// the firmware OOM. Set below the ~4.4 MB normal-session dip so it only fires
	// on a genuine runaway, not a healthy self-limiting session.
	memGuardCriticalKB   = 4 * 1024
	memGuardMinUptimeSec = 900 // never reboot in the first 15 min (boot-loop guard)
)

// memoryGuardCheck reboots the box when free memory is critically low and the
// box is idle, to clear the accumulated firmware leak from Spotify playback.
// Conservative and heavily logged: it skips while Spotify streams, while the
// box is playing anything (do not interrupt radio either), and early after
// boot. The reboot itself clears the condition, so no loop stamp is needed.
func memoryGuardCheck(logger *slog.Logger, sp *spotify.Manager, boxHost string) {
	avail, _ := readMemKB()
	if avail < 0 || avail > memGuardThresholdKB {
		return // healthy
	}
	if up := readUptimeSec(); up >= 0 && up < memGuardMinUptimeSec {
		logger.Warn("memory guard: low memAvail but uptime too short, holding off", "memAvailKB", avail, "uptimeSec", up)
		return
	}
	playing := (sp != nil && sp.Streaming()) || boxIsPlaying(boxHost)
	if playing && avail > memGuardCriticalKB {
		// Tolerate low memory during playback to avoid interrupting, but only down
		// to the critical floor. The Spotify manager's single-connection invariant
		// should keep steady playback stable; this is a last-resort net for any
		// runaway, where a controlled reboot + auto-resume beats the uncontrolled
		// firmware OOM (garbled audio then crash).
		logger.Warn("memory guard: low memAvail but playing and above critical, holding off", "memAvailKB", avail, "criticalKB", memGuardCriticalKB)
		return
	}
	logger.Warn("memory guard: memAvail critically low, rebooting box to clear firmware leak", "memAvailKB", avail, "playing", playing)
	_ = exec.Command("sync").Run()
	if err := exec.Command("reboot").Run(); err != nil {
		logger.Error("memory guard: reboot failed", "err", err)
	}
}

// agentBootReason distinguishes a real box boot from an agent-only respawn
// (watchdog, OOM kill, OTA restart) at agent startup, via the box's own
// /proc/uptime (the agent runs on the box). The distinction matters for the
// overnight preset-loss forensics: start-time writes after a nightly
// agent-only respawn hit a box that was asleep, while after a real boot the
// box is awake anyway.
func agentBootReason() string {
	up := readUptimeSec()
	switch {
	case up < 0:
		return "unknown"
	case up < 600:
		return fmt.Sprintf("box-boot (uptime %ds)", up)
	default:
		return fmt.Sprintf("agent-respawn (box already up %dh%02dm)", up/3600, (up%3600)/60)
	}
}

// readUptimeSec returns system uptime in seconds, or -1 on error.
func readUptimeSec() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return -1
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return -1
	}
	sec, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return -1
	}
	return int64(sec)
}

func readSelfRSS() (rssKB, threads int64) {
	rssKB, threads = -1, -1
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "VmRSS:":
			rssKB, _ = strconv.ParseInt(f[1], 10, 64)
		case "Threads:":
			threads, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	return
}

func readMemKB() (avail, total int64) {
	avail, total = -1, -1
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MemAvailable:":
			avail, _ = strconv.ParseInt(f[1], 10, 64)
		case "MemTotal:":
			total, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	return
}

func readLoadAvg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(b)); len(f) >= 3 {
		return f[0] + " " + f[1] + " " + f[2]
	}
	return ""
}

// selfProbeTarget names a TCP endpoint the agent should be able to
// reach via loopback right after its listeners are spawned.
type selfProbeTarget struct {
	name string
	addr string // ":8888" — leading colon ok, normalised below
}

// runSelfProbe attempts loopback connect to each target every 2 s for
// the first 30 s, then once a minute for the next 5 minutes. Each
// outcome (ok / refused / timeout) is logged at WARN with the elapsed
// time since probe start, so the diagnostic bundle shows exactly when
// (or whether) each listener accepted its first connection.
//
// This is purely observational; the probe never restarts a listener.
// It is the inside-the-agent counterpart to run.sh's agent_port_bound,
// useful when BusyBox lacks ss/netstat and the bash probe is blind.
func runSelfProbe(ctx context.Context, logger *slog.Logger, targets []selfProbeTarget) {
	start := time.Now()
	probe := func() {
		for _, t := range targets {
			addr := t.addr
			if strings.HasPrefix(addr, ":") {
				addr = "127.0.0.1" + addr
			}
			elapsed := time.Since(start).Round(time.Second)
			d := net.Dialer{Timeout: 2 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				logger.Warn("self-probe: connect failed", "target", t.name, "addr", addr, "elapsed", elapsed.String(), "err", err.Error())
				continue
			}
			_ = conn.Close()
			logger.Debug("self-probe: connect ok", "target", t.name, "addr", addr, "elapsed", elapsed.String())
		}
	}

	// Phase 1: every 2 s for 30 s — listener bring-up window.
	fastTicker := time.NewTicker(2 * time.Second)
	defer fastTicker.Stop()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	probe()
fast:
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			break fast
		case <-fastTicker.C:
			probe()
		}
	}

	// Phase 2: once a minute for 5 minutes — covers slow boot variants.
	slowTicker := time.NewTicker(60 * time.Second)
	defer slowTicker.Stop()
	slowDeadline := time.NewTimer(5 * time.Minute)
	defer slowDeadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-slowDeadline.C:
			return
		case <-slowTicker.C:
			probe()
		}
	}
}

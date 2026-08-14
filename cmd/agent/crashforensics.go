// Crash forensics: why the previous agent process stopped.
//
// The agent can vanish and be respawned by the boot script's watchdog without
// leaving a single line about it. A live Portable did exactly that on
// 2026-08-14: a healthy Spotify session (156 s attached, 3049 KB forwarded),
// then nothing for 76 seconds, then "streborn starting" with
// bootReason="agent-respawn (box already up 2h19m)". Spotify never recovered
// afterwards, and the log could not say whether the process had panicked, been
// killed for memory, or been stopped deliberately.
//
// One thing it could already say by omission: a deliberate stop logs "shutdown
// signal received" from the SIGTERM handler, and there was no such line. So the
// process was killed rather than asked to stop. That is inference from an
// absence, which is exactly the kind of reasoning this file exists to replace.
//
// Two cheap measurements make the next one answerable:
//
//   - A heartbeat in /tmp, so the next start knows what the previous run looked
//     like moments before it died: free memory, the agent's own footprint,
//     thread count and whether Spotify was streaming. /tmp is tmpfs, so it
//     costs no NAND and it survives an agent respawn while a box reboot clears
//     it, which is the same distinction bootReason draws.
//   - The kernel's own verdict. The OOM killer names its victim in the ring
//     buffer, and that line settles "killed for memory" outright.

package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// heartbeatPath lives on tmpfs deliberately: no NAND wear, and it disappears
// on a box reboot, so a heartbeat that IS there always belongs to the process
// that just died on a box which stayed up.
const heartbeatPath = "/tmp/streborn-agent-heartbeat.json"

// heartbeatEvery is a compromise between resolution and pointless writes. The
// interesting window is the last minute before a kill, and half a minute of
// granularity places the death inside it without writing constantly.
const heartbeatEvery = 30 * time.Second

// agentHeartbeat is the state of a running agent at one moment.
type agentHeartbeat struct {
	At               string `json:"at"`
	UptimeSec        int64  `json:"boxUptimeSec"`
	MemAvailKB       int64  `json:"memAvailableKB"`
	MemTotalKB       int64  `json:"memTotalKB"`
	AgentRSSKB       int64  `json:"agentRSSKB"`
	AgentThreads     int64  `json:"agentThreads"`
	SpotifyStreaming bool   `json:"spotifyStreaming"`
}

// lastExitReport is what the previous run left behind, assembled once at start.
type lastExitReport struct {
	// BootReason is the same string the box-write ledger is armed with.
	BootReason string `json:"bootReason"`
	// Previous is the last heartbeat the dead process wrote, absent when this
	// is a box boot (tmpfs cleared) or the first run after an update.
	Previous *agentHeartbeat `json:"previousRun,omitempty"`
	// GapSec is how long between that heartbeat and this start. A gap close to
	// heartbeatEvery means the process died right after it; a much larger one
	// means it was already wedged and not writing.
	GapSec int64 `json:"gapSec,omitempty"`
	// OOMKill carries the kernel's own line when the OOM killer took the agent.
	// Empty means the ring buffer holds no such verdict, which on this kernel
	// is evidence rather than silence: the OOM killer always logs its victim.
	OOMKill string `json:"oomKill,omitempty"`
	// DmesgTail is the last few kernel lines, for the cases the OOM matcher
	// does not cover (a watchdog, a segfault, a filesystem going read only).
	DmesgTail string `json:"dmesgTail,omitempty"`
}

var (
	lastExitMu sync.RWMutex
	lastExit   lastExitReport
)

// LastExit returns the assembled report for /api/debug/state.
func lastExitSnapshot() any {
	lastExitMu.RLock()
	defer lastExitMu.RUnlock()
	return lastExit
}

// noteAgentStart reads whatever the previous run left behind and logs it. Call
// once, early, BEFORE the new heartbeat overwrites the old file.
//
// It never fails the start: every input here is best effort, and an agent that
// cannot explain its predecessor's death still has to run.
func noteAgentStart(bootReason string, logger *slog.Logger) {
	rep := lastExitReport{BootReason: bootReason}

	if prev, err := readHeartbeat(); err == nil {
		rep.Previous = prev
		if t, perr := time.Parse(time.RFC3339Nano, prev.At); perr == nil {
			rep.GapSec = int64(time.Since(t).Seconds())
		}
	}
	rep.OOMKill, rep.DmesgTail = scanKernelForKill()

	lastExitMu.Lock()
	lastExit = rep
	lastExitMu.Unlock()

	// Only say something when there is something to say. A box boot with no
	// heartbeat and no OOM line is the normal case and deserves no NAND.
	switch {
	case rep.OOMKill != "":
		logger.Warn("previous agent run was killed for memory by the kernel",
			"bootReason", bootReason, "oom", rep.OOMKill, "gapSec", rep.GapSec)
	case rep.Previous != nil:
		logger.Warn("previous agent run ended without a shutdown signal, here is its last heartbeat",
			"bootReason", bootReason,
			"gapSec", rep.GapSec,
			"memAvailableKB", rep.Previous.MemAvailKB,
			"agentRSSKB", rep.Previous.AgentRSSKB,
			"agentThreads", rep.Previous.AgentThreads,
			"spotifyStreaming", rep.Previous.SpotifyStreaming)
	}
}

// runHeartbeat writes the state file until ctx is done. streaming reports
// whether Spotify is currently forwarding; nil when Spotify is not configured.
func runHeartbeat(stop <-chan struct{}, streaming func() bool, logger *slog.Logger) {
	write := func() {
		avail, total := readMemKB()
		rss, threads := readSelfRSS()
		hb := agentHeartbeat{
			At:           time.Now().Format(time.RFC3339Nano),
			UptimeSec:    readUptimeSec(),
			MemAvailKB:   avail,
			MemTotalKB:   total,
			AgentRSSKB:   rss,
			AgentThreads: threads,
		}
		if streaming != nil {
			hb.SpotifyStreaming = streaming()
		}
		b, err := json.Marshal(hb)
		if err != nil {
			return
		}
		// Write in place rather than via a temp + rename: the file is tiny, a
		// torn write only costs one sample, and a rename dance on tmpfs buys
		// nothing while leaving stale temps behind if we are killed mid-way.
		if err := os.WriteFile(heartbeatPath, b, 0o644); err != nil {
			logger.Debug("heartbeat write failed", "err", err)
		}
	}
	write() // one immediately, so a process that dies inside the first interval still leaves a mark
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			write()
		}
	}
}

func readHeartbeat() (*agentHeartbeat, error) {
	b, err := os.ReadFile(heartbeatPath)
	if err != nil {
		return nil, err
	}
	var hb agentHeartbeat
	if err := json.Unmarshal(b, &hb); err != nil {
		return nil, err
	}
	return &hb, nil
}

// oomMarkers are how this kernel announces a kill. Matching several keeps the
// check honest across the wording differences between kernel versions.
var oomMarkers = []string{"Out of memory", "oom-kill", "oom_reaper", "Killed process"}

// scanKernelForKill returns the OOM line naming our binary, plus a short tail
// of the kernel ring buffer for anything the matcher does not know about.
//
// dmesg is read through the command rather than /dev/kmsg because the ring
// buffer needs no privileges this way on the box, and because a missing dmesg
// then simply yields nothing instead of an error path nobody reads.
func scanKernelForKill() (oom, tail string) {
	out, err := exec.Command("dmesg").Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if !strings.Contains(l, "streborn") {
			continue
		}
		for _, m := range oomMarkers {
			if strings.Contains(l, m) {
				oom = strings.TrimSpace(l)
				break
			}
		}
		if oom != "" {
			break
		}
	}
	const keep = 12
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	return oom, strings.Join(lines, "\n")
}

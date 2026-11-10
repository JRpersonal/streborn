package main

import (
	"context"
	"time"
)

// What the install reaches out to and how long it waits, in one place. The
// install used to keep these as package-level variables next to the code that
// read them, and its tests reassigned them; a late-watcher goroutine that was
// still running when a test restored one was a data race (#1547). They now
// live in two structs, the way the update sequence keeps its own (otaSeams and
// otaPolicy in otaorchestrate.go): production builds them here, a test builds
// its own and hangs it on its App, and nothing is shared between two Apps.

// installPolicy holds the install's tunable waits. The production values are
// field-learned and are NOT to be tuned without a new field reason; tests run
// the same code at a fast clock through this struct.
type installPolicy struct {
	// lateRecheckDelay is how long after the agent-wait budget ran out the
	// late watcher takes its first extra look (installwait.go); waitPoll is
	// its cadence after that.
	lateRecheckDelay time.Duration
	waitPoll         time.Duration
	// waitCeiling / setupWaitCeiling bound the late watcher. Both fifteen
	// minutes, which is what every user-facing string promises ("wait about
	// fifteen minutes"): a watcher that outlives its own promise leaves the
	// user staring at a screen that said it would be done ten minutes ago.
	// The setup case gets its own name because it is a different wait with a
	// different message, not a longer one.
	waitCeiling      time.Duration
	setupWaitCeiling time.Duration
	// hungRebootCheckAfter is how long after the restart command the
	// hung-restart check may run (install_hungreboot.go). A speaker that is
	// restarting normally has its SSH gone well before this; one that answers
	// SSH this late AND reports an uptime older than the command has not
	// restarted at all.
	hungRebootCheckAfter time.Duration
	// hungRebootCheckWindow bounds how old a restart-ledger entry may be and
	// still count. An entry from an install half an hour ago says nothing
	// about the speaker now.
	hungRebootCheckWindow time.Duration
	// firmwareSettle and firmwarePoll are the settle wait for a speaker whose
	// /info names neither its model nor its firmware (installReadFirmware).
	firmwareSettle time.Duration
	firmwarePoll   time.Duration
}

// installProductionPolicy is the install as it runs against real speakers.
var installProductionPolicy = installPolicy{
	lateRecheckDelay:      120 * time.Second,
	waitPoll:              20 * time.Second,
	waitCeiling:           15 * time.Minute,
	setupWaitCeiling:      15 * time.Minute,
	hungRebootCheckAfter:  60 * time.Second,
	hungRebootCheckWindow: 10 * time.Minute,
	firmwareSettle:        45 * time.Second,
	firmwarePoll:          5 * time.Second,
}

// installSeams is everything the install's testable steps reach out to, so
// they can be exercised without a speaker, a ping binary or an SSH server on
// this machine. Production wiring is in newInstallSeams.
type installSeams struct {
	// ensureEngine and sleep drive the install's last step, the Spotify
	// engine delivery with its backoff (installengine.go).
	ensureEngine func(host string) (engineResult, error)
	sleep        func(d time.Duration)

	// The SSH side of the repair's staging chooser (install_repair.go): a
	// speaker's free space, its memory, the agent stop and the upload itself.
	repairFreeBytes    func(host, path string) int64
	repairMemAvailable func(host string) (int64, bool)
	repairStopSTR      func(host string) (strStopResult, error)
	repairStageUpload  func(host, dir string, files map[string][]byte) error

	// What the verdict at the end of the agent wait and the late watcher ask
	// (installwait.go): the network facts, whether the agent runs as a process
	// over SSH, and whether the speaker is in its own out-of-box setup.
	waitFacts   func(ctx context.Context, host string) agentWaitFacts
	runningLate func(host string) bool
	inSetup     func(host string) bool

	// The hung-restart check's uptime read and its forced restart
	// (install_hungreboot.go).
	hungRebootProbe func(host string) (string, error)
	forceReboot     func(host string) error

	// now and readFirmware are the clock and the /info read of the stick
	// firmware gate (install_fwgate.go).
	now          func() time.Time
	readFirmware func(host string) (FirmwareInfo, error)
}

// newInstallSeams wires the install to the real speaker.
func newInstallSeams(a *App) *installSeams {
	return &installSeams{
		ensureEngine: func(host string) (engineResult, error) { return a.ensureSpotifyEngine(host, 0) },
		sleep:        time.Sleep,

		repairFreeBytes:    freeBytesAtPath,
		repairMemAvailable: boxMemAvailable,
		repairStopSTR:      stopSTROnBox,
		repairStageUpload:  a.stageRepairFiles,

		waitFacts:   gatherAgentWaitFacts,
		runningLate: a.agentRunningViaSSH,
		inSetup:     a.boxInSetupNow,

		hungRebootProbe: func(host string) (string, error) {
			return boxSSHOutput(host, hungRebootProbeCmd, 8*time.Second)
		},
		forceReboot: func(host string) error {
			err := boxSSHFireAndForget(host, forcedRebootCmd, 6*time.Second)
			boxSSHClients.invalidateHost(host)
			return err
		},

		now:          time.Now,
		readFirmware: a.GetBoxFirmware,
	}
}

// installSeams returns what this App's install reaches out to: the production
// wiring, unless a test hung its own on the App.
func (a *App) installSeams() *installSeams {
	if a.instSeams != nil {
		return a.instSeams
	}
	return newInstallSeams(a)
}

// installPolicy returns this App's install waits: the production values,
// unless a test hung its own on the App.
func (a *App) installPolicy() installPolicy {
	if a.instPolicy != nil {
		return *a.instPolicy
	}
	return installProductionPolicy
}

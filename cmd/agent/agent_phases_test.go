package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The boot watchdog in usb-stick/run.sh checks ALIVE and BOUND every 5 s from
// t=5 s: an agent that spends its first seconds on heavy init (box /info,
// CA generation, mDNS announce) before binding :8888 is killed mid-init and
// respawned in a loop. The phase table is where that order is written down,
// so this pins it: the build phases, then bindListeners, and only after it
// the background loops and the deferred heavy init.
func TestAgentPhasesBindListenersBeforeHeavyInit(t *testing.T) {
	var names []string
	for _, p := range (&agent{}).phases() {
		names = append(names, p.name)
	}
	want := []string{
		"bootstrap",
		"buildStores",
		"buildMarge",
		"buildMedia",
		"buildWebui",
		"buildBoxLink",
		"bindListeners",
		"startBackground",
		"wireLate",
		"startDeferred",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("phase order changed:\n got %v\nwant %v\nbindListeners must stay right after the build phases and before every start phase, or the boot watchdog kills the agent mid-init", names, want)
	}
	bind := slices.Index(names, "bindListeners")
	for i, n := range names {
		prepares := n == "bootstrap" || strings.HasPrefix(n, "build")
		starts := n == "wireLate" || strings.HasPrefix(n, "start")
		switch {
		case i < bind && !prepares:
			t.Errorf("%s runs before the listeners are bound; only bootstrap and build phases may", n)
		case i > bind && !starts:
			t.Errorf("%s runs after the listeners are bound but is not a start or wire phase", n)
		}
	}
}

// runPhases runs what it is given in order, tells the observer each name
// before the phase runs, and stops at the first error with the phase named,
// so a failed start is attributable from the one log line it leaves.
func TestRunPhasesRunsInOrderAndStopsAtTheFirstError(t *testing.T) {
	var ran, observed []string
	boom := errors.New("bind failed")
	phases := []phase{
		{"first", func() error { ran = append(ran, "first"); return nil }},
		{"second", func() error { ran = append(ran, "second"); return boom }},
		{"third", func() error { ran = append(ran, "third"); return nil }},
	}
	err := runPhases(phases, func(name string) { observed = append(observed, name) })
	if !errors.Is(err, boom) {
		t.Fatalf("runPhases() = %v, want the phase's error", err)
	}
	if err.Error() != "second: bind failed" {
		t.Errorf("error %q does not name the phase", err)
	}
	if !slices.Equal(ran, []string{"first", "second"}) {
		t.Errorf("ran %v, want first and second only", ran)
	}
	if !slices.Equal(observed, ran) {
		t.Errorf("observed %v, want the phases that ran, in order", observed)
	}
	if err := runPhases(phases[:1], nil); err != nil {
		t.Errorf("a nil observer must be allowed, got %v", err)
	}
}

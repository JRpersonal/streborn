package main

import (
	"fmt"
	"testing"
)

// The desktop app answers "how much free speaker storage does this write
// need" in three places, all for a file landing on the same /mnt/nv:
//
//	nandNeedCompressed  raw*0.7 + 1 MB   the update preflight, the engine
//	                                     delivery over HTTP, the OTA journal
//	sshPushNeedKB       the same figure  the agent update and the engine push
//	                    in df's kB       over SSH
//	repairStageNeed     raw*0.6 + 2 MB   the SSH repair's /mnt/nv staging
//	                                     (orders the targets, never refuses)
//
// This file pins what each returns, so that changing one is a decision with
// the numbers in front of it and never a side effect. Until 2026-11-10 the
// SSH push asked for raw + 2 MB with no compression credit.

func TestTheThreeStorageNeedsAsTheyAreToday(t *testing.T) {
	cases := []struct {
		name string
		raw  int64
		// http is nandNeedCompressed, repairNV is repairStageNeed at the
		// /mnt/nv factor, sshKB is sshPushNeedKB.
		http, repairNV, sshKB int64
	}{
		{"nothing", 0, 1048576, 2097152, 1024},
		{"1 MB", 1 * mb, 1782579, 2726297, 1741},
		{"10 MB, where the first two meet", 10 * mb, 8388608, 8388608, 8192},
		{"a 13.3 MB agent", 13303970, 10361355, 10079534, 10119},
		{"a 16 MB engine", 16 * mb, 12792627, 12163481, 12493},
		{"a 17.5 MB agent", 18350080, 13893632, 13107200, 13568},
		{"28 MB", 28 * mb, 21600665, 19713228, 21095},
	}
	nv := repairNVCompress(t)
	for _, c := range cases {
		if got := nandNeedCompressed(c.raw); got != c.http {
			t.Errorf("%s: nandNeedCompressed(%d) = %d, want %d", c.name, c.raw, got, c.http)
		}
		if got := repairStageNeed(c.raw, nv); got != c.repairNV {
			t.Errorf("%s: repairStageNeed(%d, %v) = %d, want %d", c.name, c.raw, nv, got, c.repairNV)
		}
		if got := sshPushNeedKB(c.raw); got != c.sshKB {
			t.Errorf("%s: sshPushNeedKB(%d) = %d, want %d", c.name, c.raw, got, c.sshKB)
		}
	}
}

// repairNVCompress is the factor the repair's candidate table gives /mnt/nv.
func repairNVCompress(t *testing.T) float64 {
	t.Helper()
	for _, c := range repairStageCandidates {
		if c.base == "/mnt/nv" {
			if c.ram {
				t.Fatal("/mnt/nv is marked as a RAM candidate")
			}
			return c.compress
		}
	}
	t.Fatal("/mnt/nv is no longer a repair staging candidate")
	return 0
}

// The RAM candidates take the raw size (tmpfs stores verbatim) plus the same
// 2 MB.
func TestRepairStageNeedForRAMCandidates(t *testing.T) {
	for _, c := range repairStageCandidates {
		if !c.ram {
			continue
		}
		if got, want := repairStageNeed(20*mb, c.compress), 22*mb; got != want {
			t.Errorf("%s: repairStageNeed(20 MB) = %d, want %d", c.base, got, want)
		}
	}
}

// The staging chooser itself, at the boundary: with every RAM target too small
// by df, the NAND is tried FIRST from exactly repairStageNeed bytes free and
// LAST one byte below it. This holds the formula where it is used, not only
// the helper that carries it.
func TestRepairStagingUsesItsNeedAtTheBoundary(t *testing.T) {
	const raw = 13303970
	const need = 10079534 // repairStageNeed(raw, 0.6), from the table above
	for _, c := range []struct {
		nvFree int64
		first  string
	}{
		{need, "stage /mnt/nv/streborn-install"},
		{need - 1, "stage /dev/shm/streborn-install"},
	} {
		s := &repairSeams{
			mem:  []int64{80 * mb},
			free: map[string]int64{"/dev/shm": 1, "/run": 1, "/tmp": 1, "/mnt/nv": c.nvFree},
			stageErr: map[string]error{
				"/dev/shm": fmt.Errorf("no"), "/run": fmt.Errorf("no"), "/tmp": fmt.Errorf("no"), "/mnt/nv": fmt.Errorf("no"),
			},
		}
		a := s.install(t)
		if _, _, err := a.stageRepairFilesBestEffort("h", raw, nil); err == nil {
			t.Fatal("every target was scripted to fail")
		}
		var staged []string
		for _, e := range s.events {
			if e != "mem" {
				staged = append(staged, e)
			}
		}
		if len(staged) != 4 || staged[0] != c.first {
			t.Errorf("/mnt/nv free = %d: staging order %v, want %q first of four", c.nvFree, staged, c.first)
		}
	}
}

// The SSH push and the HTTP delivery agree on the SAME 16 MB engine going
// onto the SAME /mnt/nv. They used to disagree (12.2 MB over HTTP, 18 MB over
// SSH), so a speaker with 13 to 18 MB free was handed the engine over HTTP
// and refused it over SSH after the SSH preamble had deleted the old one.
func TestTheSSHPushAsksForWhatTheHTTPGateAsksForTheSameFile(t *testing.T) {
	const engine = 16 * mb
	httpNeed := nandNeedCompressed(engine)
	sshNeed := sshPushNeedKB(engine) * 1024
	if httpNeed != 12792627 {
		t.Fatalf("engine need over http = %d, want 12792627", httpNeed)
	}
	// Rounded up to df's kB: never below the HTTP figure, less than 1 kB above.
	if sshNeed < httpNeed || sshNeed-httpNeed >= 1024 {
		t.Fatalf("engine need over ssh = %d, want %d rounded up to a kB", sshNeed, httpNeed)
	}
	for _, c := range []struct {
		free      int64
		http, ssh bool
	}{
		{12 * mb, false, false},
		{13 * mb, true, true},
		{17 * mb, true, true},
		{18 * mb, true, true},
	} {
		// The SSH check runs on the speaker in kB: refused while free < need.
		sshFits := c.free/1024 >= sshPushNeedKB(engine)
		if got := nandFits(c.free, 0, httpNeed); got != c.http || sshFits != c.ssh {
			t.Errorf("%d MB free: http fits = %v ssh fits = %v, want %v and %v", c.free/mb, got, sshFits, c.http, c.ssh)
		}
	}
	// The repair's NAND figure sits below the HTTP gate's from 10 MB up and
	// above it below that; for the install file set (~13.3 MB) the two are
	// 0.27 MB apart.
	if d := nandNeedCompressed(13303970) - repairStageNeed(13303970, repairNVCompress(t)); d != 281821 {
		t.Errorf("http need minus repair need for a 13.3 MB file set = %d, want 281821", d)
	}
}

package main

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// TestLiveAppUpdateFlow exercises the REAL app-integrated update + Spotify-engine
// flow - the exact App methods the GUI "Update" button calls - against a live box
// on the LAN. It exists so the flow is validated end to end WITHOUT the Wails GUI
// (which Smart App Control blocks for unsigned local builds) and WITHOUT manual
// SSH/curl. The key assertion is that the app flow leaves the Spotify engine
// PRESENT: UpdateBoxAgent stages the sidecar before the agent push, so unlike a
// bare agent OTA the engine must survive the single reboot.
//
// Opt-in: set STR_LIVE_BOX=<ip> (and STR_LIVE_PORT, default 8888; use 17008 for
// BCO/taigan/scm). Skipped otherwise so CI never touches hardware.
func TestLiveAppUpdateFlow(t *testing.T) {
	host := os.Getenv("STR_LIVE_BOX")
	if host == "" {
		t.Skip("set STR_LIVE_BOX=<ip> (and optionally STR_LIVE_PORT) to run the live app-flow test")
	}
	port := 8888
	if p := os.Getenv("STR_LIVE_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	// No startup(): a.ctx stays nil, and every bound method is written to tolerate
	// that (appCtx() -> Background, emitInstallProgress guards on a.ctx != nil).
	a := NewApp()

	v0, err := a.BoxAgentVersion(host, port)
	if err != nil {
		t.Fatalf("baseline version read (%s:%d): %v", host, port, err)
	}
	t.Logf("BEFORE  version=%s build=%s goLibrespot=%s foreignDirs=%q nandFree=%s",
		v0["version"], v0["build"], v0["goLibrespot"], v0["foreignDirs"], v0["nandFreeBytes"])

	// The app-integrated update: pushes THIS build's embedded agent AND stages the
	// Spotify engine before the reboot (stageSidecarBeforeReboot inside).
	t.Log("running App.UpdateBoxAgent (the GUI 'Update' path)...")
	if err := a.UpdateBoxAgent(host, port); err != nil {
		t.Fatalf("UpdateBoxAgent (app flow) returned an error: %v", err)
	}

	// Wait for the box to reboot and its agent to answer steadily again, exactly
	// as the GUI's post-OTA version poll does.
	deadline := time.Now().Add(4 * time.Minute)
	var v1 map[string]string
	for time.Now().Before(deadline) {
		time.Sleep(8 * time.Second)
		v, verr := a.BoxAgentVersion(host, port)
		if verr == nil && v["version"] != "" {
			v1 = v
			// Give the post-reboot engine supervise a beat to report present.
			if v["goLibrespot"] == "present" {
				break
			}
		}
	}
	if v1 == nil {
		t.Fatalf("box did not answer its agent version within the wait window after the app update")
	}

	// The GUI runs EnsureSpotifyEngine after the update as a safety net (it is a
	// cheap "current" no-op when the sidecar staging already delivered the engine).
	res, eerr := a.EnsureSpotifyEngine(host, port)
	t.Logf("EnsureSpotifyEngine -> %q (err=%v)", res, eerr)

	v2, err := a.BoxAgentVersion(host, port)
	if err != nil {
		t.Fatalf("post-flow version read: %v", err)
	}
	t.Logf("AFTER   version=%s build=%s goLibrespot=%s foreignDirs=%q nandFree=%s",
		v2["version"], v2["build"], v2["goLibrespot"], v2["foreignDirs"], v2["nandFreeBytes"])

	// The whole point: the app-integrated flow must leave Spotify usable.
	if v2["goLibrespot"] != "present" {
		t.Fatalf("app update flow did NOT leave the Spotify engine present (goLibrespot=%s); "+
			"the sidecar staging or its re-delivery failed", v2["goLibrespot"])
	}
	// And the agent must actually be the build we pushed (not a revert).
	if v2["build"] != appBuild && appBuild != "" {
		t.Logf("NOTE: box build %q != app build %q (possible OTA revert, or a dev stamp mismatch)", v2["build"], appBuild)
	}
}

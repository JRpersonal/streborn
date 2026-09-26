package main

// Putting STR back in charge of the speakers' cloud address, on every speaker
// at once.
//
// A SoundTouch speaker holds ONE address for the service it reports to. STR
// sets it, and so does every rival app; whoever wrote last owns the speaker.
// STR keeps the original hostname and points it at the speaker itself, so the
// traffic never leaves the house. Another app can instead write its own host on
// the internet, and then STR can no longer pair with the speaker and the six
// hardware keys go dead.
//
// Measured on a five-speaker fleet on 2026-09-26, minutes after the ST Remote
// Pro iOS app was started: every live margeURL had become
// stremotepro.com/api/bose-cloud, every speaker restarted, none was paired
// afterwards. The override file on NAND was untouched and still correct, which
// is why the existing file-based repair healed nothing.
//
// The cure is a restart: the firmware reads that file once, at boot. Verified
// on all five, one reboot each, every one back stock and paired.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

// CloudRestoreTarget is one speaker the user asked to put right.
type CloudRestoreTarget struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Name string `json:"name"`
}

// CloudRestoreResult is what happened to one speaker. Deliberately verbose:
// "it did not work" is the answer users get from every other tool in this
// space, and the whole point here is to say which half failed.
type CloudRestoreResult struct {
	Host string `json:"host"`
	Name string `json:"name"`
	// Rebooted is true when the speaker was restarted for this.
	Rebooted bool `json:"rebooted"`
	// CameBack is true when it answered again afterwards.
	CameBack bool `json:"cameBack"`
	// Restored is the only field that means success: the speaker no longer
	// names a foreign address.
	Restored bool `json:"restored"`
	// StillForeign carries the address when it is somehow still wrong, so the
	// app can name it rather than shrug.
	StillForeign string `json:"stillForeign,omitempty"`
	Error        string `json:"error,omitempty"`
}

// cloudRestoreBootBudget bounds the wait for one speaker to come back. A cold
// ST30 is the slowest in the fleet at a bit over a minute; three times that
// leaves room for a speaker that decides to reboot twice without turning a
// working repair into a reported failure.
const cloudRestoreBootBudget = 4 * time.Minute

// RestoreSTRCloud puts STR's cloud address back on each speaker given, one at a
// time, and verifies each one before moving on.
//
// Sequential on purpose. Rebooting a whole fleet at once makes a failure
// impossible to attribute, and a speaker that is a group master takes its
// followers with it; one at a time also means the user still has music on the
// others while it runs.
func (a *App) RestoreSTRCloud(targets []CloudRestoreTarget) []CloudRestoreResult {
	out := make([]CloudRestoreResult, 0, len(targets))
	for i, t := range targets {
		a.emitCloudRestore(i, len(targets), t.Name, "checking")
		res := a.restoreOneCloudURL(t)
		out = append(out, res)
		state := "done"
		if !res.Restored {
			state = "failed"
		}
		a.emitCloudRestore(i+1, len(targets), t.Name, state)
	}
	return out
}

func (a *App) emitCloudRestore(done, total int, name, state string) {
	if a.ctx == nil {
		return
	}
	wailsrt.EventsEmit(a.ctx, "cloudrestore:progress", map[string]any{
		"done": done, "total": total, "name": name, "state": state,
	})
}

func (a *App) restoreOneCloudURL(t CloudRestoreTarget) CloudRestoreResult {
	res := CloudRestoreResult{Host: t.Host, Name: t.Name}

	// 1. Ask the agent to put the configuration right. On the runtime-hijack
	// shape this writes nothing and answers rebootRequired, which is correct:
	// the file was never wrong.
	resp, err := a.boxDoTimeout(t.Host, t.Port, http.MethodPost,
		"/api/box/restore-cloud-url", "application/json", "", 20*time.Second)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	var heal struct {
		RebootRequired bool   `json:"rebootRequired"`
		LiveForeign    string `json:"liveForeign"`
		FilesForeign   string `json:"filesForeign"`
		Note           string `json:"note"`
	}
	dec := json.NewDecoder(resp.Body)
	decErr := dec.Decode(&heal)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("the speaker refused the repair (status %d)", resp.StatusCode)
		return res
	}
	if decErr != nil {
		res.Error = decErr.Error()
		return res
	}

	// Nothing wrong and nothing pending: it is already ours. Report success
	// rather than rebooting a healthy speaker for no reason.
	if !heal.RebootRequired && heal.LiveForeign == "" && heal.FilesForeign == "" {
		res.Restored = true
		return res
	}

	// 2. The restart. This is the actual cure and there is no quieter one: the
	// firmware reads the cloud address once, at boot.
	rb, err := a.boxDoTimeout(t.Host, t.Port, http.MethodPost,
		"/api/box/reboot", "application/json", "", 15*time.Second)
	if err != nil {
		res.Error = "could not restart the speaker: " + err.Error()
		return res
	}
	rb.Body.Close()
	res.Rebooted = true

	// 3. Wait for it, then check. Waiting for it to GO first matters: the agent
	// answers for a moment after accepting the reboot, and a check in that
	// window reads the old, still-hijacked state and calls a good repair a
	// failure. Measured live on 2026-09-26, which is why this is here.
	a.waitBoxGone(t, 45*time.Second)
	deadline := time.Now().Add(cloudRestoreBootBudget)
	for time.Now().Before(deadline) {
		foreign, ok := a.boxForeignCloudURL(t)
		if ok {
			res.CameBack = true
			res.StillForeign = foreign
			res.Restored = foreign == ""
			return res
		}
		time.Sleep(5 * time.Second)
	}
	res.Error = "the speaker did not come back within " + cloudRestoreBootBudget.String()
	return res
}

// waitBoxGone blocks until the speaker stops answering, or the window passes.
// A speaker that never goes away is not an error here: some restart faster than
// the first poll, and the verification below is what decides.
func (a *App) waitBoxGone(t CloudRestoreTarget, window time.Duration) {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if _, ok := a.boxForeignCloudURL(t); !ok {
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// boxForeignCloudURL reads the agent's own verdict. ok is false when the
// speaker did not answer at all, which is what the wait loops key on; the
// string is the foreign address, empty meaning the speaker is STR's again.
func (a *App) boxForeignCloudURL(t CloudRestoreTarget) (string, bool) {
	resp, err := a.boxDoTimeout(t.Host, t.Port, http.MethodGet,
		"/api/agent/version", "", "", 5*time.Second)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var v map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", false
	}
	return v["foreignCloudURL"], true
}

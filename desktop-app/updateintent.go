package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Update intent: what the app decided a speaker should be running, kept across
// app restarts so an interrupted update is finished rather than forgotten.
//
// Until now the update lived entirely inside one UI flow. It drove the upload,
// the reboot and the engine re-delivery, and it knew exactly where the speaker
// stood at every moment, but only while it was running. Close the app during
// the reboot, or start the update from another machine, and nothing ever went
// back to check: a speaker could be left on a half-finished state with the
// Spotify engine deleted and no one to put it back, and the app would happily
// show it as a normal speaker.
//
// The fix is not more retries inside that one flow. It is writing down what the
// speaker is supposed to be running, so that any later run of the app can
// compare that against what the speaker actually reports and correct the
// difference. The intent is the contract; the reconcile is the supervision.
//
// Deliberately NOT a licence to act on its own: the app only finishes work a
// user asked for. Restoring a deleted engine needs no reboot and is always
// done. An agent update that was cut short is only resumed while the request is
// still fresh; after that the speaker is merely flagged, because rebooting
// someone's speaker hours later on the strength of an old click is not
// supervision, it is a surprise.

// updateIntent is one speaker's record.
type updateIntent struct {
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	DeviceID      string    `json:"deviceID,omitempty"`
	Name          string    `json:"name,omitempty"`
	TargetVersion string    `json:"targetVersion"`
	WantEngine    bool      `json:"wantEngine"`
	StartedAt     time.Time `json:"startedAt"`
	Attempts      int       `json:"attempts,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
}

// resumeAgentWindow bounds how long an interrupted agent update may be resumed
// without asking again. Long enough to cover an app crash, a reboot of the
// user's PC or a closed lid, short enough that nobody is surprised by a speaker
// restarting because of something they clicked yesterday.
const resumeAgentWindow = 30 * time.Minute

// intentStaleAfter is when a record is dropped entirely. A speaker that has not
// been reachable for this long is not mid-update any more, it is simply gone.
const intentStaleAfter = 14 * 24 * time.Hour

const maxIntents = 64

var intentMu sync.Mutex

func updateIntentPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ST Reborn", "update-intent.json"), nil
}

func loadUpdateIntentsFrom(path string) []updateIntent {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []updateIntent
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	out := list[:0]
	now := time.Now()
	for _, in := range list {
		if in.Host == "" || now.Sub(in.StartedAt) > intentStaleAfter {
			continue
		}
		out = append(out, in)
	}
	if len(out) > maxIntents {
		out = out[:maxIntents]
	}
	return out
}

func saveUpdateIntentsTo(path string, list []updateIntent) error {
	if len(list) > maxIntents {
		list = list[:maxIntents]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// upsertIntent records or refreshes what a speaker should be running.
func upsertIntent(list []updateIntent, in updateIntent) []updateIntent {
	for i := range list {
		if list[i].Host == in.Host && list[i].Port == in.Port {
			in.Attempts = list[i].Attempts
			list[i] = in
			return list
		}
	}
	return append(list, in)
}

func removeIntent(list []updateIntent, host string, port int) []updateIntent {
	out := list[:0]
	for _, in := range list {
		if in.Host == host && in.Port == port {
			continue
		}
		out = append(out, in)
	}
	return out
}

func findIntent(list []updateIntent, host string, port int) (updateIntent, bool) {
	for _, in := range list {
		if in.Host == host && in.Port == port {
			return in, true
		}
	}
	return updateIntent{}, false
}

// intentAction is what the reconcile decided for one speaker.
type intentAction int

const (
	intentNothing       intentAction = iota // speaker matches the intent
	intentRestoreEngine                     // engine missing, put it back (no reboot)
	intentResumeAgent                       // agent update was cut short and may still be resumed
	intentFlagOnly                          // cut short, but too long ago to act unasked
)

// reconcileIntent compares a speaker's reported state against its intent.
// Pure, so the decision table is testable without a speaker.
func reconcileIntent(in updateIntent, actualVersion string, engineState string, now time.Time) intentAction {
	agentDone := actualVersion != "" && actualVersion == in.TargetVersion
	engineOK := !in.WantEngine || engineState == "present"
	switch {
	case agentDone && engineOK:
		return intentNothing
	case agentDone && !engineOK:
		// The update landed; only the engine is missing. Safe to fix at any
		// time: the delivery is hot-swapped and never touches the speaker's
		// power state.
		return intentRestoreEngine
	case now.Sub(in.StartedAt) <= resumeAgentWindow:
		return intentResumeAgent
	default:
		return intentFlagOnly
	}
}

// RecordUpdateIntent is called when an update starts, so that whatever happens
// to this app process afterwards, the target is written down.
func (a *App) RecordUpdateIntent(host string, port int, targetVersion, deviceID, name string, wantEngine bool) {
	intentMu.Lock()
	defer intentMu.Unlock()
	path, err := updateIntentPath()
	if err != nil {
		return
	}
	list := upsertIntent(loadUpdateIntentsFrom(path), updateIntent{
		Host: host, Port: port, DeviceID: deviceID, Name: name,
		TargetVersion: targetVersion, WantEngine: wantEngine, StartedAt: time.Now(),
	})
	if err := saveUpdateIntentsTo(path, list); err != nil {
		a.logger.Warn("update intent: could not be written", "host", host, "err", err)
	}
}

// ClearUpdateIntent is called once a speaker has reached its target.
func (a *App) ClearUpdateIntent(host string, port int) {
	intentMu.Lock()
	defer intentMu.Unlock()
	path, err := updateIntentPath()
	if err != nil {
		return
	}
	_ = saveUpdateIntentsTo(path, removeIntent(loadUpdateIntentsFrom(path), host, port))
}

// PendingUpdateIntent reports what still has to happen for one speaker, for the
// frontend supervisor. Returns an empty action when there is nothing to do, so
// the common case costs one file read and no speaker traffic.
func (a *App) PendingUpdateIntent(host string, port int) map[string]string {
	intentMu.Lock()
	path, err := updateIntentPath()
	var list []updateIntent
	if err == nil {
		list = loadUpdateIntentsFrom(path)
	}
	intentMu.Unlock()
	in, ok := findIntent(list, host, port)
	if !ok {
		return map[string]string{"action": ""}
	}
	ver, verr := a.BoxAgentVersion(host, port)
	if verr != nil {
		return map[string]string{"action": "", "reason": "speaker not reachable"}
	}
	switch reconcileIntent(in, ver["version"], ver["goLibrespot"], time.Now()) {
	case intentNothing:
		a.ClearUpdateIntent(host, port)
		return map[string]string{"action": ""}
	case intentRestoreEngine:
		return map[string]string{"action": "engine", "target": in.TargetVersion, "name": in.Name}
	case intentResumeAgent:
		return map[string]string{"action": "agent", "target": in.TargetVersion, "name": in.Name}
	default:
		return map[string]string{"action": "flag", "target": in.TargetVersion, "name": in.Name}
	}
}

// otaRunning is set by the frontend for the whole duration of an install or
// update, so the window can refuse to close in the middle of one.
var otaRunning atomic.Bool

// SetOTARunning is called by the update flow as it starts and as it ends.
func (a *App) SetOTARunning(running bool) { otaRunning.Store(running) }

// beforeClose asks before closing while a speaker is mid-update.
//
// Returning true CANCELS the close. The dialog is the whole safeguard: since
// nothing is ever delivered in the background, a window closed at the wrong
// moment is exactly how a speaker ends up on a new agent with no Spotify
// engine, and that used to happen without a word.
func (a *App) beforeClose(ctx context.Context) bool {
	if !otaRunning.Load() {
		return false
	}
	res, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type:          runtime.QuestionDialog,
		Title:         "An update is still running",
		Message:       "A speaker is being updated right now. If you close ST Reborn, the update stops where it is and that speaker may be left without its Spotify engine until you run the update again.\n\nClose anyway?",
		Buttons:       []string{"Keep updating", "Close anyway"},
		DefaultButton: "Keep updating",
		CancelButton:  "Keep updating",
	})
	if err != nil {
		return false // never trap the user in a window because a dialog failed
	}
	return res != "Close anyway"
}

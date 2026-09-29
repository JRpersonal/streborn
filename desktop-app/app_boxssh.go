package main

// The speaker's SSH port as a switch, bound for the settings screen.
//
// Background in internal/webui/sshsetting.go. The short version: since the SSH
// opt-in a speaker keeps the port closed unless a marker on NAND says otherwise,
// and the only way to place that marker was over SSH. Two users on #1061 run SSH
// deliberately and asked for it to stay; the same switch serves both of them and
// the people who want it shut.

import (
	"encoding/json"
	"net/http"
)

// BoxSSHState is the speaker's SSH situation, as much as the settings screen
// needs to draw a switch and say something true underneath it.
type BoxSSHState struct {
	// Running is whether the port is open right now.
	Running bool `json:"running"`
	// Persistent is the switch's position: STR's own opt-in marker.
	Persistent bool `json:"persistent"`
	// BoseMarker means Bose's own remote_services file is on the speaker. The
	// port then stays open across restarts whatever the switch says, and STR
	// does not delete a file it did not write, so the UI has to say that
	// instead of promising to close it.
	BoseMarker bool `json:"boseMarker"`
	// Supported is false for a speaker whose agent predates the endpoint. The
	// screen hides the switch rather than showing a dead one.
	Supported bool `json:"supported"`
}

// BoxSSHStatus reads the speaker's SSH state. An agent too old to know the
// endpoint reports Supported false rather than an error, because "this speaker
// cannot be asked" is a normal answer during a staggered fleet update, not a
// failure worth a red box on screen.
func (a *App) BoxSSHStatus(host string, port int) (BoxSSHState, error) {
	resp, err := a.boxDo(host, port, http.MethodGet, "/api/agent/ssh", "", "")
	if err != nil {
		return BoxSSHState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return BoxSSHState{Supported: false}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return BoxSSHState{}, readHTTPError(resp)
	}
	var out BoxSSHState
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return BoxSSHState{}, err
	}
	out.Supported = true
	return out, nil
}

// SetBoxSSHPersistent turns the speaker's SSH port on or off across restarts.
// Turning it on opens the port immediately as well, so the switch does something
// visible. Turning it off leaves any current session alone and takes effect from
// the speaker's next restart, which is what the screen says.
func (a *App) SetBoxSSHPersistent(host string, port int, on bool) (BoxSSHState, error) {
	body, err := json.Marshal(map[string]bool{"persistent": on})
	if err != nil {
		return BoxSSHState{}, err
	}
	resp, err := a.boxDo(host, port, http.MethodPost, "/api/agent/ssh", "application/json", string(body))
	if err != nil {
		return BoxSSHState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return BoxSSHState{}, readHTTPError(resp)
	}
	var out BoxSSHState
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return BoxSSHState{}, err
	}
	out.Supported = true
	a.logger.Info("ssh setting: changed for this speaker", "host", host, "persistent", out.Persistent, "running", out.Running)
	return out, nil
}

package webui

// The speaker's SSH port, as something the user can decide rather than a file
// path in a warning message.
//
// Until v0.9.91 a speaker opened SSH on every boot whether anybody wanted it or
// not. That was a bug and it is fixed: the port now stays closed unless a marker
// on NAND says otherwise. But the only way to place that marker was over SSH,
// which is a circle, and the app's warning told people to "remove
// /mnt/nv/remote_services" as though that were an instruction a person could
// follow.
//
// Two users asked for the other direction on #1061: they run SSH deliberately
// and want it to stay. Both needs are the same switch.
//
// Scope, deliberately narrow: this manages STR's OWN marker. Bose's
// /mnt/nv/remote_services is somebody else's file and STR does not delete it; it
// is reported instead, because while it is there the port stays open no matter
// what this switch says.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// strSSHMarker is the opt-in file run.sh and the agent bootstrap read. On NAND,
// so it survives a reboot, which is the whole point of it.
func strSSHMarker() string {
	return filepath.Join(nvRoot, "streborn", "enable-ssh")
}

// boseSSHMarker is Bose's own gate, which STR reads and never writes.
func boseSSHMarker() string {
	return filepath.Join(nvRoot, "remote_services")
}

// transientSSHMarker is the tmpfs file handleAgentEnableSSH writes. It does
// not survive a reboot, which is the whole point of it.
func transientSSHMarker() string {
	return "/tmp/remote_services"
}

// stopSSHD kills the running daemon. Best-effort: a speaker that cannot stop
// it closes the port on its next restart anyway, since the marker is gone.
func stopSSHD(logger *slog.Logger) {
	if out, err := exec.Command("killall", "sshd").CombinedOutput(); err != nil {
		logger.Info("ssh setting: could not stop sshd, it closes on the next restart",
			"err", err, "out", strings.TrimSpace(string(out)))
	}
}

// sshState is what the app needs to draw the switch and say something true
// underneath it.
type sshState struct {
	// Running is whether sshd is alive right now.
	Running bool `json:"running"`
	// Persistent is STR's own marker: the switch's position.
	Persistent bool `json:"persistent"`
	// BoseMarker means Bose's remote_services file is present. The port then
	// stays open across reboots even with the switch off, and STR will not
	// remove that file, so the UI has to say so rather than promise otherwise.
	BoseMarker bool `json:"boseMarker"`
}

func currentSSHState() sshState {
	return sshState{
		Running:    sshdRunning(),
		Persistent: fileExists(strSSHMarker()),
		BoseMarker: fileExists(boseSSHMarker()),
	}
}

// handleAgentSSH reports the state on GET and sets STR's marker on POST.
// LAN-only, like every other administrative endpoint here.
func (s *Server) handleAgentSSH(w http.ResponseWriter, r *http.Request) {
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "ssh settings only allowed from LAN", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, currentSSHState())
	case http.MethodPost:
		// Require a JSON content type. The agent has no CSRF token anywhere, so a
		// page the owner happens to visit on the same LAN can POST to it; this is
		// repo-wide and older than this endpoint, but this endpoint is the one that
		// flips a reboot-surviving root-SSH marker, which makes it worth closing
		// here rather than waiting for the general fix. A plain HTML form can only
		// send urlencoded, multipart or text/plain bodies, and anything else forces
		// a CORS preflight that a foreign origin does not survive.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "ssh settings need a JSON body", http.StatusUnsupportedMediaType)
			return
		}
		var body struct {
			Persistent *bool `json:"persistent"`
			// CloseNow shuts the port for this boot: it removes the transient
			// tmpfs marker and stops sshd. STR uses it to take back the SSH it
			// opened for a stick refresh when the update then failed before its
			// reboot, which is what used to close the port by itself.
			CloseNow bool `json:"closeNow"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || (body.Persistent == nil && !body.CloseNow) {
			http.Error(w, "expected {\"persistent\": true|false} or {\"closeNow\": true}", http.StatusBadRequest)
			return
		}
		if body.CloseNow {
			// Never touch the NAND markers here: closeNow is about this boot, and
			// a speaker deliberately opted in must stay open across it.
			if fileExists(strSSHMarker()) || fileExists(boseSSHMarker()) {
				s.logger.Info("ssh setting: close requested, but this speaker is opted in, so the port stays open")
				writeJSON(w, http.StatusOK, currentSSHState())
				return
			}
			if err := os.Remove(transientSSHMarker()); err != nil && !os.IsNotExist(err) {
				s.logger.Warn("ssh setting: could not remove the transient marker", "err", err)
			}
			stopSSHD(s.logger)
			s.logger.Info("ssh setting: the port opened for this boot is closed again")
			writeJSON(w, http.StatusOK, currentSSHState())
			return
		}
		if *body.Persistent {
			if err := os.MkdirAll(filepath.Dir(strSSHMarker()), 0o755); err != nil {
				s.logger.Warn("ssh setting: could not create the marker directory", "err", err)
				http.Error(w, "could not write the setting to the speaker", http.StatusInternalServerError)
				return
			}
			if err := os.WriteFile(strSSHMarker(), nil, 0o644); err != nil {
				s.logger.Warn("ssh setting: could not write the marker", "err", err)
				http.Error(w, "could not write the setting to the speaker", http.StatusInternalServerError)
				return
			}
			// Open it now as well, so the switch does something the user can
			// see instead of only taking effect after the next restart.
			ensureSSHDRunning(s.logger)
			s.logger.Info("ssh setting: SSH is now on for this speaker and stays on across restarts")
		} else {
			if err := os.Remove(strSSHMarker()); err != nil && !os.IsNotExist(err) {
				s.logger.Warn("ssh setting: could not remove the marker", "err", err)
				http.Error(w, "could not write the setting to the speaker", http.StatusInternalServerError)
				return
			}
			// sshd is deliberately left running: killing the session the user
			// may be sitting in is not this switch's job. It stays closed from
			// the next restart on, which is what the UI says.
			s.logger.Info("ssh setting: SSH will stay closed from the next restart of this speaker")
		}
		writeJSON(w, http.StatusOK, currentSSHState())
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

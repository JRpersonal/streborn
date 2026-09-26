package webui

import (
	"net/http"
)

// handleRestoreCloudURL puts STR back in charge of the speaker's cloud address.
//
// Why this exists next to handleRemoveConflictingMod: that one cleans up the
// FILES another tool left behind. This is the other shape, and it is the one a
// rival app causes while it is simply running.
//
// Measured on a five-speaker fleet on 2026-09-26, minutes after the ST Remote
// Pro iOS app was started for the first time: every speaker's LIVE margeURL had
// become https://stremotepro.com/api/bose-cloud, every speaker restarted, and
// afterwards none of them was paired with STR any more, so the six hardware
// keys were dead. The override file on NAND was untouched and still correct.
//
// That is the whole trap. The repair path STR had rewrites the file, the file
// was never wrong, so it healed nothing and then reported success. The firmware
// reads that file once, at boot, so the actual cure is a restart, and it works:
// all five came back stock and paired after one reboot each.
//
// This endpoint therefore does the file half (a no-op on the common shape) and
// reports honestly whether a restart is what is still owed. The restart itself
// is left to the caller, so the desktop app can sequence a whole fleet and
// verify each speaker afterwards instead of every box rebooting at once.
func (s *Server) handleRestoreCloudURL(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "cloud URL restore only allowed from LAN", http.StatusForbidden)
		return
	}
	heal := healSDKCloudURLs()
	live := liveForeignCloudURL()
	files := foreignCloudURLFiles()
	s.logger.Info("cloud URL restore requested",
		"healed", heal.Healed, "restartPending", heal.RestartPending,
		"filesForeign", files, "liveForeign", live, "note", heal.Note)
	// A restart only cures anything once the CONFIG is right, because that is
	// what the firmware re-reads at boot. So the answer is in two parts.
	//
	// rebootRequired is keyed on the live value rather than on heal's own
	// RestartPending: heal returns early with neither flag set when it finds no
	// SDK config at all, and a speaker whose firmware names a foreign host
	// would then be told nothing was owed. That cannot happen on a real
	// speaker, where the rootfs copy always exists, which is exactly why it
	// would have sat here unnoticed.
	rebootHelps := files == ""
	out := map[string]any{
		"status": "ok",
		// healed says the file was rewritten. On the runtime-hijack shape it is
		// false and that is correct, not a failure.
		"healed": heal.Healed,
		// rebootRequired is the one the caller acts on: the configuration is
		// right and only the running firmware still names the foreign host.
		"rebootRequired": rebootHelps && (heal.Healed || heal.RestartPending || live != ""),
	}
	if !rebootHelps {
		// The repair did not finish, so a restart would bring the same address
		// back. Say so instead of sending the user through a pointless reboot.
		out["repairIncomplete"] = true
	}
	if heal.Path != "" {
		out["file"] = heal.Path
	}
	if files != "" {
		out["filesForeign"] = files
	}
	if live != "" {
		out["liveForeign"] = live
	}
	if heal.Note != "" {
		out["note"] = heal.Note
	}
	writeJSON(w, http.StatusOK, out)
}

package webui

import (
	"net/http"
	"os"
)

// handleForeignInfluence serves the findings, and undoes one of them.
//
// GET is the safe haven view: everything another tool has done to this speaker
// or left on it, each row saying honestly whether STR can take it back.
//
// POST {"id":"<finding id>"} undoes exactly ONE row. One at a time on purpose:
// a single "clean everything" button that quietly skipped the half it could not
// do is the mistake this whole area keeps making, and it is what #986 cost
// three days for.
func (s *Server) handleForeignInfluence(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		findings := collectForeignInfluence()
		s.noteForeignInfluence(findings)
		writeJSON(w, http.StatusOK, map[string]any{
			"findings": findings,
			"summary":  foreignSummary(findings),
		})
	case http.MethodPost:
		if !isLocalLAN(r.RemoteAddr) {
			http.Error(w, "undo only allowed from LAN", http.StatusForbidden)
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		if !decodeJSONRequest(w, r, 256, &req) {
			return
		}
		s.undoForeignFinding(w, req.ID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) undoForeignFinding(w http.ResponseWriter, id string) {
	before := collectForeignInfluence()
	var target *ForeignFinding
	for i := range before {
		if before[i].ID == id {
			target = &before[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "nothing found with id "+id, http.StatusNotFound)
		return
	}
	out := map[string]any{"status": "ok", "id": id, "undo": target.Undo}

	switch target.Undo {
	case undoReboot:
		// The configuration is already right; only the running firmware still
		// names the other host, and it reads that file once, at boot. The
		// caller does the restart so it can sequence a fleet.
		out["rebootRequired"] = true

	case undoRewrite:
		heal := healSDKCloudURLs()
		out["healed"] = heal.Healed
		out["rebootRequired"] = heal.Healed || heal.RestartPending
		if heal.Note != "" {
			out["note"] = heal.Note
		}
		if !heal.Healed && heal.Note != "" {
			// Say it failed rather than letting a note hide under an "ok".
			out["status"] = "failed"
		}

	case undoDelete:
		removed, err := s.removeForeignFiles(target)
		out["removed"] = removed
		if err != nil {
			out["status"] = "failed"
			out["error"] = err.Error()
		}

	case undoModRemove:
		// Handled by the older, more careful endpoint. Saying so beats either
		// duplicating its file list or silently doing half of it here.
		out["status"] = "use-endpoint"
		out["endpoint"] = "/api/box/remove-conflicting-mod"

	default:
		// undoNone: the row exists to be explained, not actioned.
		out["status"] = "not-possible"
		out["note"] = target.Note
	}

	after := collectForeignInfluence()
	s.noteForeignInfluence(after)
	// The honest part: what is STILL there. A row that reported success while
	// the speaker was unchanged is the failure mode this whole file exists to
	// stop, so the answer always carries the current state.
	out["remaining"] = foreignSummary(after)
	s.logger.Info(foreignLogPrefix+" undo requested", "id", id, "undo", target.Undo,
		"status", out["status"], "remaining", out["remaining"])
	writeJSON(w, http.StatusOK, out)
}

// removeForeignFiles deletes the inert leftovers behind one finding.
func (s *Server) removeForeignFiles(f *ForeignFinding) ([]string, error) {
	removed := []string{}
	var paths []string
	switch f.ID {
	case "mod-backups":
		paths = octBackupFiles()
	case "mod-hosts-backup":
		paths = []string{octHostsBackupPath}
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		removed = append(removed, p)
	}
	return removed, nil
}

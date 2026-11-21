package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JRpersonal/streborn/internal/presets"
)

const defaultPresetRowsPath = "/mnt/nv/streborn/preset-rows"

func (s *Server) presetRowsFilePath() string {
	if s.presetRowsPath != "" {
		return s.presetRowsPath
	}
	return defaultPresetRowsPath
}

// PresetRows is the speaker's configured number of active preset rows. An
// unset or damaged setting keeps the existing two-row default; users can
// choose any supported row count in speaker settings.
func (s *Server) PresetRows() int {
	b, err := os.ReadFile(s.presetRowsFilePath())
	if err == nil {
		if n, parseErr := strconv.Atoi(strings.TrimSpace(string(b))); parseErr == nil && n >= 1 && n <= presets.Rows {
			return n
		}
	}
	return presets.DefaultRows
}

func (s *Server) handlePresetRows(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPut) {
		return
	}
	var in struct {
		Rows int `json:"rows"`
	}
	if !decodeOptionalJSONRequest(w, r, 256, &in) {
		return
	}
	if in.Rows < 1 || in.Rows > presets.Rows {
		http.Error(w, "rows must be between 1 and "+strconv.Itoa(presets.Rows), http.StatusBadRequest)
		return
	}
	path := s.presetRowsFilePath()
	if err := writeFileAtomic(path, []byte(strconv.Itoa(in.Rows)+"\n")); err != nil {
		s.logger.Warn("preset rows: could not persist", "path", filepath.Base(path), "err", err)
		http.Error(w, "could not save preset rows", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"rows": in.Rows})
}

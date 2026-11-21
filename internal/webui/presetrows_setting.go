package webui

import "github.com/JRpersonal/streborn/internal/presets"

// PresetRows is the fixed number of six-key rows this agent supports.
func (s *Server) PresetRows() int { return presets.Rows }

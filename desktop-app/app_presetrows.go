package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SetPresetRows configures how many six-key rows this speaker uses. Each
// additional row is reached by another quick press of the same hardware key.
func (a *App) SetPresetRows(host string, port int, rows int) error {
	if rows < 1 || rows > presetMaxRows {
		return fmt.Errorf("preset rows must be between 1 and %d", presetMaxRows)
	}
	body, err := json.Marshal(map[string]int{"rows": rows})
	if err != nil {
		return err
	}
	resp, err := a.boxDo(host, port, http.MethodPut, "/api/presets/rows", "application/json", string(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

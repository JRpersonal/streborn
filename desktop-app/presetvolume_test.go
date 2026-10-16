package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SetPresetVolume stores a key's own volume (#1323) on the rename verb, and an
// agent from before the per-key volume is named instead of looking like a
// success.
func TestSetPresetVolume(t *testing.T) {
	cases := []struct {
		name    string
		agent   func(w http.ResponseWriter, body map[string]any)
		wantErr string
	}{
		{"current agent keeps it", func(w http.ResponseWriter, body map[string]any) {
			_ = json.NewEncoder(w).Encode(Preset{Slot: 2, Name: "News", Volume: int(body["volume"].(float64))})
		}, ""},
		{"old agent wants a name", func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"code":"name-empty"}`))
		}, errPresetVolumeUnsupported},
		{"old agent ignores the field", func(w http.ResponseWriter, _ map[string]any) {
			_ = json.NewEncoder(w).Encode(Preset{Slot: 2, Name: "News"})
		}, errPresetVolumeUnsupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotMethod, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				b, _ := io.ReadAll(r.Body)
				var body map[string]any
				_ = json.Unmarshal(b, &body)
				if _, hasName := body["name"]; hasName {
					t.Errorf("the volume write carried a name: %s", b)
				}
				c.agent(w, body)
			}))
			defer srv.Close()
			a := moveApp(t)
			err := a.SetPresetVolume("127.0.0.1", listenPort(t, srv), 2, 5)
			if gotMethod != http.MethodPatch || gotPath != presetAPIPath+"/2" {
				t.Errorf("request = %s %s, want PATCH %s/2", gotMethod, gotPath, presetAPIPath)
			}
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("err = %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("err = %v, want %s", err, c.wantErr)
			}
		})
	}
}

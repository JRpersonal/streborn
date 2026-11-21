package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The debug state reports the firmware's source store by service and login
// type only. Run through the real handler, masked and raw, because the raw
// form is what the desktop bundle fetches and the section is a Go struct
// (the anonymiser's walker would not see inside it before the round trip).
func TestDebugStateReportsSourcesFileWithoutTheLogin(t *testing.T) {
	dir := t.TempDir()
	store := `<sources>` +
		`<source displayName="Listener Name" secret="deezer-secret-value" secretType="token"><sourceKey type="DEEZER" account="0000000000"/></source>` +
		`<source secret="" secretType=""><sourceKey type="LOCAL_INTERNET_RADIO" account=""/></source>` +
		`</sources>`
	if err := os.WriteFile(filepath.Join(dir, "Sources.xml"), []byte(store), 0o600); err != nil {
		t.Fatal(err)
	}
	old := boseSourcesGlob
	boseSourcesGlob = filepath.Join(dir, "Sources.xml")
	t.Cleanup(func() { boseSourcesGlob = old })

	s := New(":0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, q := range []string{"", "?raw=1"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/debug/state"+q, nil)
		r.RemoteAddr = "127.0.0.1:5000"
		s.handleDebugState(w, r)
		body := w.Body.String()
		for _, leak := range []string{"deezer-secret-value", "0000000000", "Listener Name"} {
			if strings.Contains(body, leak) {
				t.Fatalf("query %q: debug state carries %q", q, leak)
			}
		}
		var state map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatalf("query %q: not JSON: %v", q, err)
		}
		var got struct {
			Present bool `json:"present"`
			Entries []struct {
				Source     string `json:"source"`
				SecretType string `json:"secretType"`
				HasSecret  bool   `json:"hasSecret"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(state["bose_sources_file"], &got); err != nil {
			t.Fatalf("query %q: section = %s: %v", q, state["bose_sources_file"], err)
		}
		if !got.Present || len(got.Entries) != 2 ||
			got.Entries[0].Source != "DEEZER" || got.Entries[0].SecretType != "token" || !got.Entries[0].HasSecret ||
			got.Entries[1].Source != "LOCAL_INTERNET_RADIO" || got.Entries[1].HasSecret {
			t.Fatalf("query %q: section = %s", q, state["bose_sources_file"])
		}
	}
}

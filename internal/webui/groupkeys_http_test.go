package webui

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/groupkeys"
)

// TestGroupKeysEndpoint: GET returns the stored document, a PUT with a defect
// is refused with the reason, a valid PUT persists and answers with the
// stored document, and a caller off the LAN is refused.
func TestGroupKeysEndpoint(t *testing.T) {
	st, err := groupkeys.Load(filepath.Join(t.TempDir(), "group-keys.json"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: slog.Default(), groupKeys: st}

	get := func(remote string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/groupkeys", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		s.handleGroupKeys(w, r)
		return w
	}
	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/groupkeys", strings.NewReader(body))
		r.RemoteAddr = "192.168.1.20:5555"
		w := httptest.NewRecorder()
		s.handleGroupKeys(w, r)
		return w
	}

	if w := get("192.168.1.20:5555"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"templates"`) {
		t.Fatalf("empty GET: %d %s", w.Code, w.Body.String())
	}
	if w := get("203.0.113.9:5555"); w.Code != http.StatusForbidden {
		t.Fatalf("off-LAN GET must be refused, got %d", w.Code)
	}
	if w := put(`{"templates":[{"name":"","master":{"ip":"192.0.2.1"},"members":[{"ip":"192.0.2.2"}]}]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("defective PUT must be refused, got %d %s", w.Code, w.Body.String())
	}
	body := `{"templates":[{"name":"Evening","master":{"deviceID":"AAAA","ip":"192.0.2.1"},"members":[{"deviceID":"BBBB","ip":"192.0.2.2","name":"Kitchen"}],"permanent":true}],"bindings":{"thumbsUp":"Evening"}}`
	w := put(body)
	if w.Code != http.StatusOK {
		t.Fatalf("valid PUT: %d %s", w.Code, w.Body.String())
	}
	var got groupkeys.Document
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Templates) != 1 || got.Bindings["thumbsUp"] != "Evening" || !got.Templates[0].Permanent {
		t.Fatalf("stored document %+v", got)
	}
	if !st.Bound("thumbsUp") {
		t.Fatal("the store must carry the binding after the PUT")
	}
}

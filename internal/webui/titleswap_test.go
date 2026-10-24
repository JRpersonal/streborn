package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/streamproxy"
)

func titleSwapServer(t *testing.T) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sp := streamproxy.New(nil, logger)
	sp.SetTitleSwapPath(filepath.Join(t.TempDir(), "title-swap.json"))
	return &Server{logger: logger, streamProxy: sp}
}

func postTitleSwap(s *Server, body, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/stream/title-swap", strings.NewReader(body))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	s.handleTitleSwap(w, r)
	return w
}

func TestHandleTitleSwap(t *testing.T) {
	s := titleSwapServer(t)
	const st = "http://192.0.2.1/ndr1mv"
	const lan = "192.168.1.20:5555"

	// Nothing playing and no station named: nothing to act on.
	if w := postTitleSwap(s, `{"swap":true}`, lan); w.Code != http.StatusConflict {
		t.Errorf("no station: code %d, want 409", w.Code)
	}

	w := postTitleSwap(s, `{"swap":true,"station":"`+st+`"}`, lan)
	if w.Code != http.StatusOK {
		t.Fatalf("POST: code %d body %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["station"] != st || out["swapped"] != true {
		t.Errorf("POST answer = %v", out)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/stream/title-swap", nil)
	w = httptest.NewRecorder()
	s.handleTitleSwap(w, r)
	var got struct {
		Stations []string `json:"stations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Stations) != 1 || got.Stations[0] != st {
		t.Errorf("GET = %s", w.Body.String())
	}

	// Writes only from the LAN.
	if w := postTitleSwap(s, `{"swap":false,"station":"`+st+`"}`, "203.0.113.9:5555"); w.Code != http.StatusForbidden {
		t.Errorf("non-LAN POST: code %d, want 403", w.Code)
	}
}

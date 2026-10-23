package webui

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/updatesig"
)

// The upload services behind POST /api/agent/update and /api/agent/sidecar.
//
// The self-update used to feed its verified download to the HTTP handlers
// through a loopback request and a memResponse, reading the status and the
// body text back out. Now installDownloaded hands an upload to the service
// and maps its refusal to the same status and text; these tests pin that
// mapping and the handlers' wire for every exit reachable without a NAND.

// softfloatELF is a header that passes validSoftfloatARMELF, padded to n.
func softfloatELF(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 1, 1})
	b[18], b[19] = 40, 0 // EM_ARM
	return b
}

func TestUploadRefusalBodyTextMatchesTheWire(t *testing.T) {
	t.Parallel()
	text := textUploadRefusal(http.StatusInsufficientStorage, "NAND full")
	if got := text.bodyText(); got != "NAND full" {
		t.Errorf("text body = %q", got)
	}
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	updateInFlight.Store(false)
	if ref := s.engineBlockedByUpdate(); ref != nil {
		t.Fatalf("no update running, yet refused: %+v", ref)
	}
	js := &uploadRefusal{Status: http.StatusConflict, Error: "busy", JSON: map[string]string{"code": "update-in-flight", "error": "busy"}}
	if got := js.bodyText(); got != `{"code":"update-in-flight","error":"busy"}` {
		t.Errorf("json body = %q", got)
	}
	// What the handler renders is the same text plus the newline the
	// encoders add, which every reader trims.
	w := httptest.NewRecorder()
	writeUploadRefusal(w, js)
	if w.Code != 409 || strings.TrimSpace(w.Body.String()) != js.bodyText() {
		t.Errorf("rendered = %d %q", w.Code, w.Body.String())
	}
}

func TestInstalledBodiesAreTheOldAnswers(t *testing.T) {
	t.Parallel()
	if got := (agentInstalled{}).body(); len(got) != 2 || got["status"] != "ok" || got["action"] != "reboot" {
		t.Errorf("flash body = %v", got)
	}
	if got := (agentInstalled{RAMStaged: true}).body(); got["mode"] != "ram-staged" || got["action"] != "reboot" {
		t.Errorf("ram-staged body = %v", got)
	}
	if got := (engineInstalled{Reloaded: true}).body(); got["status"] != "ok" || got["reloaded"] != "true" {
		t.Errorf("engine body = %v", got)
	}
	if got := (engineInstalled{}).body(); got["reloaded"] != "false" {
		t.Errorf("engine body without reload = %v", got)
	}
}

// installDownloaded reports a refusal the way the loopback request used to:
// the status and the body text the speaker would have sent.
func TestInstallDownloadedReportsTheRefusalAsStatusAndText(t *testing.T) {
	agent := softfloatELF(4096)
	s, base := selfUpdateFixture(t, "v1.0.13", agent, []byte("engine-binary"), nil)
	url := base + "download/v1.0.13/" + updatesig.AgentFile

	code, body, err := s.installDownloaded(url, fileOf(agent), func(up upload) *uploadRefusal {
		_, _ = io.ReadAll(up.Body)
		return textUploadRefusal(http.StatusInsufficientStorage, "NAND full: 3 MB free")
	})
	if err != nil || code != http.StatusInsufficientStorage || body != "NAND full: 3 MB free" {
		t.Errorf("text refusal: code=%d body=%q err=%v", code, body, err)
	}

	code, body, err = s.installDownloaded(url, fileOf(agent), func(up upload) *uploadRefusal {
		_, _ = io.ReadAll(up.Body)
		return &uploadRefusal{Status: http.StatusConflict, JSON: map[string]string{"code": "update-in-flight"}}
	})
	if err != nil || code != http.StatusConflict || body != `{"code":"update-in-flight"}` {
		t.Errorf("json refusal: code=%d body=%q err=%v", code, body, err)
	}

	code, body, err = s.installDownloaded(url, fileOf(agent), func(up upload) *uploadRefusal {
		_, _ = io.ReadAll(up.Body)
		return nil
	})
	if err != nil || code != http.StatusOK || body != "" {
		t.Errorf("accepted: code=%d body=%q err=%v", code, body, err)
	}
}

// The engine service refuses while an update is installing, with the same
// code the handler sends, so the self-update's own engine fetch and the
// desktop app's push see one answer.
func TestInstallEngineUploadRefusesWhileAnUpdateIsInstalling(t *testing.T) {
	updateInFlight.Store(true)
	t.Cleanup(func() { updateInFlight.Store(false) })
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	_, ref := s.installEngineUpload(upload{Body: bytes.NewReader(softfloatELF(2048)), Size: 2048, Remote: "127.0.0.1:1"})
	if ref == nil || ref.Status != http.StatusConflict || !strings.Contains(ref.bodyText(), "update-in-flight") {
		t.Fatalf("refusal = %+v, want 409 update-in-flight", ref)
	}
}

// The wire of POST /api/agent/update for every exit before the NAND is
// touched. The desktop app retries on every 5xx and shows these texts, and
// the ELF gate (#302) is the last thing between a wrong build and a bricked
// speaker, so its wording is pinned too.
func TestHandleAgentUpdateWire(t *testing.T) {
	t.Parallel()
	hardFloat := softfloatELF(4096)
	hardFloat[37] = 0x04 // EF_ARM_ABI_FLOAT_HARD
	wrongArch := softfloatELF(4096)
	wrongArch[18] = 3 // EM_386
	elf64 := softfloatELF(4096)
	elf64[4] = 2 // ELFCLASS64
	cases := []struct {
		name   string
		method string
		body   []byte
		length int64
		status int
		want   string
	}{
		{"method", http.MethodGet, nil, 0, http.StatusMethodNotAllowed, "method not allowed\n"},
		{"announced too big", http.MethodPost, softfloatELF(64), 31 << 20, http.StatusRequestEntityTooLarge, errUploadTooBig.Error() + "\n"},
		{"empty body", http.MethodPost, nil, 0, http.StatusBadRequest, "binary too small to be an ELF\n"},
		{"not an ELF", http.MethodPost, bytes.Repeat([]byte("<html>"), 20), 120, http.StatusBadRequest, "not an ELF binary\n"},
		{"64-bit", http.MethodPost, elf64, 4096, http.StatusBadRequest, "not a 32-bit little-endian ARM binary\n"},
		{"wrong architecture", http.MethodPost, wrongArch, 4096, http.StatusBadRequest, "wrong architecture; STR speakers need an ARM (GOARM=5) build\n"},
		{"hard-float", http.MethodPost, hardFloat, 4096, http.StatusBadRequest, "hard-float ARM binary refused; STR speakers need a softfloat (GOARM=5) build\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{logger: slog.New(slog.DiscardHandler)}
			r := httptest.NewRequest(c.method, "/api/agent/update", bytes.NewReader(c.body))
			r.ContentLength = c.length
			r.RemoteAddr = "127.0.0.1:1"
			w := httptest.NewRecorder()
			s.handleAgentUpdate(w, r)
			if w.Code != c.status {
				t.Errorf("status = %d, want %d", w.Code, c.status)
			}
			if got := w.Body.String(); got != c.want {
				t.Errorf("body = %q, want %q", got, c.want)
			}
		})
	}
	// The LAN gate's body names the caller and the speaker's subnets, which
	// depend on the host; the status is what the app branches on.
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	r := httptest.NewRequest(http.MethodPost, "/api/agent/update", bytes.NewReader(softfloatELF(4096)))
	r.RemoteAddr = "203.0.113.9:4"
	w := httptest.NewRecorder()
	s.handleAgentUpdate(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("off-LAN status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
}

// The wire of POST /api/agent/sidecar for the exits before the NAND. Not
// parallel: updateInFlight is package state the guard tests flip too.
func TestHandleAgentSidecarWire(t *testing.T) {
	updateInFlight.Store(false)
	cases := []struct {
		name     string
		method   string
		body     []byte
		inFlight bool
		status   int
		want     string
	}{
		{"update in flight wins over the method", http.MethodGet, nil, true, http.StatusConflict,
			`{"code":"update-in-flight","error":"An update is installing on this speaker. Deliver the Spotify engine again once the speaker reports the new version."}` + "\n"},
		{"method", http.MethodGet, nil, false, http.StatusMethodNotAllowed, "method not allowed\n"},
		{"empty body", http.MethodPost, nil, false, http.StatusBadRequest, "not an ELF binary\n"},
		{"not an ELF", http.MethodPost, []byte("<html>web ui</html>"), false, http.StatusBadRequest, "not an ELF binary\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			updateInFlight.Store(c.inFlight)
			t.Cleanup(func() { updateInFlight.Store(false) })
			s := &Server{logger: slog.New(slog.DiscardHandler)}
			r := httptest.NewRequest(c.method, "/api/agent/sidecar", bytes.NewReader(c.body))
			r.RemoteAddr = "127.0.0.1:1"
			w := httptest.NewRecorder()
			s.handleAgentSidecar(w, r)
			if w.Code != c.status {
				t.Errorf("status = %d, want %d", w.Code, c.status)
			}
			if got := w.Body.String(); got != c.want {
				t.Errorf("body = %q, want %q", got, c.want)
			}
		})
	}
}

package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The agent hands the speaker's own update form on unchanged: same multipart
// body, same field, same file name, and answers with the page's status.
func TestBoseFirmwareHandsTheUploadOn(t *testing.T) {
	var gotName string
	var gotBytes int
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "<title>Bose SoundTouch Bonjour Update</title>")
			return
		}
		f, h, err := r.FormFile("filename")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(f)
		gotName, gotBytes = h.Filename, len(b)
		_, _ = io.WriteString(w, "ok")
	}))
	defer page.Close()
	old := boseUpdatePageURL
	boseUpdatePageURL = page.URL + "/update.html"
	t.Cleanup(func() { boseUpdatePageURL = old })
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("filename", "Update_ti_27.0.6.46330.5043500.scm.stu")
	_, _ = fw.Write(bytes.Repeat([]byte{0xAB}, 100000))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/bose-firmware", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.RemoteAddr = "192.168.1.20:5000"
	rr := httptest.NewRecorder()
	s.handleBoseFirmware(rr, req)
	if rr.Code != http.StatusOK || gotName != "Update_ti_27.0.6.46330.5043500.scm.stu" || gotBytes != 100000 {
		t.Fatalf("code %d, page got %q %d bytes", rr.Code, gotName, gotBytes)
	}

	// GET reports the page
	req = httptest.NewRequest(http.MethodGet, "/api/agent/bose-firmware", nil)
	req.RemoteAddr = "192.168.1.20:5000"
	rr = httptest.NewRecorder()
	s.handleBoseFirmware(rr, req)
	var got map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got["available"] != true {
		t.Fatalf("GET %s", rr.Body.String())
	}

	// not multipart: refused before anything reaches the page
	req = httptest.NewRequest(http.MethodPost, "/api/agent/bose-firmware", bytes.NewReader([]byte("x")))
	req.RemoteAddr = "192.168.1.20:5000"
	rr = httptest.NewRecorder()
	s.handleBoseFirmware(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("plain body: %d", rr.Code)
	}
}

// A page that reads the whole file and hangs up without answering has the
// file: the agent reports it as delivered, not as a failure.
func TestBoseFirmwareClosedWithoutAnswerIsDelivered(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		c, _, _ := w.(http.Hijacker).Hijack()
		_ = c.Close()
	}))
	defer page.Close()
	old := boseUpdatePageURL
	boseUpdatePageURL = page.URL + "/update.html"
	t.Cleanup(func() { boseUpdatePageURL = old })
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	body := bytes.Repeat([]byte{1}, 50000)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/bose-firmware", bytes.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.RemoteAddr = "192.168.1.20:5000"
	rr := httptest.NewRecorder()
	s.handleBoseFirmware(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code %d (%s), want 202", rr.Code, rr.Body.String())
	}
}

func TestBoseFirmwareRefusesUnknownLength(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/bose-firmware", io.NopCloser(bytes.NewReader([]byte("x"))))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.RemoteAddr = "192.168.1.20:5000"
	rr := httptest.NewRecorder()
	s.handleBoseFirmware(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code %d, want 413", rr.Code)
	}
}

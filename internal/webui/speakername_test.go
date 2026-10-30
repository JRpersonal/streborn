package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// syncBuffer is a log sink the background probe can write to while the test
// reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func nameTestServer() (*Server, *syncBuffer) {
	buf := &syncBuffer{}
	return &Server{logger: slog.New(slog.NewTextHandler(buf, nil))}, buf
}

func agentVersionFields(t *testing.T, s *Server) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAgentVersion(w, httptest.NewRequest(http.MethodGet, "/api/agent/version", nil))
	var av map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &av); err != nil {
		t.Fatalf("agent version: %v (%s)", err, w.Body.String())
	}
	return av
}

// A name with "&" (or stored as invalid UTF-8) is recognised, logged once and
// reported to the apps; a rename to a clean name clears it.
func TestNoteBoxNameFlagsUnsafeName(t *testing.T) {
	s, logs := nameTestServer()

	s.NoteBoxName("Kitchen", false)
	if unsafe, _ := s.SpeakerNameIssue(); unsafe {
		t.Fatal("a clean name was flagged")
	}
	if av := agentVersionFields(t, s); av["speakerNameUnsafe"] != "" {
		t.Fatalf("speakerNameUnsafe = %q for a clean name", av["speakerNameUnsafe"])
	}

	s.NoteBoxName("Test&B", false)
	s.NoteBoxName("Test&B", false) // the 30 s poll: no second WARN
	if unsafe, _ := s.SpeakerNameIssue(); !unsafe {
		t.Fatal("a name with & was not flagged")
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Fatalf("want exactly one WARN, got %d:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "suggested=Test+B") {
		t.Errorf("the WARN does not name the fixed name:\n%s", logs.String())
	}
	if av := agentVersionFields(t, s); av["speakerNameUnsafe"] != "true" {
		t.Fatalf("speakerNameUnsafe = %q, want true", av["speakerNameUnsafe"])
	}

	// A name the firmware stores as Latin-1 reads back fine but is just as bad.
	s.NoteBoxName("Küche", true)
	if unsafe, _ := s.SpeakerNameIssue(); !unsafe {
		t.Fatal("a name stored as invalid UTF-8 was not flagged")
	}

	s.NoteBoxName("Test+B", false)
	if unsafe, _ := s.SpeakerNameIssue(); unsafe {
		t.Fatal("the flag survived a rename to a clean name")
	}
}

// A renderer 404 triggers ONE description probe per window, never a loop, and
// a 404 there with a bad name is reported as the cause.
func TestRendererNotFoundProbesOnce(t *testing.T) {
	s, logs := nameTestServer()
	var mu sync.Mutex
	infoCalls, descCalls := 0, 0
	done := make(chan struct{}, 4)
	s.speakerName.readInfo = func(context.Context) (boxapi.Info, error) {
		mu.Lock()
		infoCalls++
		mu.Unlock()
		return boxapi.Info{Name: "Test&B", DeviceID: "device-id-here"}, nil
	}
	s.speakerName.probeDesc = func(_ context.Context, id string) (int, error) {
		mu.Lock()
		descCalls++
		mu.Unlock()
		if id != "device-id-here" {
			t.Errorf("probed deviceID %q", id)
		}
		defer func() { done <- struct{}{} }()
		return http.StatusNotFound, nil
	}

	for i := 0; i < 20; i++ {
		s.NoteRendererNotFound()
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no probe ran")
	}
	// Give a wrongly started second probe the chance to show up.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		s.speakerName.mu.Lock()
		probing := s.speakerName.probing
		s.speakerName.mu.Unlock()
		if !probing {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.NoteRendererNotFound() // still inside the window
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	ic, dc := infoCalls, descCalls
	mu.Unlock()
	if ic != 1 || dc != 1 {
		t.Fatalf("probe ran %d/%d times, want once", ic, dc)
	}
	unsafe, missing := s.SpeakerNameIssue()
	if !unsafe || !missing {
		t.Fatalf("SpeakerNameIssue = %v, %v; want true, true", unsafe, missing)
	}
	if !strings.Contains(logs.String(), "UPnP renderer is down because of the speaker name") {
		t.Errorf("the cause was not logged:\n%s", logs.String())
	}
	av := agentVersionFields(t, s)
	if av["speakerNameUnsafe"] != "true" || av["upnpRendererMissing"] != "true" {
		t.Errorf("version fields = %q / %q", av["speakerNameUnsafe"], av["upnpRendererMissing"])
	}
}

// A renderer whose description answers is not "down", whatever the 404 was.
func TestRendererNotFoundDescriptionThere(t *testing.T) {
	s, _ := nameTestServer()
	done := make(chan struct{}, 1)
	s.speakerName.readInfo = func(context.Context) (boxapi.Info, error) {
		return boxapi.Info{Name: "Kitchen", DeviceID: "device-id-here"}, nil
	}
	s.speakerName.probeDesc = func(context.Context, string) (int, error) {
		defer func() { done <- struct{}{} }()
		return http.StatusOK, nil
	}
	s.NoteRendererNotFound()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no probe ran")
	}
	time.Sleep(20 * time.Millisecond)
	if unsafe, missing := s.SpeakerNameIssue(); unsafe || missing {
		t.Fatalf("SpeakerNameIssue = %v, %v; want false, false", unsafe, missing)
	}
}

// A name that is nothing but characters the firmware cannot handle is
// refused before the speaker is touched. (The stored form of a real name is
// covered by boxapi's TestSetNameSanitises, which SetName applies.)
func TestHandleBoxNameRefusesNameThatSanitisesToNothing(t *testing.T) {
	s, _ := nameTestServer()
	w := httptest.NewRecorder()
	s.handleBoxName(w, httptest.NewRequest(http.MethodPut, "/api/box/name", strings.NewReader(`{"name":"<>"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

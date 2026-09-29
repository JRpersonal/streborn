package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useTempNAND points the NAND paths at a temp tree so the marker can be written
// and read for real instead of mocked.
func useTempNAND(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := nvRoot
	nvRoot = dir
	t.Cleanup(func() { nvRoot = old })
	return dir
}

func postSSH(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/agent/ssh", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.168.1.5:5000"
	w := httptest.NewRecorder()
	s.handleAgentSSH(w, r)
	return w
}

// The switch has to actually place and remove the file the boot path reads.
// Anything less and it is a control that lies.
func TestTheSwitchWritesAndRemovesTheMarkerTheBootPathReads(t *testing.T) {
	dir := useTempNAND(t)
	s := quietServer("127.0.0.1")
	marker := filepath.Join(dir, "streborn", "enable-ssh")

	if w := postSSH(t, s, `{"persistent":true}`); w.Code != http.StatusOK {
		t.Fatalf("turning it on: status %d, body %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the opt-in marker was not written: %v", err)
	}

	if w := postSSH(t, s, `{"persistent":false}`); w.Code != http.StatusOK {
		t.Fatalf("turning it off: status %d, body %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the opt-in marker survived being switched off: %v", err)
	}
}

// Turning it off twice must not fail. A user who presses Off on a speaker that
// is already off gets an error otherwise, for nothing.
func TestTurningItOffWhenItIsAlreadyOffIsFine(t *testing.T) {
	useTempNAND(t)
	s := quietServer("127.0.0.1")
	for i := 0; i < 2; i++ {
		if w := postSSH(t, s, `{"persistent":false}`); w.Code != http.StatusOK {
			t.Fatalf("attempt %d: status %d, body %s", i+1, w.Code, w.Body.String())
		}
	}
}

// Bose's own file is reported, never removed. STR does not delete what it did
// not write, and the UI needs to know so it can say the port stays open.
func TestBoseOwnMarkerIsReportedAndLeftAlone(t *testing.T) {
	dir := useTempNAND(t)
	s := quietServer("127.0.0.1")
	bose := filepath.Join(dir, "remote_services")
	if err := os.WriteFile(bose, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	w := postSSH(t, s, `{"persistent":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got sshState
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.BoseMarker {
		t.Error("Bose's own marker is present but not reported, so the UI would promise to close a port that stays open")
	}
	if _, err := os.Stat(bose); err != nil {
		t.Errorf("STR deleted a file it did not write: %v", err)
	}
}

// A body without the field must not be read as "switch it off".
func TestAMissingFieldIsRejectedRatherThanReadAsOff(t *testing.T) {
	dir := useTempNAND(t)
	s := quietServer("127.0.0.1")
	marker := filepath.Join(dir, "streborn", "enable-ssh")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{`{}`, `{"persistent":null}`, ``, `nonsense`} {
		if w := postSSH(t, s, body); w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, w.Code)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("a malformed request removed the marker: %v", err)
	}
}

// Off the LAN, the endpoint answers nothing at all.
func TestTheEndpointRefusesNonLANCallers(t *testing.T) {
	useTempNAND(t)
	s := quietServer("127.0.0.1")
	r := httptest.NewRequest(http.MethodPost, "/api/agent/ssh", strings.NewReader(`{"persistent":true}`))
	r.RemoteAddr = "203.0.113.7:5000"
	w := httptest.NewRecorder()
	s.handleAgentSSH(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", w.Code)
	}
}

// closeNow takes back a port STR opened for this boot, but must never shut one
// the owner opted into: the update-failure path fires it on speakers whose owner
// deliberately runs SSH, and closing there would be STR undoing a user's choice.
func TestCloseNowLeavesAnOptedInSpeakerOpen(t *testing.T) {
	dir := useTempNAND(t)
	s := quietServer("127.0.0.1")
	marker := filepath.Join(dir, "streborn", "enable-ssh")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	w := postSSH(t, s, `{"closeNow":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("closeNow removed the owner's opt-in marker: %v", err)
	}
	var got sshState
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Persistent {
		t.Error("the answer no longer reports the speaker as opted in")
	}
}

// Bose's own marker counts the same way: STR does not close a port that file
// keeps open, and it never deletes that file.
func TestCloseNowRespectsBosesOwnMarker(t *testing.T) {
	dir := useTempNAND(t)
	s := quietServer("127.0.0.1")
	bose := filepath.Join(dir, "remote_services")
	if err := os.WriteFile(bose, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if w := postSSH(t, s, `{"closeNow":true}`); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if _, err := os.Stat(bose); err != nil {
		t.Errorf("closeNow deleted a file STR did not write: %v", err)
	}
}

// A body with neither field is still a bad request: reading it as "close" would
// let an empty POST shut the port.
func TestAnEmptyBodyIsStillRejected(t *testing.T) {
	useTempNAND(t)
	s := quietServer("127.0.0.1")
	for _, body := range []string{`{}`, `{"closeNow":false}`, ``} {
		if w := postSSH(t, s, body); w.Code != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, w.Code)
		}
	}
}

// A form POSTed by a page the owner visits on the LAN must not be able to open
// root SSH on a speaker. The agent carries no CSRF token, so the content type is
// what stands between a browser form and this marker.
func TestABrowserFormCannotFlipTheSSHMarker(t *testing.T) {
	dir := useTempNAND(t)
	s := quietServer("127.0.0.1")
	for _, ct := range []string{
		"application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x",
		"text/plain;charset=UTF-8",
		"",
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/agent/ssh", strings.NewReader(`{"persistent":true}`))
		r.RemoteAddr = "192.168.1.5:5000"
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		w := httptest.NewRecorder()
		s.handleAgentSSH(w, r)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("content type %q: status %d, want 415", ct, w.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "streborn", "enable-ssh")); !os.IsNotExist(err) {
		t.Error("a form-shaped POST placed the opt-in marker")
	}
}

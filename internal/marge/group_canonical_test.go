package marge

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serveGroup drives the marge group CRUD the firmware uses.
func serveGroup(s *Server, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/streaming/account/stick@local/group/", strings.NewReader(body))
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func selfCenteredGroupXML(master, other string) string {
	return `<?xml version="1.0" encoding="UTF-8" ?><group><masterDeviceId>` + master +
		`</masterDeviceId><name>Stereo pair</name><roles>` +
		`<groupRole><deviceId>` + master + `</deviceId><role>LEFT</role></groupRole>` +
		`<groupRole><deviceId>` + other + `</deviceId><role>RIGHT</role></groupRole>` +
		`</roles></group>`
}

// The RIGHT box's firmware re-creates the pair record on its own marge with
// ITSELF as master/LEFT (field: Rolf's pair, 2026-07-31). Once the canonical
// document is installed, such a post must be answered with the canonical
// record, not stored.
func TestCanonicalGroupGuardsSelfCenteredPost(t *testing.T) {
	s := newTestServer()
	canonical := CanonicalGroupXML("Stereo pair", "AAAABBBBCCCC", "192.0.2.10", "DDDDEEEEFFFF", "192.0.2.11")
	if err := s.SetCanonicalGroup(canonical); err != nil {
		t.Fatalf("SetCanonicalGroup: %v", err)
	}

	// The partner firmware posts a SELF-centered record (it thinks it is master).
	rec := serveGroup(s, http.MethodPost, selfCenteredGroupXML("DDDDEEEEFFFF", "AAAABBBBCCCC"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d, want 201\nbody=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	xmlWellFormed(t, body)
	if !strings.Contains(body, "<masterDeviceId>AAAABBBBCCCC</masterDeviceId>") {
		t.Fatalf("self-centered post was not answered with the canonical master:\n%s", body)
	}

	// The stored record must still be the canonical one.
	xmlDoc, isCanonical, ok := s.GroupSnapshot()
	if !ok || !isCanonical {
		t.Fatalf("snapshot after guarded post: ok=%v canonical=%v", ok, isCanonical)
	}
	if !strings.Contains(xmlDoc, "<masterDeviceId>AAAABBBBCCCC</masterDeviceId>") {
		t.Fatalf("stored record lost the canonical master:\n%s", xmlDoc)
	}

	// The group poll must serve the canonical record too.
	rec = serveGroup(s, http.MethodGet, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "AAAABBBBCCCC") {
		t.Fatalf("group poll: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// A post that AGREES on the master must not replace the record either (the
	// firmware's own shape would silently become "canonical"): the canonical
	// document is echoed back, recognizable by its ipAddress fields, which the
	// firmware post does not carry.
	rec = serveGroup(s, http.MethodPost, selfCenteredGroupXML("AAAABBBBCCCC", "DDDDEEEEFFFF"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("agreeing post status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<ipAddress>192.0.2.10</ipAddress>") {
		t.Fatalf("agreeing post replaced the canonical record:\n%s", rec.Body.String())
	}

	// Dissolve clears record + canonical flag.
	rec = serveGroup(s, http.MethodDelete, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d", rec.Code)
	}
	if _, _, ok := s.GroupSnapshot(); ok {
		t.Fatal("record still present after delete")
	}
}

// The record must survive an agent restart: a restart that answered the
// firmware's group poll with "not grouped" invited the firmware to re-create
// the record from its own point of view.
func TestGroupRecordPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marge-group.json")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s1 := New(logger, WithGroupPath(path))
	canonical := CanonicalGroupXML("Stereo pair", "AAAABBBBCCCC", "192.0.2.10", "DDDDEEEEFFFF", "192.0.2.11")
	if err := s1.SetCanonicalGroup(canonical); err != nil {
		t.Fatalf("SetCanonicalGroup: %v", err)
	}

	// "Restart": a fresh server on the same path must restore the record.
	s2 := New(logger, WithGroupPath(path))
	xmlDoc, isCanonical, ok := s2.GroupSnapshot()
	if !ok || !isCanonical || !strings.Contains(xmlDoc, "AAAABBBBCCCC") {
		t.Fatalf("restored snapshot: ok=%v canonical=%v xml=%s", ok, isCanonical, xmlDoc)
	}

	// Clear removes the persisted file so the NEXT restart has no pair.
	s2.ClearGroup("test")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("persisted file still present after clear (err=%v)", err)
	}
	if _, _, ok := New(logger, WithGroupPath(path)).GroupSnapshot(); ok {
		t.Fatal("record resurrected after clear + restart")
	}
}

// A plain firmware post (no canonical record involved, the LEFT/master box's
// own report) must persist too, so the master's marge keeps answering the
// group poll after an agent restart.
func TestFirmwareGroupPostPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marge-group.json")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s1 := New(logger, WithGroupPath(path))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/streaming/account/stick@local/group/",
		strings.NewReader(selfCenteredGroupXML("AAAABBBBCCCC", "DDDDEEEEFFFF")))
	s1.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d", rec.Code)
	}

	xmlDoc, isCanonical, ok := New(logger, WithGroupPath(path)).GroupSnapshot()
	if !ok || isCanonical || !strings.Contains(xmlDoc, "AAAABBBBCCCC") {
		t.Fatalf("restored firmware post: ok=%v canonical=%v xml=%s", ok, isCanonical, xmlDoc)
	}
}

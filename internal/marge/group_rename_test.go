package marge

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A rename must land in the stored pair document (that is what the Bose app
// reads) and survive persistence; without a stored pair it must refuse.
func TestRenameGroup(t *testing.T) {
	dir := t.TempDir()
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), groupPath: filepath.Join(dir, "group.json")}

	if err := s.RenameGroup("Wohnzimmer"); err == nil {
		t.Fatal("renaming with no stored pair must refuse")
	}

	if err := s.SetCanonicalGroup(CanonicalGroupXML("", "AAA", "192.0.2.1", "BBB", "192.0.2.2")); err != nil {
		t.Fatalf("seed pair: %v", err)
	}
	if err := s.RenameGroup("  Wohnzimmer  "); err != nil {
		t.Fatalf("rename: %v", err)
	}
	doc, _, ok := s.GroupSnapshot()
	if !ok || !strings.Contains(doc, "<name>Wohnzimmer</name>") {
		t.Fatalf("renamed name missing from the document: %q", doc)
	}
	b, err := os.ReadFile(s.groupPath)
	if err != nil || !strings.Contains(string(b), "Wohnzimmer") {
		t.Fatalf("rename did not persist: %v %q", err, string(b))
	}
}

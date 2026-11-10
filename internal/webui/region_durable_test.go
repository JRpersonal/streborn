package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func putRegion(t *testing.T, s *Server, country string) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/region", strings.NewReader(`{"country":"`+country+`"}`))
	r.Header.Set("Content-Type", "application/json")
	s.handleRegion(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /api/region: %d %s", w.Code, w.Body.String())
	}
}

// region.txt is one upper-case line at 0644. Cut by a power loss, a plain
// write left it empty and the speaker forgot its region. A write that fails
// is only logged: the request still succeeds, the running agent uses the new
// region, and the file keeps the region it had.
func TestRegionPutContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	s := quietServer("127.0.0.1")
	s.regionFile = filepath.Join(t.TempDir(), "region.txt")
	putRegion(t, s, "de")
	b, err := os.ReadFile(s.regionFile)
	if err != nil || string(b) != "DE\n" {
		t.Fatalf("region.txt = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(s.regionFile)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(s.regionFile + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}

	// Occupy the temp sibling with a directory so the write cannot start.
	if err := os.Mkdir(s.regionFile+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	putRegion(t, s, "US")
	s.regionMu.Lock()
	inMemory := s.region
	s.regionMu.Unlock()
	if inMemory != "US" {
		t.Fatalf("in-memory region = %q, want US", inMemory)
	}
	b, err = os.ReadFile(s.regionFile)
	if err != nil || string(b) != "DE\n" {
		t.Fatalf("failed write changed region.txt: %q, %v", b, err)
	}
}

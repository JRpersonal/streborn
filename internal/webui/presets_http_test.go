package webui

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// The GET /api/presets read log is rate-limited per client (#882): two phone
// remotes polling an empty store every 15 s wrote 250 WARN lines in 19 minutes
// and pushed every boot line out of the NAND log tail a bundle carries.

func newReadLogServer(t *testing.T, buf *bytes.Buffer, now *time.Time) *Server {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		presets: store,
		logger:  slog.New(slog.NewTextHandler(buf, nil)),
	}
	s.presetsReadNow = func() time.Time { return *now }
	return s
}

func getPresets(s *Server, remote string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/presets", nil)
	req.RemoteAddr = remote
	s.handlePresets(rec, req)
	if rec.Code != http.StatusOK {
		panic("GET /api/presets answered " + rec.Result().Status)
	}
}

func countLines(buf *bytes.Buffer, needle string) int {
	n := 0
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, needle) {
			n++
		}
	}
	return n
}

func TestEmptyPresetsReadWarnIsRateLimitedPerClient(t *testing.T) {
	var buf bytes.Buffer
	now := time.Now()
	s := newReadLogServer(t, &buf, &now)
	for i := 0; i < 20; i++ {
		getPresets(s, "192.0.2.10:51234")
	}
	if got := countLines(&buf, "level=WARN"); got != 1 {
		t.Fatalf("20 empty GETs from one client must log ONE warn line, got %d:\n%s", got, buf.String())
	}
	// The next window: one more line, carrying what was left out.
	now = now.Add(presetsReadLogWindow + time.Second)
	getPresets(s, "192.0.2.10:51235")
	if got := countLines(&buf, "level=WARN"); got != 2 {
		t.Fatalf("a new window must log again, got %d warn lines:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "suppressed=19") {
		t.Fatalf("the second line must say how many answers went unlogged:\n%s", buf.String())
	}
	// A second client gets its own line at once.
	getPresets(s, "192.0.2.11:40000")
	if got := countLines(&buf, "level=WARN"); got != 3 {
		t.Fatalf("a second client must get its own line, got %d:\n%s", got, buf.String())
	}
	if got := countLines(&buf, "remote=192.0.2.11"); got != 1 {
		t.Fatalf("the second client's line must name it, got %d:\n%s", got, buf.String())
	}
}

// The transition between empty and non-empty is what a bundle has to show,
// so it is never rate-limited away; the non-empty side logs ONE INFO line
// (non-empty reads used to be Debug, invisible in a bundle).
func TestPresetsReadLogsTheTransitionAtOnce(t *testing.T) {
	var buf bytes.Buffer
	now := time.Now()
	s := newReadLogServer(t, &buf, &now)
	getPresets(s, "192.0.2.10:1")
	getPresets(s, "192.0.2.10:2")
	if err := s.presets.SetSlot(presets.Preset{Slot: 1, Name: "WDCB Jazz", StreamURL: "https://wdcb.example/stream", Type: "radio"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		getPresets(s, "192.0.2.10:3")
	}
	if got := countLines(&buf, "level=WARN"); got != 1 {
		t.Fatalf("empty phase: one warn line, got %d:\n%s", got, buf.String())
	}
	info := countLines(&buf, "GET /api/presets served")
	if info != 1 {
		t.Fatalf("non-empty phase: one INFO line at the transition, got %d:\n%s", info, buf.String())
	}
	if !strings.Contains(buf.String(), "count=1") || !strings.Contains(buf.String(), "suppressed=1") {
		t.Fatalf("the transition line must carry the count and what the empty phase left out:\n%s", buf.String())
	}
}

// A healthy box answers non-empty all day, and every line is an unbuffered
// append to NAND: after the one "served" line a client gets, its non-empty
// reads stay silent for good, across any number of windows. The count of
// what went unlogged rides on the next line, which is the empty WARN.
func TestNonEmptyPresetsReadsStaySilentAfterTheFirstLine(t *testing.T) {
	var buf bytes.Buffer
	now := time.Now()
	s := newReadLogServer(t, &buf, &now)
	if err := s.presets.SetSlot(presets.Preset{Slot: 2, Name: "KEXP", StreamURL: "https://kexp.example/stream", Type: "radio"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		for j := 0; j < 10; j++ {
			getPresets(s, "192.0.2.20:5000")
		}
		now = now.Add(presetsReadLogWindow + time.Second)
	}
	if got := countLines(&buf, "GET /api/presets served"); got != 1 {
		t.Fatalf("30 non-empty GETs over three windows must log ONE served line, got %d:\n%s", got, buf.String())
	}
	if got := countLines(&buf, "level=WARN"); got != 0 {
		t.Fatalf("a non-empty store must not warn, got %d:\n%s", got, buf.String())
	}
	// The store turns empty: the WARN comes at once and carries the 29 silent reads.
	if err := s.presets.RemoveSlot(2); err != nil {
		t.Fatal(err)
	}
	getPresets(s, "192.0.2.20:5001")
	if got := countLines(&buf, "level=WARN"); got != 1 {
		t.Fatalf("the non-empty to empty transition must warn at once, got %d:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "suppressed=29") {
		t.Fatalf("the warn line must carry the silent non-empty reads:\n%s", buf.String())
	}
}

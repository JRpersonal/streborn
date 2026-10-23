package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/zones"
)

func TestMemberAddrCanonicalisesAndRefuses(t *testing.T) {
	t.Parallel()
	ok := map[string]string{
		"":                 "",
		"  192.0.2.7 ":     "192.0.2.7",
		"::ffff:192.0.2.7": "192.0.2.7",
		"fe80::1%eth0":     "fe80::1%eth0",
		"2001:DB8::0001":   "2001:db8::1",
	}
	for in, want := range ok {
		got, err := memberAddr(in)
		if err != nil || got != want {
			t.Errorf("memberAddr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"speaker.local", "192.0.2.7:8090", "0.0.0.0", "224.0.0.1", "http://192.0.2.7", "192.0.2"} {
		if got, err := memberAddr(bad); err == nil {
			t.Errorf("memberAddr(%q) = %q, want an error", bad, got)
		}
	}
}

// A member address that is not an IP is refused before anything is dialled,
// with the member named so the caller can see which entry was wrong.
func TestZoneFormRefusesAMemberThatIsNotAnAddress(t *testing.T) {
	t.Parallel()
	zs, err := zones.Load(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatalf("zones.Load: %v", err)
	}
	s := &Server{boxHost: deadBoxHost, zones: zs, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	body := `{"master":{"deviceID":"MASTER","ip":"192.0.2.1"},"slaves":[{"deviceID":"AAA","ip":"speaker.local"}],"mode":"native"}`
	rec := httptest.NewRecorder()
	s.handleZoneForm(rec, httptest.NewRequest(http.MethodPost, "/api/box/zone/form", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "slave AAA") || !strings.Contains(got, "not an IP address") {
		t.Fatalf("body = %q, want the member named and the reason", got)
	}
}

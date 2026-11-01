package webui

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// A standby the app or a remote asks for names who asked (#1659): the group
// that came apart because its main speaker was switched off left no trace of
// which client did it.
func TestBoxStandbyLogsTheRequester(t *testing.T) {
	var logBuf bytes.Buffer
	// The host makes the GET to the speaker fail at once, which does not
	// matter here: the line is written before it.
	s := &Server{logger: slog.New(slog.NewTextHandler(&logBuf, nil)), boxHost: "127.0.0.1:1"}
	r := httptest.NewRequest("POST", "/api/box/power", nil)
	r.RemoteAddr = "192.0.2.7:51234"
	r.Header.Set("User-Agent", "STR-Remote/0.2.8")
	_ = s.boxStandby(r, "power")

	out := logBuf.String()
	for _, want := range []string{"standby requested by the app or phone", "route=power", "remote=192.0.2.7", "userAgent=STR-Remote/0.2.8"} {
		if !strings.Contains(out, want) {
			t.Errorf("standby line misses %q:\n%s", want, out)
		}
	}
}

func TestBoxStandbyWithoutRequestStillLogs(t *testing.T) {
	var logBuf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&logBuf, nil)), boxHost: "127.0.0.1:1"}
	_ = s.boxStandby(nil, "power")
	if !strings.Contains(logBuf.String(), "standby requested by the app or phone") {
		t.Errorf("standby line missing:\n%s", logBuf.String())
	}
}

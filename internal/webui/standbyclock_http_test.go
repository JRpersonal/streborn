package webui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/standbyclock"
)

// clockFirmware is a /clockDisplay that keeps what it is sent.
type clockFirmware struct {
	cfg   boxapi.ClockConfig
	posts []string
}

func (f *clockFirmware) GetClockConfig(context.Context) (boxapi.ClockConfig, error) {
	return f.cfg, nil
}

func (f *clockFirmware) SetClockConfigXML(_ context.Context, body string) error {
	f.posts = append(f.posts, body)
	get := func(name string) string {
		i := strings.Index(body, name+`="`)
		if i < 0 {
			return ""
		}
		rest := body[i+len(name)+2:]
		return rest[:strings.IndexByte(rest, '"')]
	}
	f.cfg = boxapi.ClockConfig{UserEnable: get("userEnable"), TimezoneInfo: get("timezoneInfo"),
		UserOffsetMinute: get("userOffsetMinute"), TimeFormat: get("timeFormat"), BrightnessLevel: get("brightnessLevel")}
	return nil
}

func TestStandbyClockEndpoint(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(":0", logger)
	fw := &clockFirmware{cfg: boxapi.ClockConfig{UserEnable: "false", TimezoneInfo: "NOT_SET", TimeFormat: "TIME_FORMAT_24HOUR_ID", BrightnessLevel: "70"}}
	path := filepath.Join(t.TempDir(), "standby-clock.json")
	s.SetStandbyClockKeeper(standbyclock.New(fw, logger, standbyclock.Options{Path: path}))
	call := func(method, body string) (int, map[string]any) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/box/standby-clock", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		s.handleStandbyClock(rr, req)
		var got map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &got)
		return rr.Code, got
	}
	code, got := call(http.MethodGet, "")
	if code != http.StatusOK || got["supported"] != true || got["brightnessSupported"] != true || got["stored"] != nil {
		t.Fatalf("GET %d %v", code, got)
	}
	code, got = call(http.MethodPost, `{"enabled":true,"timezone":"Europe/Berlin","offsetMinutes":60,"format24":true,"brightness":30}`)
	if code != http.StatusOK || got["ok"] != true {
		t.Fatalf("POST %d %v", code, got)
	}
	res := got["result"].(map[string]any)
	if res["brightnessHonoured"] != true {
		t.Fatalf("result %v", res)
	}
	want := `<clockDisplay><clockConfig userEnable="true" timezoneInfo="Europe/Berlin" userOffsetMinute="0" timeFormat="TIME_FORMAT_24HOUR_ID" brightnessLevel="30" /></clockDisplay>`
	if fw.posts[len(fw.posts)-1] != want {
		t.Fatalf("firmware got %s", fw.posts[len(fw.posts)-1])
	}
	// The phone's slider sends the brightness alone; the rest stays.
	if code, got = call(http.MethodPost, `{"brightness":10}`); code != http.StatusOK {
		t.Fatalf("brightness only: %d %v", code, got)
	}
	if last := fw.posts[len(fw.posts)-1]; !strings.Contains(last, `userEnable="true"`) || !strings.Contains(last, `brightnessLevel="10"`) || !strings.Contains(last, "Europe/Berlin") {
		t.Fatalf("brightness only wrote %s", last)
	}
	_, got = call(http.MethodGet, "")
	st := got["stored"].(map[string]any)
	if st["enabled"] != true || st["brightness"] != float64(10) {
		t.Fatalf("stored %v", st)
	}
	if code, _ = call(http.MethodPost, `{"enabled":true,"timezone":"Europe/\"x","format24":true}`); code != http.StatusBadRequest {
		t.Fatalf("a quote in the zone must be refused, got %d", code)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/box/standby-clock", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	s.handleStandbyClock(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("WAN caller: %d", rr.Code)
	}
}

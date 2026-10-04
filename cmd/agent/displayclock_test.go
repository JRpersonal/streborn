package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDisplayClockSnapshotReportsTheFirmwareView(t *testing.T) {
	now := time.Date(2026, 10, 3, 6, 1, 18, 0, time.UTC)
	frozen := now.Add(-3 * time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/clockTime":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?><clockTime utcTime="` +
				itoa(frozen.Unix()) + `" cueMusic="0" timeFormat="TIME_FORMAT_24HOUR_ID" />`))
		case "/clockDisplay":
			_, _ = w.Write([]byte(`<clockDisplay><clockConfig timezoneInfo="Europe/Berlin" userEnable="true" userOffsetMinute="0" /></clockDisplay>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got := displayClockSnapshot(srv.URL, func() time.Time { return now }).(map[string]any)
	if got["display_minus_system_sec"] != int64(-3*3600) {
		t.Fatalf("display_minus_system_sec = %v, want %d", got["display_minus_system_sec"], -3*3600)
	}
	if !strings.Contains(got["clock_display"].(string), "Europe/Berlin") {
		t.Fatalf("clock_display not kept raw: %v", got["clock_display"])
	}
	if got["display_utc"] != frozen.Format(time.RFC3339) {
		t.Fatalf("display_utc = %v", got["display_utc"])
	}
}

func TestDisplayClockSnapshotKeepsAnUnknownSchemaRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<clockTime><localTime hour="7" minute="1" /></clockTime>`))
	}))
	defer srv.Close()
	got := displayClockSnapshot(srv.URL, time.Now).(map[string]any)
	if _, ok := got["display_minus_system_sec"]; ok {
		t.Fatalf("a delta was invented without a utcTime attribute: %v", got)
	}
	if !strings.Contains(got["clock_time"].(string), "localTime") {
		t.Fatalf("raw clock_time lost: %v", got["clock_time"])
	}
}

func TestDisplayClockSnapshotSurvivesAnUnreachableFirmware(t *testing.T) {
	got := displayClockSnapshot("http://127.0.0.1:1", time.Now).(map[string]any)
	for _, k := range []string{"clock_time", "clock_display"} {
		if s, _ := got[k].(string); !strings.HasPrefix(s, "ERR:") {
			t.Fatalf("%s = %v, want an ERR string", k, got[k])
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

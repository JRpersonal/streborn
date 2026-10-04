package main

import (
	"context"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The speaker's displayed clock, as the firmware itself reports it.
//
// The clock on the display is a Bose firmware feature, separate from the Linux
// system clock STR keeps correct (clock_status). It can freeze while the system
// clock runs on: #841, an ST20 whose display stopped after a few hours, and the
// Portable on 2026-07-14, which showed a time ten hours off and only recovered
// after a reboot. The first bundle taken DURING a freeze (#841, 2026-10-03)
// carried the system clock and nothing at all about the displayed one, so the
// one question it could have answered, what the firmware believes the time is,
// stayed open.
//
// This section asks the firmware directly, only when a diagnostic is taken: no
// timer and no polling on the speaker. /clockTime is what the display draws
// from, /clockDisplay is its configuration (time zone, offset, on/off). Both are
// kept raw, because the schema of /clockTime on FW 27.0.6 is not documented and
// a parse that guessed wrong would hide exactly the field that matters.
// display_minus_system_sec is filled in when a utcTime attribute can be read.

// displayClockTimeout bounds each of the two firmware reads. The debug state as
// a whole is bounded too (debugSectionTimeout), but a frozen BoseApp should not
// cost the other sections their slot.
const displayClockTimeout = 2 * time.Second

// displayClockSnapshot returns the firmware's own view of the displayed clock.
// base is the speaker's Bose API, e.g. "http://127.0.0.1:8090".
func displayClockSnapshot(base string, now func() time.Time) any {
	out := map[string]any{
		"system_utc": now().UTC().Format(time.RFC3339),
	}
	clockTime, err := fetchRaw(base + "/clockTime")
	if err != nil {
		out["clock_time"] = "ERR: " + err.Error()
	} else {
		out["clock_time"] = clockTime
		if utc, ok := clockTimeUTC(clockTime); ok {
			out["display_utc"] = utc.UTC().Format(time.RFC3339)
			out["display_minus_system_sec"] = int64(utc.Sub(now()).Round(time.Second) / time.Second)
		}
	}
	clockDisplay, err := fetchRaw(base + "/clockDisplay")
	if err != nil {
		out["clock_display"] = "ERR: " + err.Error()
	} else {
		out["clock_display"] = clockDisplay
	}
	return out
}

// fetchRaw GETs one firmware path and returns its body, trimmed and capped so a
// misbehaving firmware cannot bloat the bundle.
func fetchRaw(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), displayClockTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: displayClockTimeout}).DialContext}}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return "", &statusError{code: resp.StatusCode, body: s}
	}
	return s, nil
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return "HTTP " + strconv.Itoa(e.code) + ": " + e.body
}

// clockTimeUTC reads a utcTime attribute (Unix seconds) from the root element
// of a /clockTime answer, if there is one.
func clockTimeUTC(raw string) (time.Time, bool) {
	var doc struct {
		UTCTime string `xml:"utcTime,attr"`
	}
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil || doc.UTCTime == "" {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(doc.UTCTime), 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

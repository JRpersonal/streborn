package webui

import (
	"testing"
	"time"
)

// The wake resume captures lastPlay BEFORE it waits for the box command lock.
// A recall that held the lock meanwhile updates lastPlay; the resume must then
// stand down instead of pushing the captured PREVIOUS station over the user's
// new one (#252: recall slot 5 on a sleeping ST30 ended up back on slot 4).
func TestResumeIsStale(t *testing.T) {
	ts := time.Date(2026, 7, 8, 10, 5, 49, 0, time.UTC)
	oldURL := "http://127.0.0.1:8888/stream/4"
	newURL := "http://127.0.0.1:8888/stream/5"

	cases := []struct {
		name    string
		current *lastPlayInfo
		want    bool
	}{
		{
			// Bare power press: nothing played meanwhile, the capture is current.
			name:    "unchanged target resumes",
			current: &lastPlayInfo{boxURL: oldURL, ts: ts},
			want:    false,
		},
		{
			// The racing recall switched the station: stand down.
			name:    "newer play on another url is stale",
			current: &lastPlayInfo{boxURL: newURL, ts: ts.Add(5 * time.Second)},
			want:    true,
		},
		{
			// The racing recall re-played the SAME station: it already runs, a
			// second push only causes a double-start hiccup.
			name:    "same url replayed meanwhile is stale",
			current: &lastPlayInfo{boxURL: oldURL, ts: ts.Add(5 * time.Second)},
			want:    true,
		},
		{
			name:    "no last play left is stale",
			current: nil,
			want:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resumeIsStale(oldURL, ts, tc.current); got != tc.want {
				t.Errorf("resumeIsStale(%q, ts, %+v) = %v, want %v", oldURL, tc.current, got, tc.want)
			}
		})
	}
}

// verifyRecall must treat "busy on a DIFFERENT stream" as a failed recall (the
// box playing the wrong station used to count as success, #252), while staying
// lenient when either side is unknown so a now_playing hiccup can never cause
// a retry storm.
func TestRecallLocationMatches(t *testing.T) {
	slot5 := "http://127.0.0.1:8888/stream/5"
	cases := []struct {
		name               string
		expected, location string
		want               bool
	}{
		{"exact match", slot5, slot5, true},
		{"wrong slot playing", slot5, "http://127.0.0.1:8888/stream/4", false},
		{"no expectation is lenient", "", "http://127.0.0.1:8888/stream/4", true},
		{"unreadable location is lenient", slot5, "", true},
		{"raw stream mismatch", slot5, "http://127.0.0.1:8888/stream/raw?u=abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recallLocationMatches(tc.expected, tc.location); got != tc.want {
				t.Errorf("recallLocationMatches(%q, %q) = %v, want %v", tc.expected, tc.location, got, tc.want)
			}
		})
	}
}

// The box entity-encodes attribute values in now_playing; the verify must
// compare decoded URLs so a legitimate match with a query string is not
// misread as a mismatch.
func TestXMLAttrUnescape(t *testing.T) {
	in := "http://127.0.0.1:8888/stream/raw?u=abc&amp;x=1"
	want := "http://127.0.0.1:8888/stream/raw?u=abc&x=1"
	if got := xmlAttrUnescape(in); got != want {
		t.Errorf("xmlAttrUnescape(%q) = %q, want %q", in, got, want)
	}
	if got := xmlAttrUnescape("plain"); got != "plain" {
		t.Errorf("xmlAttrUnescape(plain) = %q", got)
	}
}

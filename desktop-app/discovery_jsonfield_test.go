package main

import (
	"encoding/json"
	"testing"
)

// A value with an escaped quote used to end at that quote: a refused
// group-key press whose reason quotes a URL reached the user as
// "... Get \" (2026-10-27). Every escape Go's encoder writes must come back
// as the original text.
func TestJSONStringFieldDecodesEscapes(t *testing.T) {
	reason := `read the main speaker's group: http 502: Get "http://127.0.0.1:8090/getZone": context deadline exceeded`
	name := `Küche <oben> & "Bad"`
	body, err := json.Marshal(map[string]string{
		"groupKeyError":     reason,
		"groupKeyErrorKind": "master-unresponsive",
		"friendlyName":      name,
		"version":           "1.1.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for key, want := range map[string]string{
		"groupKeyError":     reason,
		"groupKeyErrorKind": "master-unresponsive",
		"friendlyName":      name,
		"version":           "1.1.2",
		"missing":           "",
	} {
		if got := jsonStringField(s, key); got != want {
			t.Errorf("jsonStringField(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestJSONStringFieldEdgeCases(t *testing.T) {
	cases := []struct{ body, key, want string }{
		{`{"a":""}`, "a", ""},
		{`{"a" : "x\/y"}`, "a", "x/y"},
		{`{"a":"ends with backslash \\"}`, "a", `ends with backslash \`},
		{`{"a":"unterminated`, "a", ""},
		{`{"a":12}`, "a", ""},
	}
	for _, tc := range cases {
		if got := jsonStringField(tc.body, tc.key); got != tc.want {
			t.Errorf("jsonStringField(%s, %q) = %q, want %q", tc.body, tc.key, got, tc.want)
		}
	}
}

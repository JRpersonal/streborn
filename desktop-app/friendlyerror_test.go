package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func respWithBody(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusConflict,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// A grouped-follower rejection must reach the frontend as the raw JSON body:
// parsePlayRejection extracts the master field from the error string to
// retarget the play at the group's lead speaker. The friendly reduction used
// to collapse it to just "box-grouped", dropping master.
func TestFriendlyErrorPassesBoxGroupedThroughRaw(t *testing.T) {
	const body = `{"error":"box-grouped","master":"AA11BB22CC01"}`
	if got := friendlyError(respWithBody(body)); got != body {
		t.Errorf("box-grouped body reduced to %q, want the raw JSON passed through", got)
	}
	// The master hint is optional on the agent side; the marker alone must
	// still pass through unchanged so the frontend's JSON parse stays valid.
	const noMaster = `{"error":"box-grouped"}`
	if got := friendlyError(respWithBody(noMaster)); got != noMaster {
		t.Errorf("box-grouped body without master reduced to %q, want raw", got)
	}
	// Same rule if a future agent moves the marker into the code field.
	const viaCode = `{"code":"box-grouped","master":"192.0.2.1"}`
	if got := friendlyError(respWithBody(viaCode)); got != viaCode {
		t.Errorf("box-grouped code body reduced to %q, want raw", got)
	}
}

// Every other error keeps the existing friendly reduction so UI toasts stay
// readable and the frontend can still branch on "code: message".
func TestFriendlyErrorKeepsReductionForOtherCodes(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"code plus detail", `{"code":"spotify-not-logged-in","detail":"log in first"}`, "spotify-not-logged-in: log in first"},
		{"code only", `{"code":"spotify-premium-required"}`, "spotify-premium-required"},
		{"detail only", `{"detail":"stream not reachable"}`, "stream not reachable"},
		{"error only", `{"error":"boom"}`, "boom"},
		{"non-JSON body", "plain text failure", "plain text failure"},
	}
	for _, c := range cases {
		if got := friendlyError(respWithBody(c.body)); got != c.want {
			t.Errorf("%s: friendlyError = %q, want %q", c.name, got, c.want)
		}
	}
}

package main

import (
	"strings"
	"testing"
)

// TestManualServerCandidates covers the add-server input forms (#341):
// a full description URL is used as-is, a bare IP expands to the
// well-known port+path combinations, and "ip:port" probes the
// well-known description paths on that port.
func TestManualServerCandidates(t *testing.T) {
	t.Run("full URL with path is exact", func(t *testing.T) {
		in := "http://192.0.2.10:8200/rootDesc.xml"
		got := manualServerCandidates(in)
		if len(got) != 1 || got[0] != in {
			t.Errorf("got %v, want exactly [%s]", got, in)
		}
	})

	t.Run("bare IP expands to well-known endpoints", func(t *testing.T) {
		got := manualServerCandidates("192.0.2.10")
		if len(got) != len(wellKnownDescriptionEndpoints) {
			t.Fatalf("got %d candidates, want %d: %v", len(got), len(wellKnownDescriptionEndpoints), got)
		}
		if got[0] != "http://192.0.2.10:8200/rootDesc.xml" {
			t.Errorf("first candidate = %q, want the MiniDLNA endpoint", got[0])
		}
	})

	t.Run("ip:port probes description paths on that port", func(t *testing.T) {
		got := manualServerCandidates("192.0.2.10:9999")
		if len(got) == 0 {
			t.Fatal("no candidates")
		}
		for _, c := range got {
			if !strings.Contains(c, "192.0.2.10:9999/") {
				t.Errorf("candidate %q does not use the given port", c)
			}
		}
		// Duplicate paths in the endpoint table must be deduped.
		seen := map[string]bool{}
		for _, c := range got {
			if seen[c] {
				t.Errorf("duplicate candidate %q", c)
			}
			seen[c] = true
		}
	})

	t.Run("whitespace and empty input", func(t *testing.T) {
		if got := manualServerCandidates("   "); got != nil {
			t.Errorf("blank input: got %v, want nil", got)
		}
		got := manualServerCandidates("  192.0.2.10  ")
		if len(got) == 0 || !strings.Contains(got[0], "192.0.2.10") {
			t.Errorf("trimmed input: got %v", got)
		}
	})

	t.Run("scheme URL without path expands", func(t *testing.T) {
		got := manualServerCandidates("http://192.0.2.10:8200")
		if len(got) == 0 {
			t.Fatal("no candidates")
		}
		for _, c := range got {
			if !strings.HasPrefix(c, "http://192.0.2.10:8200/") {
				t.Errorf("candidate %q does not keep the given host:port", c)
			}
		}
	})
}

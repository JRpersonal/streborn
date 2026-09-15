//go:build darwin

package main

import "testing"

// The four macOS answers have to stay distinguishable. Collapsing translocation
// into "folder not writable" is the specific mistake worth guarding: it would
// send somebody to fix permissions on a folder that is fine.
func TestTheMacOSReasonsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range []string{
		selfReplaceOK,
		selfReplaceNoBundle,
		selfReplaceFromImage,
		selfReplaceTranslocated,
		selfReplaceFolderLocked,
	} {
		if r == "" {
			t.Error("an empty reason token")
		}
		if seen[r] {
			t.Errorf("duplicate reason token %q: two different causes would read the same", r)
		}
		seen[r] = true
	}
}

//go:build !windows

package main

// exeResourceVersion has no equivalent outside Windows: the macOS and Linux
// builds update in place and never look for a sibling copy by version.
func exeResourceVersion(string) (maj, min, patch int, ok bool) {
	return 0, 0, 0, false
}

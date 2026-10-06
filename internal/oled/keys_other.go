//go:build !(linux && arm)

package oled

import "errors"

// The key thread exists only on the speaker.
func traceKeys(<-chan struct{}, chan<- KeyPress) error {
	return errors.New("no key trace on this platform")
}

//go:build !linux

package oled

import (
	"errors"
	"time"
)

// The panel only exists on the speaker; dev hosts never draw.

func panelSupported() bool { return false }

func boseAppDisplayReady() bool { return false }

func play(func(float64, []byte) bool, func(float64), time.Duration, <-chan struct{}) error {
	return errors.New("no panel on this platform")
}

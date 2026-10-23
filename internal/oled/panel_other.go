//go:build !linux

package oled

import (
	"errors"
	"time"
)

// The panel only exists on the speaker; dev hosts never draw.

func panelSupported() bool { return false }

func boseAppDisplayReady() bool { return false }

func boseAppAge() time.Duration { return 0 }

func play(func(float64, []byte) bool, func(float64), time.Duration, <-chan struct{}) error {
	return errors.New("no panel on this platform")
}

func hold(func([]byte), <-chan struct{}, <-chan struct{}, func() time.Duration) error {
	return errors.New("no panel on this platform")
}

// ProcessCPUms and SelfCPUms read /proc; nothing to read here.
func ProcessCPUms(string) int64 { return -1 }

func SelfCPUms() int64 { return -1 }

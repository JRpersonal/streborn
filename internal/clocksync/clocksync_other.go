//go:build !linux

package clocksync

import (
	"errors"
	"time"
)

// setSystemTime is unsupported off Linux (the agent only runs on the speaker's
// Linux firmware). Present so the package builds on developer hosts.
func setSystemTime(time.Time) error {
	return errors.New("clocksync: setting the system clock is only supported on linux")
}

package boxlog

import "time"

// The firmware's own minute.
//
// BoseApp logs the Wi-Fi signal quality once a minute, in standby too
// ("WiFiSignalStrengthDBMToQuality - RSSI Level dbm=-50 quality=Excellent").
// A diagnostic bundle from an ST20 whose standby clock froze in the night
// (#841, 2026-11-13) has the line every minute right up to the morning. The
// agent reads every one of those lines already, so a check that has to run
// now and then in a long standby can ride on it instead of a timer of its
// own: nothing extra wakes up, and the line stops when the firmware stops.

// SetMinuteHandler installs the hook for the firmware's once-a-minute line.
// It runs on the reader's goroutine, so it must return at once. Set before
// Run; nil disables it.
func (r *Reader) SetMinuteHandler(h func(at time.Time)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.minuteHandler = h
}

// fireMinute hands the Wi-Fi quality line to the minute hook.
func (r *Reader) fireMinute(ev Event) {
	if ev.Class != ClassWiFiQuality {
		return
	}
	r.mu.Lock()
	h := r.minuteHandler
	r.mu.Unlock()
	if h != nil {
		h(ev.At)
	}
}

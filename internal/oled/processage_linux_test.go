//go:build linux

package oled

import (
	"testing"
	"time"
)

// A stat line in the kernel's shape, with the start time in ticks at field
// 22; the command name may carry spaces and parentheses, so the parse starts
// after the last ')'.
func TestProcessAgeReadsTheStartTimeAfterTheCommandName(t *testing.T) {
	t.Parallel()
	stat := "1234 (Bose App (x)) S 1 1234 1234 0 -1 4194560 100 0 0 0 10 5 0 0 20 0 30 0 6400 1000000 500 4294967295 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0"
	if got := processAge(stat, "100.50 90.00", 100); got != 36500*time.Millisecond {
		t.Fatalf("age = %v, want 36.5s", got)
	}
	if got := processAge(stat, "60.00 50.00", 100); got != 0 {
		t.Fatalf("a start time after the uptime must read as 0, got %v", got)
	}
	if got := processAge("garbage", "100 90", 100); got != 0 {
		t.Fatalf("unparseable stat must read as 0, got %v", got)
	}
}

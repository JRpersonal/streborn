package webui

import (
	"context"
	"testing"
	"time"
)

// A speaker on a cable can get its lease seconds after the guard started. The
// settle wait has to notice the cable then, or it sits out its whole budget
// and the unused radio is power-cycled (ST20 on a cable, v1.2.0 bundle).
func TestSettleWaitEndsWhenTheCableComesUp(t *testing.T) {
	t.Parallel()
	start := time.Now()
	_, assoc, wired := waitAssociationSettled(context.Background(), "wlan-test-none", time.Minute,
		func() string { return "eth0" })
	if wired != "eth0" || assoc {
		t.Fatalf("got assoc=%v wired=%q, want the cable reported", assoc, wired)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("the wait ran on for %v after the cable was there", time.Since(start))
	}
}

// Without a cable probe the wait behaves as before: no cable, no association,
// and the budget decides.
func TestSettleWaitWithoutACableProbe(t *testing.T) {
	t.Parallel()
	_, assoc, wired := waitAssociationSettled(context.Background(), "wlan-test-none", 0, nil)
	if assoc || wired != "" {
		t.Fatalf("got assoc=%v wired=%q, want neither", assoc, wired)
	}
}

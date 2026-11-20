package spotify

import (
	"errors"
	"testing"
)

// #1947: an engine restart STR asks for (account switch for a preset, new
// name, hot swap) is told apart from a crash, so it skips the 3 s crash pause.
func TestClassifyRunExit(t *testing.T) {
	killed := errors.New("signal: killed")
	cases := []struct {
		name          string
		waitErr       error
		runCancelled  bool
		managerDone   bool
		wantRequested bool
		wantErr       error
	}{
		{"restart requested, process killed", killed, true, false, true, nil},
		{"restart requested, process exited cleanly", nil, true, false, true, nil},
		{"crash on its own", killed, false, false, false, killed},
		{"clean exit on its own", nil, false, false, false, nil},
		{"agent shutting down", killed, true, true, false, killed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyRunExit(c.waitErr, c.runCancelled, c.managerDone)
			if errors.Is(got, errRestartRequested) != c.wantRequested {
				t.Fatalf("requested = %v, want %v (err %v)", !c.wantRequested, c.wantRequested, got)
			}
			if !c.wantRequested && !errors.Is(got, c.wantErr) && got != c.wantErr {
				t.Fatalf("err = %v, want %v", got, c.wantErr)
			}
		})
	}
	if restartRequestedWait >= 3e9 {
		t.Fatalf("a requested restart must be quicker than the 3 s crash pause, got %v", restartRequestedWait)
	}
}

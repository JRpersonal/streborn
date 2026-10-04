package main

import (
	"errors"
	"io"
	"log/slog"
	"testing"
)

func TestStickRefreshNeeded(t *testing.T) {
	cases := []struct {
		name string
		ver  map[string]string
		err  error
		want bool
	}{
		{"agent says no stick", map[string]string{"version": "v1.0.3", "usbStick": "absent"}, nil, false},
		{"agent says stick", map[string]string{"version": "v1.0.3", "usbStick": "present"}, nil, true},
		{"older agent without the field", map[string]string{"version": "v1.0.2"}, nil, true},
		{"agent version unknown", nil, errors.New("timeout"), true},
	}
	for _, c := range cases {
		if got, _ := stickRefreshNeeded(c.ver, c.err); got != c.want {
			t.Errorf("%s: stickRefreshNeeded = %v, want %v", c.name, got, c.want)
		}
	}
}

// refreshStickWith must not open SSH when the agent reports no stick, and must
// close it again on every path where it did open it, including a write that
// panics part-way (the defer still runs).
func TestRefreshStickWithOpensSSHOnlyForAStickAndAlwaysClosesIt(t *testing.T) {
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	run := func(ver map[string]string, write func()) (opened, closed, wrote int) {
		a.refreshStickWith("192.0.2.1", stickRefreshSteps{
			version:  func() (map[string]string, error) { return ver, nil },
			openSSH:  func() { opened++ },
			closeSSH: func() { closed++ },
			write: func() {
				wrote++
				if write != nil {
					write()
				}
			},
		})
		return
	}

	if o, c, w := run(map[string]string{"usbStick": "absent"}, nil); o != 0 || c != 0 || w != 0 {
		t.Fatalf("no stick: opened=%d closed=%d wrote=%d, want 0/0/0", o, c, w)
	}
	if o, c, w := run(map[string]string{"usbStick": "present"}, nil); o != 1 || c != 1 || w != 1 {
		t.Fatalf("stick: opened=%d closed=%d wrote=%d, want 1/1/1", o, c, w)
	}
	if o, c, w := run(map[string]string{"version": "v1.0.2"}, nil); o != 1 || c != 1 || w != 1 {
		t.Fatalf("old agent: opened=%d closed=%d wrote=%d, want 1/1/1", o, c, w)
	}

	// A write that blows up still gets its SSH closed.
	var opened, closed int
	func() {
		defer func() { _ = recover() }()
		a.refreshStickWith("192.0.2.1", stickRefreshSteps{
			version:  func() (map[string]string, error) { return map[string]string{"usbStick": "present"}, nil },
			openSSH:  func() { opened++ },
			closeSSH: func() { closed++ },
			write:    func() { panic("write failed") },
		})
	}()
	if opened != 1 || closed != 1 {
		t.Fatalf("panicking write: opened=%d closed=%d, want 1/1", opened, closed)
	}
}

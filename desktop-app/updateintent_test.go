package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestReconcileIntent(t *testing.T) {
	now := time.Now()
	base := updateIntent{Host: "192.0.2.5", Port: 8888, TargetVersion: "v1.2.3", WantEngine: true}
	fresh := base
	fresh.StartedAt = now.Add(-5 * time.Minute)
	old := base
	old.StartedAt = now.Add(-6 * time.Hour)

	cases := []struct {
		name    string
		in      updateIntent
		version string
		engine  string
		want    intentAction
	}{
		{"everything as intended", fresh, "v1.2.3", "present", intentNothing},
		{"agent landed, engine gone", fresh, "v1.2.3", "missing", intentRestoreEngine},
		{"agent landed, engine not wanted", func() updateIntent { i := fresh; i.WantEngine = false; return i }(), "v1.2.3", "missing", intentNothing},
		{"cut short, still fresh", fresh, "v1.2.2", "missing", intentResumeAgent},
		{"cut short, long ago", old, "v1.2.2", "missing", intentFlagOnly},
		{"unreadable version, still fresh", fresh, "", "present", intentResumeAgent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reconcileIntent(c.in, c.version, c.engine, now); got != c.want {
				t.Errorf("reconcileIntent = %v, want %v", got, c.want)
			}
		})
	}
}

// The record must survive a restart, which is the entire point.
func TestUpdateIntentRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "update-intent.json")
	list := upsertIntent(nil, updateIntent{Host: "192.0.2.5", Port: 8888, TargetVersion: "v1", WantEngine: true, StartedAt: time.Now()})
	list = upsertIntent(list, updateIntent{Host: "192.0.2.6", Port: 17008, TargetVersion: "v1", StartedAt: time.Now()})
	if err := saveUpdateIntentsTo(p, list); err != nil {
		t.Fatalf("save: %v", err)
	}
	back := loadUpdateIntentsFrom(p)
	if len(back) != 2 {
		t.Fatalf("want 2 records back, got %d", len(back))
	}
	if _, ok := findIntent(back, "192.0.2.5", 8888); !ok {
		t.Error("the first speaker's record did not survive")
	}
	back = removeIntent(back, "192.0.2.5", 8888)
	if _, ok := findIntent(back, "192.0.2.5", 8888); ok {
		t.Error("a cleared record must be gone")
	}
	// A record older than the retention window is not a speaker mid-update.
	stale := []updateIntent{{Host: "192.0.2.7", Port: 8888, StartedAt: time.Now().Add(-30 * 24 * time.Hour)}}
	if err := saveUpdateIntentsTo(p, stale); err != nil {
		t.Fatalf("save stale: %v", err)
	}
	if got := loadUpdateIntentsFrom(p); len(got) != 0 {
		t.Errorf("a stale record must be dropped on load, got %d", len(got))
	}
}

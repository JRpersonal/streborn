// Tests for the languageUpdated forensics: the typed parse must recognize the
// frame (not drop it as unrecognized) and the revert detector must flag a
// different value arriving within languageRevertWindow - the Wave firmware
// overwriting a user's language save (live bundle 2026-07-25: 2 then 3 within
// 200 ms, setting back on English).

package boxws

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// levelRecorder captures messages per level so a test can assert the revert
// WARN fired without depending on log formatting.
type levelRecorder struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	warn bytes.Buffer
}

func (r *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *levelRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.WriteString(rec.Message + "\n")
	if rec.Level >= slog.LevelWarn {
		r.warn.WriteString(rec.Message + "\n")
	}
	return nil
}
func (r *levelRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *levelRecorder) WithGroup(string) slog.Handler      { return r }

func (r *levelRecorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func (r *levelRecorder) warns() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.warn.String()
}

func langFrame(v string) []byte {
	return []byte(`<updates deviceID="x"><languageUpdated><sysLanguage>` + v + `</sysLanguage></languageUpdated></updates>`)
}

func TestHandleMessage_LanguageUpdatedParsedNotUnrecognized(t *testing.T) {
	rec := &levelRecorder{}
	c := New(slog.New(rec), "ws://127.0.0.1:8080/", &recHandler{})
	c.handleMessage(context.Background(), langFrame("2"))
	if strings.Contains(rec.all(), "unrecognized frame") {
		t.Fatalf("languageUpdated fell through as unrecognized:\n%s", rec.all())
	}
	c.mu.Lock()
	got := c.lastLang
	c.mu.Unlock()
	if got != "2" {
		t.Fatalf("lastLang = %q, want 2", got)
	}
}

func TestHandleMessage_LanguageRevertFlagged(t *testing.T) {
	rec := &levelRecorder{}
	c := New(slog.New(rec), "ws://127.0.0.1:8080/", &recHandler{})
	// User saves German (2); the firmware overwrites with English (3) 40 ms
	// later - the live Wave capture. The second frame must WARN as a revert.
	c.handleMessage(context.Background(), langFrame("2"))
	c.handleMessage(context.Background(), langFrame("3"))
	if !strings.Contains(rec.warns(), "sysLanguage overwritten") {
		t.Fatalf("expected the revert WARN, got warns:\n%s", rec.warns())
	}
}

func TestHandleMessage_LanguageSlowChangeIsNotARevert(t *testing.T) {
	rec := &levelRecorder{}
	c := New(slog.New(rec), "ws://127.0.0.1:8080/", &recHandler{})
	c.handleMessage(context.Background(), langFrame("2"))
	// Simulate the previous frame being old: a user genuinely changing their
	// mind minutes later must not be flagged as a firmware revert.
	c.mu.Lock()
	c.lastLangAt = time.Now().Add(-time.Minute)
	c.mu.Unlock()
	c.handleMessage(context.Background(), langFrame("3"))
	if strings.Contains(rec.warns(), "sysLanguage overwritten") {
		t.Fatalf("slow change wrongly flagged as revert:\n%s", rec.warns())
	}
}

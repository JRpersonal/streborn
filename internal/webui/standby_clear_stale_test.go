package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// TestHandleEnterStandby_UserPlayDuringClearAbortsClearURI: moving the standby
// transport clear off the gabbo read loop un-serialized it from the next
// queued frame, so a preset press right after the power-off (the routine
// power-on-via-preset flow) could have its fresh SetURI overtaken by the
// straggling ClearURI - the Stop can block for seconds against a box that is
// shutting down. A user play that arrives while the clear is in flight is
// newer intent: the ClearURI must not be issued.
func TestHandleEnterStandby_UserPlayDuringClearAbortsClearURI(t *testing.T) {
	rec := &soapRecorder{}
	stopStarted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("SOAPACTION"), "#Stop") {
			once.Do(func() { close(stopStarted) })
			<-release
		}
		rec.ServeHTTP(w, r)
	}))
	t.Cleanup(box.Close)
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		presets:  store,
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
	}
	t.Cleanup(s.stopQueue)
	// A real power-off: fresh adjacent key press, so the latch+clear route runs.
	s.SetUserActivityFn(func() time.Time { return time.Now() })

	s.HandleEnterStandby()

	select {
	case <-stopStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("the clear goroutine never issued its Stop")
	}
	// The user presses a preset while the Stop is still blocking.
	s.NoteUserPlay()
	close(release)

	// Give the goroutine time to run its post-Stop staleness check.
	time.Sleep(300 * time.Millisecond)
	if rec.has("SetAVTransportURI") {
		t.Fatalf("a user play during the clear must abort the ClearURI (it would wipe the fresh recall's transport), got %v", rec.list())
	}
}

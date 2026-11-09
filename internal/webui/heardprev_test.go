package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type heardPrevCall struct {
	heardPrev bool
	forward   bool
}

// heardPrevTestServer is a Server playing Spotify whose engine records which
// skip each press reached. withHeardPrev=false is an engine without the
// SpotifyRemoteKeys capability.
func heardPrevTestServer(withHeardPrev bool) (*Server, chan heardPrevCall) {
	calls := make(chan heardPrevCall, 8)
	s := &Server{
		queue:            newPlayQueue(),
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		spotifyStreaming: func() bool { return true },
		spotifySkip: func(_ context.Context, forward bool) error {
			calls <- heardPrevCall{forward: forward}
			return nil
		},
	}
	if withHeardPrev {
		s.spotifyHeardPrev = func(context.Context) (bool, error) {
			calls <- heardPrevCall{heardPrev: true}
			return true, nil
		}
	}
	return s, calls
}

func nextHeardPrevCall(t *testing.T, calls chan heardPrevCall) heardPrevCall {
	t.Helper()
	select {
	case c := <-calls:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("the skip never reached the engine")
	}
	return heardPrevCall{}
}

// Back on a Spotify playlist goes through HeardPrev for the remote (#1678) AND
// the app (#1797): the engine's plain prev decides on its own position, which
// runs the whole buffer lag ahead of the room, so the app's Previous only ever
// restarted the song. Next stays a plain skip on both.
func TestBackTakesHeardPrevForTheRemoteAndTheApp(t *testing.T) {
	s, calls := heardPrevTestServer(true)
	// The remote (no Spotify key slot: app-driven playback) presses Back.
	if _, err := s.HardwareSkip(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if c := nextHeardPrevCall(t, calls); !c.heardPrev {
		t.Fatalf("the remote's Back took the plain prev: %+v", c)
	}
	// The app presses Previous, through the route its button posts to.
	rec := httptest.NewRecorder()
	s.handleTransportPrev(rec, httptest.NewRequest(http.MethodPost, "/api/prev", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/prev answered %d: %s", rec.Code, rec.Body.String())
	}
	if c := nextHeardPrevCall(t, calls); !c.heardPrev {
		t.Fatalf("the app's Previous took the plain prev (restarts the song, #1797): %+v", c)
	}
	// The app presses Next.
	rec = httptest.NewRecorder()
	s.handleTransportNext(rec, httptest.NewRequest(http.MethodPost, "/api/next", nil))
	if c := nextHeardPrevCall(t, calls); c.heardPrev || !c.forward {
		t.Fatalf("the app's Next took %+v, want the plain next", c)
	}
	// The remote presses Next.
	if _, err := s.HardwareSkip(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if c := nextHeardPrevCall(t, calls); c.heardPrev || !c.forward {
		t.Fatalf("the remote's Next took %+v, want the plain next", c)
	}
}

// An engine without the capability keeps the plain prev for every Back.
func TestBackWithoutHeardPrevKeepsThePlainPrev(t *testing.T) {
	s, calls := heardPrevTestServer(false)
	if _, err := s.transportSkip(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if c := nextHeardPrevCall(t, calls); c.heardPrev || c.forward {
		t.Fatalf("Back took %+v, want the plain prev", c)
	}
}

package main

// The app hears a media server come back (#1890).
//
// A library key whose music server was off is refused and marked red in the
// app. The passive SSDP listener hears every ssdp:alive anyway, so a media
// server that comes back is passed to the frontend as one event, which clears
// the marks its keys got. No request to the server, no timer: a server sends
// its alive by itself, for every device and service it has, so the event is
// let through once per server per mediaServerAliveGap.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/dlna"
	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	mediaServerAliveEvent = "mediaserver:alive"
	mediaServerAliveGap   = 30 * time.Second
)

// aliveGate lets one alive per server through per gap.
type aliveGate struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// pass reports whether the alive of server key goes through at now.
func (g *aliveGate) pass(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.last == nil {
		g.last = map[string]time.Time{}
	}
	if at, ok := g.last[key]; ok && now.Sub(at) < mediaServerAliveGap {
		return false
	}
	g.last[key] = now
	// Forget servers not heard for a long while, so the map stays small.
	for k, at := range g.last {
		if now.Sub(at) > time.Hour {
			delete(g.last, k)
		}
	}
	return true
}

// usnUUID is the device part of a USN ("uuid:abc::urn:..." -> "uuid:abc").
func usnUUID(usn string) string {
	if i := strings.Index(usn, "::"); i >= 0 {
		return usn[:i]
	}
	return usn
}

// mediaServerAlivePayload is what the frontend gets for one alive.
func mediaServerAlivePayload(location, usn string) map[string]any {
	return map[string]any{"location": location, "uuid": usnUUID(usn)}
}

// watchMediaServerAlives passes media server alives to the frontend.
func watchMediaServerAlives(ctx context.Context) {
	gate := &aliveGate{}
	dlna.SetMediaServerAliveHook(func(location, usn string) {
		key := usnUUID(usn)
		if key == "" {
			key = location
		}
		if !gate.pass(key, time.Now()) {
			return
		}
		wailsrt.EventsEmit(ctx, mediaServerAliveEvent, mediaServerAlivePayload(location, usn))
	})
}

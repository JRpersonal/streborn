package dlna

// Hearing a media server come back (#1890).
//
// A library key whose music server was off is refused, and the desktop app
// marks it red. The mark stayed until the key was pressed again, also after
// the server was back. A media server that comes back announces itself with
// ssdp:alive NOTIFYs, which the passive listener (announce.go) hears anyway,
// so the app can clear the mark on that, without asking the server anything.

import (
	"strings"
	"sync/atomic"
)

// MediaServerAliveFunc hears one ssdp:alive of a media server: its
// description LOCATION and its USN ("uuid:...::urn:...").
type MediaServerAliveFunc func(location, usn string)

var mediaServerAlive atomic.Pointer[MediaServerAliveFunc]

// SetMediaServerAliveHook sets the function the NOTIFY listener calls for
// every ssdp:alive a media server sends; nil removes it. It is called on the
// listener's goroutine and must return quickly. A server repeats its alive
// for every device and service it has, so a caller throttles.
func SetMediaServerAliveHook(f MediaServerAliveFunc) {
	if f == nil {
		mediaServerAlive.Store(nil)
		return
	}
	mediaServerAlive.Store(&f)
}

// noteMediaServerAlive passes an ssdp:alive to the hook when it comes from a
// media server: its NT names the MediaServer device or the ContentDirectory
// service. A speaker, a router or a TV announcing itself is not one.
func noteMediaServerAlive(pkt []byte) {
	hook := mediaServerAlive.Load()
	if hook == nil {
		return
	}
	nt := strings.ToLower(headerValue(pkt, "NT"))
	if !strings.Contains(nt, ":mediaserver:") && !strings.Contains(nt, ":contentdirectory:") {
		return
	}
	loc := headerValue(pkt, "LOCATION")
	if loc == "" {
		return
	}
	(*hook)(loc, headerValue(pkt, "USN"))
}

package webui

import "net/http"

// playRefusal is the answer a preset recall gives when it does not start
// playback: the status and the exact body the HTTP adapter renders, plus the
// typed fields an in-process caller (an alarm, the stuck-buffer rescue, the
// idle re-push) reads without parsing anything.
//
// The bodies are pinned by three clients and must not change: the phone
// remote reads data.code and data.error (playRefusal in assets/index.html),
// the desktop app passes the raw body of box-grouped and stereo-incomplete
// through to friendlyError, and the hold-to-save path re-emits the status. A
// refusal is built once, in the service, and rendered by writePlayRefusal;
// nothing else writes these answers.
type playRefusal struct {
	Status int
	// Error is the user-facing message: the whole body of a plain-text
	// refusal, the "error" field of a JSON one.
	Error string
	// Code is the machine-readable "code" field when the JSON body carries
	// one (spotify-not-logged-in, native-select-refused, ...).
	Code string
	// JSON is the exact JSON body to render. nil means a plain-text body
	// (http.Error with Error, which appends a newline).
	JSON any
}

// textRefusal is a plain-text refusal, rendered the way http.Error renders it.
func textRefusal(status int, msg string) *playRefusal {
	return &playRefusal{Status: status, Error: msg}
}

// jsonRefusal is a JSON refusal. Error and Code are lifted out of the body so
// an in-process caller can branch on them; the body itself goes out as is.
func jsonRefusal(status int, body map[string]any) *playRefusal {
	ref := &playRefusal{Status: status, JSON: body}
	if e, ok := body["error"].(string); ok {
		ref.Error = e
	}
	if c, ok := body["code"].(string); ok {
		ref.Code = c
	}
	return ref
}

// writePlayRefusal renders a refusal onto the wire: http.Error for the text
// ones, writeJSON for the JSON ones, byte for byte what the handlers wrote
// before the service existed.
func writePlayRefusal(w http.ResponseWriter, ref *playRefusal) {
	if ref.JSON == nil {
		http.Error(w, ref.Error, ref.Status)
		return
	}
	writeJSON(w, ref.Status, ref.JSON)
}

// playResult is what a delivered recall reports: the 200 body of
// POST /api/play/<slot>.
type playResult struct {
	Slot int
	Name string
	// Type is the preset type the body names ("queue", "native", "spotify");
	// empty for a radio station or a library file, whose body has no type key.
	Type string
}

// body is the exact 200 body: {status:"playing",slot,name[,type]}.
func (r playResult) body() map[string]any {
	out := map[string]any{"status": "playing", "slot": r.Slot, "name": r.Name}
	if r.Type != "" {
		out["type"] = r.Type
	}
	return out
}

// libraryServerOfflineCode is the code of a library key refused because its
// music server is off (#1339). The desktop app passes this body through raw
// (friendlyError) and the phone page reads it, both for the server's name.
const libraryServerOfflineCode = "library-server-offline"

// libraryServerOfflineRefusal is the answer to an app or phone press on a
// library key whose music server is off: the speaker was not handed the file.
// The body carries the browse offline reply's "offline" and "server" fields,
// so a server that is off reads the same in every answer, plus the code and an
// English sentence for a client that knows neither.
func (s *Server) libraryServerOfflineRefusal(slot int, name string, off *libraryKeyUnplayable) *playRefusal {
	msg := "The music server is not answering. Switch it on and try again."
	if off.Server != "" {
		msg = "The music server " + off.Server + " is not answering. Switch it on and try again."
	}
	s.logger.Warn("preset slot recall (app): the key's music server is off, the speaker is not handed its file",
		"slot", slot, "name", name, "server", off.Server)
	return jsonRefusal(http.StatusBadGateway, map[string]any{
		"error": msg, "code": libraryServerOfflineCode,
		"offline": true, "server": off.Server,
		"slot": slot, "name": name,
	})
}

// libraryKeyRefusal is the answer to an app or phone press on a library key
// that cannot play: the server-off refusal, or the file-gone one.
func (s *Server) libraryKeyRefusal(slot int, name string, v *libraryKeyUnplayable) *playRefusal {
	if v.FileGone {
		return s.libraryFileGoneRefusal(slot, name, v)
	}
	return s.libraryServerOfflineRefusal(slot, name, v)
}

// libraryFileGoneCode is the code of a library key refused because its music
// server answers but no longer has the key's music (#919, #733, #726): its
// library was re-indexed, or the folder or drive with the music is not there
// any more. Passed through raw by the desktop app (friendlyError) and read by
// the phone page, both for the server's name, like libraryServerOfflineCode.
const libraryFileGoneCode = "library-file-gone"

// libraryFileGoneRefusal is the answer to an app or phone press on a library
// key whose music server answers but could not find the key's music again:
// the speaker is not handed a file it can only answer with AUDIO_ERROR_BAD_URL.
// The body names the server ("server") and says "gone", plus the code and an
// English sentence for a client that knows neither. There is no "offline"
// field: the server is on, and a client must not say it is not answering.
func (s *Server) libraryFileGoneRefusal(slot int, name string, gone *libraryKeyUnplayable) *playRefusal {
	server := "The music server"
	if gone.Server != "" {
		server = "The music server " + gone.Server
	}
	msg := server + " answers, but no longer has the music on this key. " +
		"Usually its library was re-indexed, or the folder or drive with the music is not available. " +
		"Check the server's library (Windows Media Player: Manage libraries > Music), then put the music on the key again."
	s.logger.Warn("preset slot recall (app): the key's music server answers but no longer has its music, the speaker is not handed it",
		"slot", slot, "name", name, "server", gone.Server)
	return jsonRefusal(http.StatusBadGateway, map[string]any{
		"error": msg, "code": libraryFileGoneCode,
		"gone": true, "server": gone.Server,
		"slot": slot, "name": name,
	})
}

// logHardwareLibraryVerdict is the line a hardware key press leaves when its
// library key cannot play. A hardware press has no screen to say so, so the
// press goes ahead as it always did; the log is what a support bundle reads.
func (s *Server) logHardwareLibraryVerdict(slot int, name string, v *libraryKeyUnplayable) {
	if v == nil {
		return
	}
	if v.FileGone {
		s.logger.Warn("preset slot recall (hardware): the key's music server answers but no longer has its music; "+
			"the press goes ahead, the speaker will probably not play it. Check the server's library, then put the music on the key again",
			"slot", slot, "name", name, "server", v.Server)
		return
	}
	s.logger.Warn("preset slot recall (hardware): the key's music server is not answering; the press goes ahead, the speaker will probably not play it",
		"slot", slot, "name", name, "server", v.Server)
}

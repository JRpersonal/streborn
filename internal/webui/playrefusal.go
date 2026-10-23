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

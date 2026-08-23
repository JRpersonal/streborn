// Typed parsing of the firmware's error envelope.
//
// The Bose Web API answers failed calls with an <errors deviceID> envelope
// wrapping one <error value name severity>text</error> element; a malformed
// request may instead get a bare <error>text</error> body. The deviceID
// attribute is the raw 12-hex MAC, which is why substring-matching the whole
// error string for a code like 5510 or 5580 is a latent false positive: a MAC
// that happens to contain that digit run satisfies the check on EVERY failure
// of the call (~0.014% of boxes per code). This file gives callers the typed
// fields so they can stop matching substrings.

package boxapi

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// BoxError is a >=400 reply from the box, with the firmware's own error
// envelope parsed out of the body when it carried one.
//
// Value/Name/Severity/Detail hold the first <error> element (the firmware has
// not been seen sending more than one) and stay empty when the body was not
// an envelope, so Value == "" means "untyped", never "success". Reach it with
// errors.As; the webui 5510/5580 helpers are the intended first consumers.
type BoxError struct {
	Path     string // request path, e.g. "/addGroup"
	Status   int    // HTTP status of the reply
	Value    string // numeric error code, e.g. "5510"; "" when untyped
	Name     string // e.g. "GROUP_ALREADY_EXISTS"
	Severity string
	Detail   string // the error element's own text
	Body     string // raw reply body as read (callers cap it), verbatim
}

// Error reproduces the pre-typed message byte for byte. Substring consumers
// exist (webui's isGroupExistsErr/isMargeGroupErr) and the webui handlers
// surface this text to the app, so the shape is load-bearing: change it and
// those checks and their field playbooks go blind.
func (e *BoxError) Error() string {
	return fmt.Sprintf("box %s: %d: %s", e.Path, e.Status, e.Body)
}

// newBoxError builds the typed error for a >=400 reply. Parsing is best
// effort: a truncated or non-envelope body leaves the typed fields empty and
// only the verbatim message remains.
func newBoxError(path string, status int, body []byte) *BoxError {
	e := &BoxError{Path: path, Status: status, Body: string(body)}
	if el, ok := decodeErrorEnvelope(body); ok {
		e.Value = strings.TrimSpace(el.Value)
		e.Name = strings.TrimSpace(el.Name)
		e.Severity = strings.TrimSpace(el.Severity)
		e.Detail = strings.TrimSpace(el.Text)
	}
	return e
}

// errorElement is one <error> element: the value/name/severity attributes
// plus the element text. Shared by the >=400 envelope parse and the 2xx
// detection below.
type errorElement struct {
	Value    string `xml:"value,attr"`
	Name     string `xml:"name,attr"`
	Severity string `xml:"severity,attr"`
	Text     string `xml:",chardata"`
}

// decodeErrorEnvelope reports whether body's ROOT element is the firmware's
// error envelope (<errors>, or the bare <error> a malformed request gets)
// and returns the first error element when it is. Root-only on purpose: an
// <error> nested inside a legitimate reply must not count (the 200 bodies in
// webui's nativeselect carry one), and substring matching over the body is
// exactly the MAC false positive this file exists to end.
func decodeErrorEnvelope(body []byte) (errorElement, bool) {
	switch rootElementName(body) {
	case "errors":
		var env struct {
			Errors []errorElement `xml:"error"`
		}
		if xml.Unmarshal(ensureUTF8(body), &env) != nil || len(env.Errors) == 0 {
			// The root says envelope; a decode failure (truncated body) or an
			// empty envelope still counts as one, just without typed fields.
			return errorElement{}, true
		}
		return env.Errors[0], true
	case "error":
		var el errorElement
		if xml.Unmarshal(ensureUTF8(body), &el) != nil {
			return errorElement{}, true
		}
		return el, true
	}
	return errorElement{}, false
}

// rootElementName returns the local name of the document's first start
// element, or "" when the body is not XML.
func rootElementName(body []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(ensureUTF8(body)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

// noteErrorEnvelope logs a 2xx reply whose body is the firmware's error
// envelope. Log only, and deliberately so: the API doc never binds the
// envelope to a status code and the firmware demonstrably answers 200 with an
// <error> body (webui's nativeselect), but nobody has surveyed what the 2xx
// bodies of /name, /setup or /key carry per chassis. Returning an error here
// before that fleet survey could turn a working flow with a benign envelope
// into a failure, so enforcement waits for the survey's verdict.
func (c *Client) noteErrorEnvelope(path string, body []byte) {
	if len(bytes.TrimSpace(body)) == 0 {
		return
	}
	el, ok := decodeErrorEnvelope(body)
	if !ok {
		return
	}
	if v, n := strings.TrimSpace(el.Value), strings.TrimSpace(el.Name); v != "" || n != "" {
		// Info on purpose: this fires only when the box smuggles a real error
		// code into a success reply, which is the one signal the fleet survey
		// needs inside default bundles (the NAND ring keeps Info and above).
		c.logger().Info("box answered 2xx with an error envelope", "path", path, "value", v, "name", n)
		return
	}
	// Envelope-shaped body with no code: not actionable, keep it out of the
	// NAND ring.
	c.logger().Debug("box answered 2xx with an error element", "path", path, "detail", clip(strings.TrimSpace(el.Text), 200))
}

// clip bounds a log payload so a pathological body cannot bloat a log line.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

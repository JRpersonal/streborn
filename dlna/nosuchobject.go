package dlna

import (
	"encoding/xml"
	"errors"
	"strings"
)

// ErrNoSuchObject marks a ContentDirectory answer that says the object asked
// for is not there: a UPnP fault 701 (no such object) or 710 (no such
// container), or a BrowseMetadata answer that holds no such item. It is the
// only failure that says something about what a server holds; a refused or
// busy server (401, 403, 503, a 501 "Action Failed"), a garbled answer or a
// connection that failed say nothing about it. Test with errors.Is.
var ErrNoSuchObject = errors.New("no such object")

// answerError is a failed ContentDirectory call whose message stays the one
// line it always was, while errors.Is can still tell whether the server said
// the object is not there.
type answerError struct {
	msg    string
	noSuch bool
}

func (e *answerError) Error() string { return e.msg }

func (e *answerError) Is(target error) bool { return e.noSuch && target == ErrNoSuchObject }

// browseFault is the error for a Browse answer that was not a 200: prefix,
// then the server's fault in one readable line (soapFaultMessage).
func browseFault(prefix string, raw []byte, status int) error {
	return &answerError{msg: prefix + ": " + soapFaultMessage(raw, status), noSuch: noSuchObjectFault(raw)}
}

// noSuchObjectFault reports whether a SOAP fault is UPnP 701 or 710.
func noSuchObjectFault(raw []byte) bool {
	var env struct {
		Code string `xml:"Body>Fault>detail>UPnPError>errorCode"`
	}
	if err := xml.Unmarshal(raw, &env); err != nil {
		return false
	}
	switch strings.TrimSpace(env.Code) {
	case "701", "710":
		return true
	}
	return false
}

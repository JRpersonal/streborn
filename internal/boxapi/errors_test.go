package boxapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newRawBox serves fn for every request (any path, any status) and returns a
// Client aimed at it via rewriteTransport, so Client.url's hardcoded :8090
// still lands on the test server. Complements newFakeBox, which can only
// answer 200 with fixed bodies.
func newRawBox(t *testing.T, fn http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(fn)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	return &Client{
		Host: "ignored",
		HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &rewriteTransport{to: u}},
	}
}

// A >=400 envelope reply must come back as an errors.As-able *BoxError whose
// typed fields are taken from the <error> element's ATTRIBUTES alone. The
// fixture's deviceID deliberately contains the digit run 5580: the raw MAC in
// the envelope is what makes the webui substring helpers fire on every
// failure of a box with an unlucky MAC, and the typed Value is how that false
// positive dies.
func TestPostXMLTypedErrorEnvelope(t *testing.T) {
	body := `<errors deviceID="AC5580123456"><error value="1019" name="CLIENT_XML_ERROR" severity="Unknown">1019</error></errors>`
	c := newRawBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	})
	err := c.SetName(context.Background(), "Kitchen")
	if err == nil {
		t.Fatal("expected an error from a 500 reply")
	}
	var be *BoxError
	if !errors.As(err, &be) {
		t.Fatalf("error is not errors.As-able to *BoxError: %T %v", err, err)
	}
	if be.Path != "/name" || be.Status != 500 {
		t.Errorf("path/status wrong: %+v", be)
	}
	if be.Value != "1019" || be.Name != "CLIENT_XML_ERROR" || be.Severity != "Unknown" || be.Detail != "1019" {
		t.Errorf("typed fields wrong: %+v", be)
	}
	if strings.Contains(be.Value, "5580") {
		t.Errorf("MAC digits leaked into the typed value: %q", be.Value)
	}
	// The message must stay byte for byte what fmt.Errorf produced before the
	// type existed: webui's isGroupExistsErr/isMargeGroupErr parse substrings
	// of it and the webui handlers surface it to the app.
	want := fmt.Sprintf("box %s: %d: %s", "/name", 500, body)
	if err.Error() != want {
		t.Errorf("Error() text drifted:\n got %q\nwant %q", err.Error(), want)
	}
}

// The malformed-request form is a bare <error> body with no attributes; the
// typed value stays empty and the element text lands in Detail. The fixture
// text is invented: only the element shape is the firmware's.
func TestPostXMLTypedErrorBareElement(t *testing.T) {
	body := `<error>request rejected as malformed</error>`
	c := newRawBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	})
	err := c.SetName(context.Background(), "Kitchen")
	var be *BoxError
	if !errors.As(err, &be) {
		t.Fatalf("error is not a *BoxError: %T %v", err, err)
	}
	if be.Value != "" || be.Name != "" {
		t.Errorf("bare element must not invent a code: %+v", be)
	}
	if be.Detail != "request rejected as malformed" {
		t.Errorf("detail wrong: %q", be.Detail)
	}
	want := fmt.Sprintf("box %s: %d: %s", "/name", 400, body)
	if err.Error() != want {
		t.Errorf("Error() text drifted:\n got %q\nwant %q", err.Error(), want)
	}
}

// A non-XML error body stays untyped but keeps the verbatim message.
func TestPostXMLTypedErrorNonEnvelopeBody(t *testing.T) {
	c := newRawBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("Bad Gateway"))
	})
	err := c.SetName(context.Background(), "Kitchen")
	var be *BoxError
	if !errors.As(err, &be) {
		t.Fatalf("error is not a *BoxError: %T %v", err, err)
	}
	if be.Value != "" || be.Name != "" || be.Detail != "" {
		t.Errorf("non-envelope body must stay untyped: %+v", be)
	}
	want := fmt.Sprintf("box %s: %d: %s", "/name", 502, "Bad Gateway")
	if err.Error() != want {
		t.Errorf("Error() text drifted:\n got %q\nwant %q", err.Error(), want)
	}
}

// postXMLInto shares the >=400 contract with postXML.
func TestPostXMLIntoTypedErrorEnvelope(t *testing.T) {
	body := `<errors deviceID="AABBCCDDEEFF"><error value="5510" name="GROUP_ALREADY_EXISTS" severity="Unknown">5510</error></errors>`
	c := newRawBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(body))
	})
	var dst struct{}
	err := c.postXMLInto(context.Background(), "/navigate", "<x/>", &dst)
	var be *BoxError
	if !errors.As(err, &be) {
		t.Fatalf("error is not a *BoxError: %T %v", err, err)
	}
	if be.Value != "5510" || be.Name != "GROUP_ALREADY_EXISTS" {
		t.Errorf("typed fields wrong: %+v", be)
	}
	want := fmt.Sprintf("box %s: %d: %s", "/navigate", 409, body)
	if err.Error() != want {
		t.Errorf("Error() text drifted:\n got %q\nwant %q", err.Error(), want)
	}
}

// A 2xx reply whose body is the error envelope must stay a SUCCESS (log-only
// until the fleet survey settles what 2xx bodies carry per chassis) and must
// put value/name into the log for that survey to read.
func TestPostXML2xxErrorEnvelopeIsLogOnly(t *testing.T) {
	body := `<errors deviceID="AABBCCDDEEFF"><error value="1005" name="UNKNOWN_SOURCE_ERROR" severity="Unknown">1005</error></errors>`
	c := newRawBox(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	var buf bytes.Buffer
	c.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := c.SetName(context.Background(), "Kitchen"); err != nil {
		t.Fatalf("2xx envelope must stay log-only, got error: %v", err)
	}
	logged := buf.String()
	for _, want := range []string{"1005", "UNKNOWN_SOURCE_ERROR", "/name"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log line missing %q:\n%s", want, logged)
		}
	}
	if !strings.Contains(logged, "level=INFO") {
		t.Errorf("an envelope with a code must log at Info so it reaches default bundles:\n%s", logged)
	}
}

// Detection is root-element-only: an <error> nested inside a legitimate 2xx
// reply, or a normal <status> body, must not trip it. Substring matching over
// the body is exactly the false-positive class the typed parse retires.
func TestPostXML2xxNormalBodiesNotFlagged(t *testing.T) {
	for _, body := range []string{
		`<status>/name</status>`,
		`<status>OK<error>nested, not an envelope</error></status>`,
		``,
	} {
		c := newRawBox(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		var buf bytes.Buffer
		c.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		if err := c.SetName(context.Background(), "Kitchen"); err != nil {
			t.Fatalf("body %q: unexpected error: %v", body, err)
		}
		if buf.Len() != 0 {
			t.Errorf("body %q must not be flagged, logged:\n%s", body, buf.String())
		}
	}
}

// postXMLInto flags a 2xx envelope the same way and still decodes dst as it
// always did (here: nothing to decode, no error).
func TestPostXMLInto2xxErrorEnvelopeIsLogOnly(t *testing.T) {
	body := `<errors deviceID="AABBCCDDEEFF"><error value="1036" name="UNABLE_TO_PROCESS_NOT_LOGGED_IN" severity="Unknown">1036</error></errors>`
	c := newRawBox(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	var buf bytes.Buffer
	c.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var dst struct{}
	if err := c.postXMLInto(context.Background(), "/navigate", "<x/>", &dst); err != nil {
		t.Fatalf("2xx envelope must stay log-only, got error: %v", err)
	}
	if !strings.Contains(buf.String(), "1036") || !strings.Contains(buf.String(), "/navigate") {
		t.Errorf("log line missing envelope data:\n%s", buf.String())
	}
}

// decodeErrorEnvelope table: what counts as an envelope and what the typed
// element carries.
func TestDecodeErrorEnvelope(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		envelope bool
		value    string
	}{
		{"full envelope", `<errors deviceID="AABBCCDDEEFF"><error value="5580" name="GROUP_CREATE_GROUP_ON_MARGE_ERROR" severity="Unknown">5580</error></errors>`, true, "5580"},
		{"bare error", `<error>parse trouble</error>`, true, ""},
		{"empty envelope", `<errors deviceID="AABBCCDDEEFF"></errors>`, true, ""},
		{"truncated envelope", `<errors deviceID="AABBCCDDEEFF"><error value="5510"`, true, ""},
		{"status reply", `<status>/setup</status>`, false, ""},
		{"nested error", `<info><error>not root</error></info>`, false, ""},
		{"not xml", `Bad Request`, false, ""},
		{"empty", ``, false, ""},
		{"xml decl then envelope", `<?xml version="1.0" encoding="UTF-8" ?><errors deviceID="AABBCCDDEEFF"><error value="1019" name="CLIENT_XML_ERROR" severity="Unknown">1019</error></errors>`, true, "1019"},
	}
	for _, tc := range cases {
		el, ok := decodeErrorEnvelope([]byte(tc.body))
		if ok != tc.envelope {
			t.Errorf("%s: envelope = %v, want %v", tc.name, ok, tc.envelope)
			continue
		}
		if el.Value != tc.value {
			t.Errorf("%s: value = %q, want %q", tc.name, el.Value, tc.value)
		}
	}
}

package dlna

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func upnpFault(code string) string {
	return `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>` +
		`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring>` +
		`<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>` + code + `</errorCode></UPnPError></detail>` +
		`</s:Fault></s:Body></s:Envelope>`
}

// Only a server that says the object is not there marks ErrNoSuchObject: a
// 701 or 710 fault, or a BrowseMetadata answer without the item. A refused,
// busy or failing server says nothing about what it holds, and the message
// stays the readable line it always was.
func TestOnlyANoSuchObjectAnswerIsErrNoSuchObject(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"701 no such object", http.StatusInternalServerError, upnpFault("701"), true},
		{"710 no such container", http.StatusInternalServerError, upnpFault("710"), true},
		{"501 action failed", http.StatusInternalServerError, upnpFault("501"), false},
		{"503 busy", http.StatusServiceUnavailable, "rescanning", false},
		{"403 refused", http.StatusForbidden, "<html>forbidden</html>", false},
		{"200 garbled", http.StatusOK, "<s:Envelope", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer ts.Close()
			srv := Server{CDSControlURL: ts.URL}
			_, berr := Browse(context.Background(), srv, "c-1", 0, 10)
			_, merr := ItemMetadata(context.Background(), srv, "obj-7")
			for name, err := range map[string]error{"Browse": berr, "ItemMetadata": merr} {
				if err == nil {
					t.Fatalf("%s: no error", name)
				}
				if got := errors.Is(err, ErrNoSuchObject); got != c.want {
					t.Errorf("%s: errors.Is(%v, ErrNoSuchObject) = %v, want %v", name, err, got, c.want)
				}
			}
			if c.status != http.StatusOK && !strings.HasPrefix(berr.Error(), "browse failed: ") {
				t.Errorf("Browse message changed: %q", berr)
			}
		})
	}
}

func TestItemMetadataAnswerWithoutTheItemIsErrNoSuchObject(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
			`<u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>` +
			`&lt;DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/"&gt;&lt;/DIDL-Lite&gt;` +
			`</Result><NumberReturned>0</NumberReturned><TotalMatches>0</TotalMatches></u:BrowseResponse></s:Body></s:Envelope>`))
	}))
	defer ts.Close()
	_, err := ItemMetadata(context.Background(), Server{CDSControlURL: ts.URL}, "obj-7")
	if !errors.Is(err, ErrNoSuchObject) {
		t.Fatalf("err = %v, want ErrNoSuchObject", err)
	}
	if err.Error() != `browse metadata: no item "obj-7" in the answer` {
		t.Fatalf("message changed: %q", err)
	}
}

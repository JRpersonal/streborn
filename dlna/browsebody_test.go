package dlna

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Browse request must be well-formed SOAP. A missing space between the
// element name and its namespace ("<u:Browsexmlns:u=") passed every fake
// server in the tests, while both real servers on the maintainer's LAN, a
// Synology and a FRITZ!Box, refused the browse with 400 and 500.
func TestBrowseRequestIsWellFormedSOAP(t *testing.T) {
	for _, flag := range []string{browseChildren, browseMetadata} {
		var body []byte
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}))
		srv := Server{CDSControlURL: ts.URL}
		if _, _, err := browseRaw(context.Background(), srv, "4:cont&1", flag, 0, 10); err != nil {
			t.Fatalf("%s: %v", flag, err)
		}
		ts.Close()

		var env struct {
			Body struct {
				Browse struct {
					XMLName    xml.Name
					ObjectID   string
					BrowseFlag string
				} `xml:"Browse"`
			} `xml:"Body"`
		}
		if err := xml.Unmarshal(body, &env); err != nil {
			t.Fatalf("%s: the request is not valid XML: %v\n%s", flag, err, body)
		}
		b := env.Body.Browse
		if b.XMLName.Space != "urn:schemas-upnp-org:service:ContentDirectory:1" {
			t.Fatalf("%s: Browse is not in the ContentDirectory namespace: %q", flag, b.XMLName.Space)
		}
		if b.ObjectID != "4:cont&1" || b.BrowseFlag != flag {
			t.Fatalf("%s: got ObjectID %q, BrowseFlag %q", flag, b.ObjectID, b.BrowseFlag)
		}
	}
}

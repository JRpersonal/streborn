package boxapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeBox stands in for the Bose firmware on port 8090 and rewrites
// http://host:8090/getZone style calls to the test server's URL.
type fakeBox struct {
	srv *httptest.Server
}

func newFakeBox(t *testing.T, routes map[string]string) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	// boxapi.Client builds URLs as http://<Host>:8090<path>; we need to
	// inject the test host:port directly and bypass that template.
	c := &Client{Host: u.Host, HTTP: &http.Client{Timeout: 2 * time.Second}}
	// Override the url() builder via a custom client struct field would
	// require more plumbing; instead we use a wrapper here that strips
	// the implicit ":8090" by setting Host to the bare test host. To
	// make that work the routes have to anticipate the trailing port
	// segment, so we mount with the path Client.url produced. Simpler:
	// override the path comparison by registering routes the test cares
	// about behind a strip prefix.
	stripped := http.NewServeMux()
	for path, body := range routes {
		path := path
		body := body
		stripped.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			_, _ = w.Write([]byte(body))
		})
	}
	srv.Config.Handler = stripped
	// Patch Client to point at the test host with the port the test
	// server picked, by giving Host="<host>:<port>" — Client.url then
	// produces http://<host>:<port>:8090<path>, which fails. So we
	// instead expose getXMLForTest via the same code path by replacing
	// the HTTP client's transport with a rewriter.
	c.HTTP.Transport = &rewriteTransport{to: u}
	c.Host = "ignored"
	return c, func() { srv.Close() }
}

// rewriteTransport sends every request to the configured target host,
// preserving the request path. Used so the existing Client.url builder
// (which hardcodes :8090) still hits the test server.
type rewriteTransport struct {
	to *url.URL
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = t.to.Scheme
	req2.URL.Host = t.to.Host
	return http.DefaultTransport.RoundTrip(req2)
}

func TestGetZoneEmpty(t *testing.T) {
	c, stop := newFakeBox(t, map[string]string{
		"/getZone": `<?xml version="1.0" encoding="UTF-8" ?><zone />`,
	})
	defer stop()
	z, err := c.GetZone(context.Background())
	if err != nil {
		t.Fatalf("GetZone error: %v", err)
	}
	if z.Master != "" {
		t.Errorf("expected empty master, got %q", z.Master)
	}
	if len(z.Members) != 0 {
		t.Errorf("expected no members, got %d", len(z.Members))
	}
}

func TestGetZoneWithMembers(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8" ?>` +
		`<zone master="AAAAAAAAAAAA" senderIPAddress="192.0.2.10">` +
		`<member ipaddress="192.0.2.11" role="NORMAL">BBBBBBBBBBBB</member>` +
		`<member ipaddress="192.0.2.12" role="NORMAL">CCCCCCCCCCCC</member>` +
		`</zone>`
	c, stop := newFakeBox(t, map[string]string{"/getZone": xml})
	defer stop()
	z, err := c.GetZone(context.Background())
	if err != nil {
		t.Fatalf("GetZone error: %v", err)
	}
	if z.Master != "AAAAAAAAAAAA" {
		t.Errorf("master: got %q", z.Master)
	}
	if z.SenderIP != "192.0.2.10" {
		t.Errorf("senderIP: got %q", z.SenderIP)
	}
	if len(z.Members) != 2 {
		t.Fatalf("members: got %d", len(z.Members))
	}
	if z.Members[0].DeviceID != "BBBBBBBBBBBB" || z.Members[0].IP != "192.0.2.11" {
		t.Errorf("first member wrong: %+v", z.Members[0])
	}
}

func TestGetGroupEmpty(t *testing.T) {
	c, stop := newFakeBox(t, map[string]string{
		"/getGroup": `<?xml version="1.0" encoding="UTF-8" ?><group />`,
	})
	defer stop()
	g, err := c.GetGroup(context.Background())
	if err != nil {
		t.Fatalf("GetGroup error: %v", err)
	}
	if g.ID != "" || len(g.Members) != 0 {
		t.Errorf("expected empty group, got %+v", g)
	}
}

func TestGetZoneTrimsWhitespace(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8" ?>` +
		`<zone master="  AAAA  " senderIPAddress="  192.0.2.10  ">` +
		`<member ipaddress="192.0.2.11"> ` + "\n  BBBB \t" + ` </member>` +
		`</zone>`
	c, stop := newFakeBox(t, map[string]string{"/getZone": xml})
	defer stop()
	z, err := c.GetZone(context.Background())
	if err != nil {
		t.Fatalf("GetZone error: %v", err)
	}
	if z.Master != "AAAA" {
		t.Errorf("master not trimmed: %q", z.Master)
	}
	if z.SenderIP != "192.0.2.10" {
		t.Errorf("senderIP not trimmed: %q", z.SenderIP)
	}
	if !strings.HasPrefix(z.Members[0].DeviceID, "BBBB") || strings.ContainsAny(z.Members[0].DeviceID, "\t\n ") {
		t.Errorf("member DeviceID not trimmed: %q", z.Members[0].DeviceID)
	}
}

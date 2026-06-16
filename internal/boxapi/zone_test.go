package boxapi

import (
	"context"
	"io"
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

// TestGetZoneDropsMasterMember: the firmware /getZone body lists the master as a
// member too, but Zone.Members must mean the slaves only, so the master entry is
// filtered out (keeps len(Members)==len(slaves) for every consumer).
func TestGetZoneDropsMasterMember(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8" ?>` +
		`<zone master="AAAAAAAAAAAA" senderIPAddress="192.0.2.10">` +
		`<member ipaddress="192.0.2.10">AAAAAAAAAAAA</member>` +
		`<member ipaddress="192.0.2.11">BBBBBBBBBBBB</member>` +
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
	if len(z.Members) != 1 {
		t.Fatalf("master must be filtered from members, got %d: %+v", len(z.Members), z.Members)
	}
	if z.Members[0].DeviceID != "BBBBBBBBBBBB" {
		t.Errorf("remaining member wrong: %+v", z.Members[0])
	}
}

func TestZoneXML(t *testing.T) {
	got := zoneXML(
		ZoneMember{DeviceID: "AAAA", IP: "192.0.2.10"},
		[]ZoneMember{{DeviceID: "BBBB", IP: "192.0.2.11"}, {DeviceID: "CCCC", IP: "192.0.2.12"}},
	)
	want := `<zone master="AAAA" senderIPAddress="192.0.2.10">` +
		`<member ipaddress="192.0.2.11">BBBB</member>` +
		`<member ipaddress="192.0.2.12">CCCC</member>` +
		`</zone>`
	if got != want {
		t.Errorf("zoneXML:\n got %q\nwant %q", got, want)
	}
}

func TestZoneXMLNoSenderIP(t *testing.T) {
	got := zoneXML(ZoneMember{DeviceID: "AAAA"}, []ZoneMember{{DeviceID: "BBBB", IP: "192.0.2.11"}})
	if strings.Contains(got, "senderIPAddress") {
		t.Errorf("expected no senderIPAddress when master IP empty: %q", got)
	}
}

func TestZoneXMLWithRole(t *testing.T) {
	got := zoneXML(
		ZoneMember{DeviceID: "AAAA", IP: "192.0.2.10"},
		[]ZoneMember{{DeviceID: "BBBB", IP: "192.0.2.11", Role: "LEFT"}, {DeviceID: "CCCC", IP: "192.0.2.12"}},
	)
	if !strings.Contains(got, `<member ipaddress="192.0.2.11" role="LEFT">BBBB</member>`) {
		t.Errorf("expected role attr for the LEFT member: %q", got)
	}
	// A member with no role must not gain an empty role attr.
	if strings.Contains(got, `role=""`) {
		t.Errorf("must not emit empty role attr: %q", got)
	}
}

// TestSetZonePostsCorrectly confirms SetZone POSTs the zone body to /setZone.
func TestSetZonePostsCorrectly(t *testing.T) {
	var gotPath, gotBody, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := &Client{Host: "ignored", HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &rewriteTransport{to: u}}}

	err := c.SetZone(context.Background(),
		ZoneMember{DeviceID: "AAAA", IP: "192.0.2.10"},
		[]ZoneMember{{DeviceID: "BBBB", IP: "192.0.2.11"}})
	if err != nil {
		t.Fatalf("SetZone error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/setZone" {
		t.Errorf("expected POST /setZone, got %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(gotBody, `master="AAAA"`) ||
		!strings.Contains(gotBody, `<member ipaddress="192.0.2.11">BBBB</member>`) {
		t.Errorf("setZone body wrong: %q", gotBody)
	}
	// The /setZone member list MUST include the master itself as the FIRST
	// member (Bose API + thlucas1 + HA + gesellix all agree), then the slaves.
	if !strings.Contains(gotBody, `<member ipaddress="192.0.2.10">AAAA</member>`) {
		t.Errorf("setZone body must include the master as a member: %q", gotBody)
	}
	wantBody := `<zone master="AAAA" senderIPAddress="192.0.2.10">` +
		`<member ipaddress="192.0.2.10">AAAA</member>` +
		`<member ipaddress="192.0.2.11">BBBB</member>` +
		`</zone>`
	if gotBody != wantBody {
		t.Errorf("setZone body:\n got %q\nwant %q", gotBody, wantBody)
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

// TestGetGroupEmptyBody covers the live taigan behavior: an unpaired box
// answers /getGroup with an empty 200 body (NOT <group/>). The getXML
// empty-body guard must turn that into a zero Group, not an xml.EOF error.
func TestGetGroupEmptyBody(t *testing.T) {
	c, stop := newFakeBox(t, map[string]string{"/getGroup": ""})
	defer stop()
	g, err := c.GetGroup(context.Background())
	if err != nil {
		t.Fatalf("GetGroup error on empty body: %v", err)
	}
	if g.ID != "" || len(g.Members) != 0 {
		t.Errorf("expected empty group from empty body, got %+v", g)
	}
}

// TestGetGroupRoles parses the documented stereo-pair schema
// (roles>groupRole with deviceId/role/ipAddress child elements + masterDeviceId).
func TestGetGroupRoles(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8" ?>` +
		`<group id="1"><name>Living room</name><masterDeviceId>AAAAAAAAAAAA</masterDeviceId>` +
		`<roles>` +
		`<groupRole><deviceId>AAAAAAAAAAAA</deviceId><role>LEFT</role><ipAddress>192.0.2.11</ipAddress></groupRole>` +
		`<groupRole><deviceId>BBBBBBBBBBBB</deviceId><role>RIGHT</role><ipAddress>192.0.2.12</ipAddress></groupRole>` +
		`</roles></group>`
	c, stop := newFakeBox(t, map[string]string{"/getGroup": xml})
	defer stop()
	g, err := c.GetGroup(context.Background())
	if err != nil {
		t.Fatalf("GetGroup error: %v", err)
	}
	if g.ID != "1" || g.Name != "Living room" || g.MasterDeviceID != "AAAAAAAAAAAA" {
		t.Errorf("group header wrong: %+v", g)
	}
	if len(g.Members) != 2 {
		t.Fatalf("members: got %d", len(g.Members))
	}
	if g.Members[0].DeviceID != "AAAAAAAAAAAA" || g.Members[0].Role != "LEFT" || g.Members[0].IP != "192.0.2.11" {
		t.Errorf("LEFT member wrong: %+v", g.Members[0])
	}
	if g.Members[1].Role != "RIGHT" {
		t.Errorf("RIGHT member wrong: %+v", g.Members[1])
	}
}

func TestGroupXML(t *testing.T) {
	got := groupXML("Stereo pair", "AAAA", []ZoneMember{
		{DeviceID: "AAAA", IP: "192.0.2.11", Role: "LEFT"},
		{DeviceID: "BBBB", IP: "192.0.2.12", Role: "RIGHT"},
	})
	want := `<group><name>Stereo pair</name><masterDeviceId>AAAA</masterDeviceId><roles>` +
		`<groupRole><deviceId>AAAA</deviceId><role>LEFT</role><ipAddress>192.0.2.11</ipAddress></groupRole>` +
		`<groupRole><deviceId>BBBB</deviceId><role>RIGHT</role><ipAddress>192.0.2.12</ipAddress></groupRole>` +
		`</roles></group>`
	if got != want {
		t.Errorf("groupXML:\n got %q\nwant %q", got, want)
	}
}

// TestAddGroupPostsCorrectly confirms AddGroup POSTs the group body to /addGroup.
func TestAddGroupPostsCorrectly(t *testing.T) {
	var gotPath, gotBody, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := &Client{Host: "ignored", HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &rewriteTransport{to: u}}}

	err := c.AddGroup(context.Background(), "Stereo pair", "AAAA", []ZoneMember{
		{DeviceID: "AAAA", IP: "192.0.2.11", Role: "LEFT"},
		{DeviceID: "BBBB", IP: "192.0.2.12", Role: "RIGHT"},
	})
	if err != nil {
		t.Fatalf("AddGroup error: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/addGroup" {
		t.Errorf("expected POST /addGroup, got %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(gotBody, `<masterDeviceId>AAAA</masterDeviceId>`) ||
		!strings.Contains(gotBody, `<role>RIGHT</role>`) {
		t.Errorf("addGroup body wrong: %q", gotBody)
	}
}

// TestRemoveGroupIsGet confirms RemoveGroup uses GET /removeGroup (no body).
func TestRemoveGroupIsGet(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusOK) // empty body, like the real firmware
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := &Client{Host: "ignored", HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &rewriteTransport{to: u}}}

	if err := c.RemoveGroup(context.Background()); err != nil {
		t.Fatalf("RemoveGroup error: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/removeGroup" {
		t.Errorf("expected GET /removeGroup, got %s %s", gotMethod, gotPath)
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

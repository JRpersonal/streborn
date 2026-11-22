package boxapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestMusicServiceCredentialsBodyEscapes(t *testing.T) {
	got := musicServiceCredentialsBody("PANDORA", "Pandora Music Service", "a&b@example.com", `p<"'>`)
	want := `<credentials source="PANDORA" displayName="Pandora Music Service"><user>a&amp;b@example.com</user><pass>p&lt;&quot;&apos;&gt;</pass></credentials>`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSetAndRemoveMusicServiceAccount(t *testing.T) {
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies[r.URL.Path] = string(b)
		_, _ = w.Write([]byte(`<status>/setMusicServiceAccount</status>`))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	c := &Client{Host: "192.0.2.1", HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &rewriteTransport{to: u}}}
	ctx := context.Background()
	if err := c.SetMusicServiceAccount(ctx, "PANDORA", "Pandora Music Service", "user@example.com", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveMusicServiceAccount(ctx, "PANDORA", "Pandora Music Service", "user@example.com"); err != nil {
		t.Fatal(err)
	}
	if want := `<credentials source="PANDORA" displayName="Pandora Music Service"><user>user@example.com</user><pass>pw</pass></credentials>`; bodies["/setMusicServiceAccount"] != want {
		t.Errorf("set body %q", bodies["/setMusicServiceAccount"])
	}
	if want := `<credentials source="PANDORA" displayName="Pandora Music Service"><user>user@example.com</user><pass></pass></credentials>`; bodies["/removeMusicServiceAccount"] != want {
		t.Errorf("remove body %q", bodies["/removeMusicServiceAccount"])
	}
}

// The source-list notification names the speaker's own device id from /info.
func TestNotifySourcesUpdated(t *testing.T) {
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies[r.Method+" "+r.URL.Path] = string(b)
		if r.URL.Path == "/info" {
			_, _ = w.Write([]byte(`<info deviceID="AABBCCDDEEFF"><name>Test</name></info>`))
			return
		}
		_, _ = w.Write([]byte(`<status>/notification</status>`))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	c := &Client{Host: "192.0.2.1", HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &rewriteTransport{to: u}}}
	if err := c.NotifySourcesUpdated(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := `<updates deviceID="AABBCCDDEEFF"><sourcesUpdated/></updates>`; bodies["POST /notification"] != want {
		t.Fatalf("notification body %q", bodies["POST /notification"])
	}
}

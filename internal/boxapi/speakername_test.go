package boxapi

import (
	"context"
	"io"
	"net/http"
	"testing"
)

// SetName must never hand the firmware a name its UPnP renderer cannot
// survive: "&" breaks the renderer's device description on the next boot and
// with it Spotify and the music library (#1582).
func TestSetNameSanitises(t *testing.T) {
	var got string
	c := newRawBox(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		_, _ = w.Write([]byte(`<name>ok</name>`))
	})
	if err := c.SetName(context.Background(), "Tom & <Jerry>"); err != nil {
		t.Fatal(err)
	}
	if want := `<name>Tom + Jerry</name>`; got != want {
		t.Fatalf("posted %q, want %q", got, want)
	}
}

// GetInfo says when /info was not UTF-8, because the name is then stored in
// a form that breaks the renderer even though it reads back fine as Latin-1.
func TestGetInfoFlagsNameNotUTF8(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
		name string
	}{
		{`<info deviceID="device-id-here"><name>K` + "\xfc" + `che</name></info>`, true, "Küche"},
		{`<info deviceID="device-id-here"><name>Küche</name></info>`, false, "Küche"},
	} {
		c := newRawBox(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(tc.body))
		})
		info, err := c.GetInfo(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.NameNotUTF8 != tc.want || info.Name != tc.name {
			t.Errorf("GetInfo = name %q notUTF8 %v, want %q %v", info.Name, info.NameNotUTF8, tc.name, tc.want)
		}
	}
}

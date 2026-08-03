package marge

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The deviceID decides whether the firmware accepts the emulated account at
// all: it looks for its OWN entry in the <devices> block and discards the whole
// payload when it is absent, which silently takes the source registration and
// the hardware preset keys with it. So the two paths that can correct a wrong
// startup guess are worth pinning.

func TestDeviceIDFromAddDeviceBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "the shape the box actually posts",
			body: `<device deviceid="94E36DF9CE40"><name>SoundTouch 10</name>` +
				`<macaddress>94E36DF9CE40</macaddress></device>`,
			want: "94E36DF9CE40",
		},
		{
			name: "lower case is normalised, since the account block must match",
			body: `<device deviceid="94e36df9ce40"></device>`,
			want: "94E36DF9CE40",
		},
		{
			name: "no id attribute leaves the current value alone",
			body: `<device><name>SoundTouch 10</name></device>`,
			want: "",
		},
		{
			name: "a separator-formatted MAC is rejected rather than stored wrong",
			body: `<device deviceid="94:E3:6D:F9:CE:40"></device>`,
			want: "",
		},
		{
			name: "a non-hex value is rejected",
			body: `<device deviceid="ZZZZZZZZZZZZ"></device>`,
			want: "",
		},
		{
			name: "not XML at all",
			body: "not xml",
			want: "",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/streaming/account/x/device/", strings.NewReader(tc.body))
			if got := deviceIDFromAddDeviceBody(r); got != tc.want {
				t.Fatalf("deviceIDFromAddDeviceBody() = %q, want %q", got, tc.want)
			}
			// The body must still be readable afterwards: consuming it silently
			// would be a trap for the next handler added to this chain.
			rest, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("body not re-readable: %v", err)
			}
			if string(rest) != tc.body {
				t.Fatalf("body was consumed: got %q, want %q", rest, tc.body)
			}
		})
	}
}

// A confirmed id must survive a restart, or every boot re-opens the window in
// which the account names an id the box does not recognise.
func TestDeviceIDPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deviceid")

	first := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("10CEA9E8CF31"), WithDeviceIDPath(path))
	first.SetDeviceID("94E36DF9CE40")

	// A fresh server with the same wrong guess must come up with the stored id.
	second := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("10CEA9E8CF31"), WithDeviceIDPath(path))
	if got := second.DeviceID(); got != "94E36DF9CE40" {
		t.Fatalf("after restart DeviceID() = %q, want the confirmed id", got)
	}

	// A corrupt file must not strand the agent: the guess has to survive.
	if err := os.WriteFile(path, []byte("nonsense"), 0o644); err != nil {
		t.Fatal(err)
	}
	third := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("10CEA9E8CF31"), WithDeviceIDPath(path))
	if got := third.DeviceID(); got != "10CEA9E8CF31" {
		t.Fatalf("a corrupt stored id must fall back to the guess, got %q", got)
	}

	// No path configured must not panic or write anywhere.
	plain := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("10CEA9E8CF31"))
	if !plain.SetDeviceID("94E36DF9CE40") {
		t.Fatal("SetDeviceID must still work without persistence")
	}
}

func TestSetDeviceIDReportsChange(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("10CEA9E8CF31"))

	if s.SetDeviceID("") {
		t.Fatal("an empty id must not replace a known one")
	}
	if got := s.DeviceID(); got != "10CEA9E8CF31" {
		t.Fatalf("id changed to %q after an empty set", got)
	}
	if !s.SetDeviceID("94E36DF9CE40") {
		t.Fatal("a different id must report a change, so the correction is logged")
	}
	if got := s.DeviceID(); got != "94E36DF9CE40" {
		t.Fatalf("DeviceID() = %q, want the corrected value", got)
	}
	if s.SetDeviceID("94e36df9ce40") {
		t.Fatal("the same id in another case must not report a change")
	}
}

// The account payload must carry the CURRENT id, not the one captured at
// construction: the correction arrives seconds before the box asks for it.
func TestAccountPayloadUsesCorrectedDeviceID(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("10CEA9E8CF31"))
	s.SetAccount(&AccountInfo{AccountEmail: "stick@local"})
	s.SetDeviceID("94E36DF9CE40")

	w := httptest.NewRecorder()
	s.respondMargeAccountFull(w, httptest.NewRequest("GET", "/streaming/account/stick@local/full", nil))

	body := w.Body.String()
	if !strings.Contains(body, `deviceid="94E36DF9CE40"`) {
		t.Fatalf("account payload does not carry the corrected deviceID:\n%s", body)
	}
	if strings.Contains(body, "10CEA9E8CF31") {
		t.Fatalf("account payload still carries the stale guess:\n%s", body)
	}
	// The whole point of getting the id right is that the sources block is
	// honoured, so assert it is actually in there.
	if !strings.Contains(body, "LOCAL_INTERNET_RADIO") {
		t.Fatalf("account payload lost the radio source:\n%s", body)
	}
}

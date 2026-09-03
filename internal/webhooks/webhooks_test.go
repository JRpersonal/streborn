package webhooks

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestDecodePayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		enc     string
		want    []byte
		wantErr bool
	}{
		{
			name:    "empty enc is literal text",
			payload: "hello box",
			enc:     "",
			want:    []byte("hello box"),
		},
		{
			name:    "text enc is literal",
			payload: "0xFF stays literal",
			enc:     "text",
			want:    []byte("0xFF stays literal"),
		},
		{
			name:    "unknown enc falls back to literal",
			payload: "abc",
			enc:     "binary",
			want:    []byte("abc"),
		},
		{
			name:    "hex plain",
			payload: "deadbeef",
			enc:     "hex",
			want:    []byte{0xDE, 0xAD, 0xBE, 0xEF},
		},
		{
			name:    "hex enc case and surrounding space ignored",
			payload: "deadbeef",
			enc:     " HEX ",
			want:    []byte{0xDE, 0xAD, 0xBE, 0xEF},
		},
		{
			name:    "hex with colons dashes and whitespace",
			payload: "de:ad be-ef\n00\t11",
			enc:     "hex",
			want:    []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x11},
		},
		{
			name:    "hex odd length rejected",
			payload: "abc",
			enc:     "hex",
			wantErr: true,
		},
		{
			name:    "hex non-hex digit rejected",
			payload: "zz",
			enc:     "hex",
			wantErr: true,
		},
		{
			name:    "base64 standard",
			payload: "aGVsbG8=",
			enc:     "base64",
			want:    []byte("hello"),
		},
		{
			name:    "base64 surrounding whitespace trimmed",
			payload: "  aGVsbG8=\n",
			enc:     "base64",
			want:    []byte("hello"),
		},
		{
			name:    "base64 invalid rejected",
			payload: "not base64!!",
			enc:     "base64",
			wantErr: true,
		},
		{
			name:    "empty payload text",
			payload: "",
			enc:     "",
			want:    []byte{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodePayload(tt.payload, tt.enc)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("decodePayload(%q, %q) = %v, want error", tt.payload, tt.enc, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodePayload(%q, %q) unexpected error: %v", tt.payload, tt.enc, err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("decodePayload(%q, %q) = %v, want %v", tt.payload, tt.enc, got, tt.want)
			}
		})
	}
}

func TestRateLimited(t *testing.T) {
	s := &Store{lastFireAt: make(map[string]time.Time)}

	if s.rateLimited("thumb") {
		t.Fatal("first fire for an id should be allowed")
	}
	if !s.rateLimited("thumb") {
		t.Fatal("second fire within the window should be blocked")
	}
	if s.rateLimited("preset1") {
		t.Fatal("a different id should be independent and allowed")
	}
	if !s.rateLimited("preset1") {
		t.Fatal("second fire on the other id should be blocked too")
	}

	// Backdate the last fire beyond the 2s window instead of sleeping.
	s.fireMu.Lock()
	s.lastFireAt["thumb"] = time.Now().Add(-3 * time.Second)
	s.fireMu.Unlock()
	if s.rateLimited("thumb") {
		t.Fatal("fire after the window elapsed should be allowed again")
	}
	if !s.rateLimited("thumb") {
		t.Fatal("allowed fire must record a new last-fire time")
	}
}

func TestBuildMagicPacket(t *testing.T) {
	mac, err := net.ParseMAC("AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatalf("ParseMAC: %v", err)
	}

	packet, err := buildMagicPacket(mac)
	if err != nil {
		t.Fatalf("buildMagicPacket(%s) unexpected error: %v", mac, err)
	}
	if len(packet) != 102 {
		t.Fatalf("packet length = %d, want 102", len(packet))
	}
	want := append(bytes.Repeat([]byte{0xFF}, 6), bytes.Repeat(mac, 16)...)
	if !bytes.Equal(packet, want) {
		t.Errorf("packet = % X, want 6x FF then MAC repeated 16 times (% X)", packet, want)
	}

	badMACs := []struct {
		name string
		mac  net.HardwareAddr
	}{
		{"nil", nil},
		{"too short", net.HardwareAddr{0xAA, 0xBB, 0xCC}},
		{"eui-64 (8 bytes)", net.HardwareAddr{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00, 0x11}},
	}
	for _, tt := range badMACs {
		t.Run("reject "+tt.name, func(t *testing.T) {
			if got, err := buildMagicPacket(tt.mac); err == nil {
				t.Fatalf("buildMagicPacket(%v) = %v, want error", tt.mac, got)
			}
		})
	}
}

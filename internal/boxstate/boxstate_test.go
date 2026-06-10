package boxstate

import "testing"

// Cases mirror states observed live on the taigan Portable 2026-06-10.
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		in   Facts
		want State
	}{
		{
			name: "unreachable",
			in:   Facts{Reachable: false},
			want: StateUnreachable,
		},
		{
			name: "oob setup-ap (box is its own AP)",
			in: Facts{
				Reachable: true, IP: "192.168.1.1",
				SetupState: "SETUP_AP_OOB", SystemState: "SETUP_LANG_SET",
			},
			want: StateOOBSetupAP,
		},
		{
			name: "reachable but no usable ip yet",
			in: Facts{
				Reachable: true, IP: "0.0.0.0",
				SetupState: "SETUP_AP_OOB",
			},
			want: StateOOBSetupAP,
		},
		{
			name: "chipset-joined LAN but soundtouch still OOB, no account",
			in: Facts{
				Reachable: true, IP: "192.168.178.79",
				SetupState: "SETUP_AP_OOB", SystemState: "SETUP_LANG_NOT_SET",
				SSID: "JJ3", WifiProfileCount: 1,
			},
			want: StateOnlineNotConfigured,
		},
		{
			name: "on LAN, setup left, account set -> configured",
			in: Facts{
				Reachable: true, IP: "192.168.178.79",
				SetupState: "SETUP_INACTIVE", SystemState: "SETUP_LANG_SET",
				MargeAccountUUID: "stick-bootstrap", SSID: "JJ3",
			},
			want: StateOnlineConfigured,
		},
		{
			name: "on LAN, setup left, but no account yet",
			in: Facts{
				Reachable: true, IP: "192.168.178.50",
				SetupState: "SETUP_INACTIVE", MargeAccountUUID: "",
			},
			want: StateOnlineNotConfigured,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.in); got != tc.want {
				t.Fatalf("Classify(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestStateHelpers(t *testing.T) {
	if !StateOnlineConfigured.IsConfigured() {
		t.Fatal("configured state should report IsConfigured")
	}
	if StateOnlineNotConfigured.IsConfigured() {
		t.Fatal("not-configured state must not report IsConfigured")
	}
	if !StateOnlineNotConfigured.IsOnline() {
		t.Fatal("not-configured state is still online (on LAN)")
	}
	if StateOOBSetupAP.IsOnline() {
		t.Fatal("setup-AP state is not online")
	}
}

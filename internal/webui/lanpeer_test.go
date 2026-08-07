package webui

import "testing"

func TestIsLANPeer(t *testing.T) {
	cases := []struct {
		host string
		want bool
		why  string
	}{
		// The real thing: another speaker on a home network.
		{"192.168.1.44", true, "RFC1918 /16"},
		{"10.0.0.5", true, "RFC1918 /8"},
		{"172.16.9.9", true, "RFC1918 /12"},
		{"169.254.4.4", true, "link-local, a speaker before DHCP"},
		{"fe80::1", true, "IPv6 link-local"},
		{"fd00::1", true, "IPv6 unique-local"},

		// Off the LAN entirely: the case the check exists for.
		{"93.184.216.34", false, "public IPv4"},
		{"2606:4700::1111", false, "public IPv6"},
		{"8.8.8.8", false, "public resolver"},

		// Our own services are not a peer, and a peer field must not become a
		// way to reach things bound to localhost.
		{"127.0.0.1", false, "loopback"},
		{"::1", false, "IPv6 loopback"},
		{"0.0.0.0", false, "unspecified"},

		// A name would resolve after the check and could resolve elsewhere at
		// dial time, which would make the check decorative.
		{"speaker.local", false, "hostname"},
		{"localhost", false, "hostname that looks harmless"},
		{"", false, "empty"},
		{"192.168.1.44:8888", false, "host:port, not a bare IP"},
		{"not an ip", false, "junk"},
	}
	for _, c := range cases {
		if got := isLANPeer(c.host); got != c.want {
			t.Errorf("isLANPeer(%q) = %v, want %v (%s)", c.host, got, c.want, c.why)
		}
	}
}

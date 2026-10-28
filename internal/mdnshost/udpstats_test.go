package mdnshost

import "testing"

func TestWorthDecodingSkipsRepliesAndEmptyQueries(t *testing.T) {
	query := []byte{0, 0, 0x00, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	reply := []byte{0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0}
	empty := []byte{0, 0, 0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if !worthDecoding(query) {
		t.Error("a query with one question must be decoded")
	}
	if worthDecoding(reply) {
		t.Error("a reply must be skipped")
	}
	if worthDecoding(empty) {
		t.Error("a query without questions must be skipped")
	}
	if worthDecoding([]byte{1, 2, 3}) {
		t.Error("a runt datagram must be skipped")
	}
}

func TestParseSNMPUdp(t *testing.T) {
	in := "Ip: Forwarding DefaultTTL\nIp: 2 64\n" +
		"Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors\n" +
		"Udp: 812345 12 75000 4000 74990 0\n"
	got := parseSNMPUdp(in)
	want := map[string]int64{"udpInDatagrams": 812345, "udpNoPorts": 12, "udpInErrors": 75000, "udpRcvbufErrors": 74990}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d", k, got[k], v)
		}
	}
	if _, ok := got["udpOutDatagrams"]; ok {
		t.Error("only the receive side is reported")
	}
}

func TestMDNSSocketDrops(t *testing.T) {
	in := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n" +
		"   1: 00000000:14E9 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1234 2 c0000000 75012\n" +
		"   2: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1235 2 c0000000 3\n" +
		"   3: FB0000E0:14E9 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1236 2 c0000000 74800\n"
	got := mdnsSocketDrops(in)
	if len(got) != 2 || got[0] != 75012 || got[1] != 74800 {
		t.Fatalf("drops = %v, want [75012 74800]", got)
	}
}

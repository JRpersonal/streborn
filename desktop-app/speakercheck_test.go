package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// checkPorts builds the five report ports with every answer silent, then
// applies the overrides.
func checkPorts(over map[int]portAnswer) []portAnswer {
	silent := "no answer (silent, timed out after 2s)"
	out := make([]portAnswer, 0, len(reportProbePorts))
	for _, p := range reportProbePorts {
		a := portAnswer{Port: p.port, Label: p.label, Result: silent}
		if o, ok := over[p.port]; ok {
			a.Result, a.Detail = o.Result, o.Detail
		}
		out = append(out, a)
	}
	return out
}

var homeIface = []localIface{{Name: "WLAN", CIDR: "192.0.2.10/24", Up: true}}

const refused = "connection refused (the speaker IS reachable, nothing is listening on this port)"

func TestClassifySpeakerCheck(t *testing.T) {
	cases := []struct {
		name string
		f    installFacts
		want string
	}{
		{
			name: "agent answers on 8888",
			f: installFacts{Host: "192.0.2.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: true,
				Ports: checkPorts(map[int]portAnswer{8090: {Result: "open", Detail: "HTTP 200 OK"}, 8888: {Result: "open", Detail: "HTTP 200 OK"}})},
			want: checkOK,
		},
		{
			name: "agent answers on 17008 only (BCO chassis)",
			f: installFacts{Host: "192.0.2.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: true,
				Ports: checkPorts(map[int]portAnswer{8888: {Result: refused}, 17008: {Result: "open", Detail: "HTTP 200 OK"}})},
			want: checkOK,
		},
		{
			name: "Bose firmware answers, STR ports refuse",
			f: installFacts{Host: "192.0.2.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: true, PingRan: true, PingAlive: true,
				Ports: checkPorts(map[int]portAnswer{8090: {Result: "open", Detail: "HTTP 200 OK"}, 8888: {Result: refused}, 17008: {Result: refused}})},
			want: checkSTRSilent,
		},
		{
			name: "agent port opens with a non-STR answer, firmware healthy",
			f: installFacts{Host: "192.0.2.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: true,
				Ports: checkPorts(map[int]portAnswer{8090: {Result: "open", Detail: "HTTP 200 OK"}, 8888: {Result: "open", Detail: "HTTP 404 Not Found"}})},
			want: checkSTRSilent,
		},
		{
			name: "ping answers, every port refuses: hung firmware",
			f: installFacts{Host: "192.0.2.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: true, PingRan: true, PingAlive: true,
				Ports: checkPorts(map[int]portAnswer{22: {Result: refused}, 8090: {Result: refused}, 8091: {Result: refused}, 8888: {Result: refused}, 17008: {Result: refused}})},
			want: checkHung,
		},
		{
			name: "8090 opens but never answers HTTP: wedged control stack",
			f: installFacts{Host: "192.0.2.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: true,
				Ports: checkPorts(map[int]portAnswer{8090: {Result: "open", Detail: "opened, but no HTTP answer (timeout)"}})},
			want: checkHung,
		},
		{
			name: "only an ARP entry",
			f: installFacts{Host: "192.0.2.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: true, PingRan: true,
				MACPrefix: "aa:bb:cc:xx:xx:xx", Ports: checkPorts(nil)},
			want: checkHung,
		},
		{
			name: "nothing at all on this PC's network",
			f: installFacts{Host: "192.0.2.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: true, PingRan: true,
				TargetSeen: true, TargetMissingSec: 600, Ports: checkPorts(nil)},
			want: checkOffNetwork,
		},
		{
			name: "nothing at all, ping could not run",
			f:    installFacts{Host: "192.0.2.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: true, Ports: checkPorts(nil)},
			want: checkOffNetwork,
		},
		{
			name: "nothing at all, address in another network",
			f: installFacts{Host: "198.51.100.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: false, PingRan: true,
				Ports: checkPorts(nil)},
			want: checkOtherNetwork,
		},
		{
			name: "other network but routed and answering: the port verdicts decide",
			f: installFacts{Host: "198.51.100.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: false, PingRan: true, PingAlive: true,
				Ports: checkPorts(map[int]portAnswer{8090: {Result: "open", Detail: "HTTP 200 OK"}})},
			want: checkSTRSilent,
		},
		{
			name: "this PC has no address",
			f: installFacts{Host: "192.0.2.20", Ifaces: []localIface{{Name: "WLAN", Up: true}}, PingRan: true,
				Ports: checkPorts(map[int]portAnswer{8090: {Result: "network unreachable (this PC is not on that network at all)"}})},
			want: checkPCOffline,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySpeakerCheck(tc.f); got != tc.want {
				t.Errorf("classifySpeakerCheck = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSpeakerCheckResultCarriesKeyAndReport(t *testing.T) {
	f := installFacts{Host: "198.51.100.20", Ifaces: homeIface, SubnetKnown: true, SameSubnet: false, PingRan: true,
		Ports: checkPorts(nil), TargetSeen: true, TargetMissingSec: 900}
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	r := speakerCheckResult(f, at)
	if r.Verdict != checkOtherNetwork || r.AdviceKey != "speakerCheck.lead.otherNetwork" {
		t.Fatalf("verdict/key = %q/%q", r.Verdict, r.AdviceKey)
	}
	if r.PCNetworks != "192.0.2.0/24 on WLAN" {
		t.Errorf("PCNetworks = %q", r.PCNetworks)
	}
	for _, want := range []string{
		"speaker check 198.51.100.20",
		"other-network (nothing answers, and the address is in a different network than this PC)",
		"no answer for 15 min",
		"this PC is on",
		"port 8090, Bose control API",
		"NO, the speaker address is not in any network this PC is on",
	} {
		if !strings.Contains(r.Report, want) {
			t.Errorf("report misses %q:\n%s", want, r.Report)
		}
	}
	if strings.Contains(r.Report, "network facts\n") {
		t.Errorf("report repeats the failure report's section heading:\n%s", r.Report)
	}
}

func TestEveryVerdictHasKeyAndWords(t *testing.T) {
	for _, v := range []string{checkOK, checkSTRSilent, checkPCOffline, checkHung, checkOtherNetwork, checkOffNetwork} {
		if speakerCheckAdviceKeys[v] == "" || speakerCheckWords[v] == "" {
			t.Errorf("verdict %q lacks an advice key or its words", v)
		}
	}
}

func TestCheckSpeakerRefusesEmptyAndOddHosts(t *testing.T) {
	a := &App{}
	calls := 0
	a.speakerCheck.gather = func(context.Context, string) installFacts { calls++; return installFacts{} }
	for _, h := range []string{"", "  ", "192.0.2.1/24", "http://x"} {
		r := a.CheckSpeaker(h)
		if r.Error == "" || r.Verdict != "" {
			t.Errorf("CheckSpeaker(%q) = %+v, want an error", h, r)
		}
	}
	if calls != 0 {
		t.Errorf("gathered %d times for refused hosts", calls)
	}
}

// A double click, or the support mail right after a list check, must not
// probe the speaker twice.
func TestCheckSpeakerReusesAFreshResult(t *testing.T) {
	a := &App{}
	var calls int32
	clock := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	a.speakerCheck.now = func() time.Time { return clock }
	a.speakerCheck.gather = func(_ context.Context, host string) installFacts {
		atomic.AddInt32(&calls, 1)
		return installFacts{Host: host, Ifaces: homeIface, SubnetKnown: true, SameSubnet: true, PingRan: true, Ports: checkPorts(nil)}
	}
	first := a.CheckSpeaker("192.0.2.20")
	second := a.CheckSpeaker("192.0.2.20")
	if first.Verdict != checkOffNetwork || second.Verdict != checkOffNetwork {
		t.Fatalf("verdicts %q %q", first.Verdict, second.Verdict)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("gathered %d times within the reuse window, want 1", n)
	}
	clock = clock.Add(speakerCheckReuse + time.Second)
	a.CheckSpeaker("192.0.2.20")
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("gathered %d times after the reuse window, want 2", n)
	}
	// Another speaker is its own check.
	a.CheckSpeaker("192.0.2.21")
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Fatalf("gathered %d times for a second speaker, want 3", n)
	}
}

func TestCheckSpeakerJoinsARunningCheck(t *testing.T) {
	a := &App{}
	var calls int32
	release := make(chan struct{})
	started := make(chan struct{})
	a.speakerCheck.gather = func(_ context.Context, host string) installFacts {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-release
		return installFacts{Host: host, Ifaces: homeIface, Ports: checkPorts(map[int]portAnswer{8888: {Result: "open", Detail: "HTTP 200 OK"}})}
	}
	var wg sync.WaitGroup
	results := make([]SpeakerCheck, 3)
	wg.Add(1)
	go func() { defer wg.Done(); results[0] = a.CheckSpeaker("192.0.2.20") }()
	<-started
	for i := 1; i < 3; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = a.CheckSpeaker("192.0.2.20") }(i)
	}
	// Give the joiners time to queue on the running call.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("gathered %d times for three overlapping clicks, want 1", n)
	}
	for i, r := range results {
		if r.Verdict != checkOK {
			t.Errorf("result %d verdict %q", i, r.Verdict)
		}
	}
}

func TestCheckSpeakerSurvivesAPanickingGather(t *testing.T) {
	a := &App{}
	a.speakerCheck.gather = func(context.Context, string) installFacts { panic("boom") }
	r := a.CheckSpeaker("192.0.2.20")
	if r.Error == "" || r.Verdict != "" {
		t.Fatalf("result after a panic = %+v", r)
	}
	// The slot is free again and the failure is not cached.
	a.speakerCheck.gather = func(_ context.Context, host string) installFacts {
		return installFacts{Host: host, Ifaces: homeIface, Ports: checkPorts(map[int]portAnswer{8888: {Result: "open", Detail: "HTTP 200 OK"}})}
	}
	if r := a.CheckSpeaker("192.0.2.20"); r.Verdict != checkOK {
		t.Fatalf("second check verdict %q, want ok", r.Verdict)
	}
}

func TestPortsSummary(t *testing.T) {
	got := portsSummary(checkPorts(map[int]portAnswer{8090: {Result: "open"}, 8888: {Result: refused}, 17008: {Result: "no route to host (this PC has no path to that address)"}}))
	want := "22=silent 8090=open 8091=silent 8888=refused 17008=noroute"
	if got != want {
		t.Errorf("portsSummary = %q, want %q", got, want)
	}
}

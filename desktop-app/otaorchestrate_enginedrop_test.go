package main

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// carloDrop is the engine push failure from #1768, verbatim apart from the
// addresses: the app's own ephemeral port 50734 carries "507", which the old
// space classifier read as a full speaker.
var carloDrop = errors.New(`sidecar upload failed: Post "http://192.0.2.31:17008/api/agent/sidecar": readfrom tcp 192.0.2.182:50734->192.0.2.31:17008: write tcp 192.0.2.182:50734->192.0.2.31:17008: use of closed network connection`)

func TestClassifyEngineFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want engineFailure
	}{
		{"#1768: closed connection, port 50734", carloDrop, engineFailDropped},
		{"forcibly closed", errors.New("write tcp 192.0.2.182:50734->192.0.2.31:17008: wsasend: An existing connection was forcibly closed by the remote host."), engineFailDropped},
		{"windows abort", errors.New("write tcp 192.0.2.182:65070->192.0.2.31:17008: wsasend: An established connection was aborted by the software in your host machine."), engineFailDropped},
		{"wsarecv", errors.New("read tcp 192.0.2.182:50712->192.0.2.31:17008: wsarecv: An existing connection was forcibly closed by the remote host."), engineFailDropped},
		{"connection reset", errors.New("read tcp 192.0.2.182:50744->192.0.2.31:8888: read: connection reset by peer"), engineFailDropped},
		{"broken pipe", errors.New("write tcp 192.0.2.182:50744->192.0.2.31:8888: write: broken pipe"), engineFailDropped},
		{"bare EOF mid-upload", errors.New(`sidecar upload failed: Post "http://192.0.2.31:17008/api/agent/sidecar": EOF`), engineFailDropped},
		{"unexpected EOF", errors.New("unexpected EOF"), engineFailDropped},
		{"typed 507 reply", fmt.Errorf("sidecar upload failed: %w", &nandFullError{status: 507, detail: "insufficient NAND space"}), engineFailNoRoom},
		{"app-side space gate", fmt.Errorf("sidecar: %w", &nandFullError{free: 1, need: 2}), engineFailNoRoom},
		{"507 reply as text", errors.New("status 507: insufficient NAND space: need 16 MB, free 11 MB"), engineFailNoRoom},
		{"507 status without a body", errors.New("status 507: "), engineFailNoRoom},
		{"older agent's 500 naming the space", errors.New("status 500: no space left on device"), engineFailNoRoom},
		{"agent busy", errors.New("status 500: agent busy"), engineFailRetry},
		{"refused", errors.New("dial tcp 192.0.2.31:17008: connectex: No connection could be made because the target machine actively refused it."), engineFailRetry},
		{"507 inside another number is not a status", errors.New("dial tcp 192.0.2.182:15071->192.0.2.31:17008: i/o timeout"), engineFailRetry},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyEngineFailure(c.err); got != c.want {
				t.Errorf("classifyEngineFailure(%q) = %d, want %d", c.err, got, c.want)
			}
		})
	}
}

// The #1768 sequence: after a confirmed agent update the engine push breaks
// off with the closed-connection error. The engine step must try again, and
// must not call the speaker too full for Spotify.
func TestSpeakerUpdateRetriesTheEngineAfterAClosedConnection(t *testing.T) {
	fb := newOtaFakeBox()
	fb.engineReplies = []engineReply{{err: carloDrop}, {res: "ok"}}
	presentAfterSecondEngine(fb)
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.engineCalls) != 2 {
		t.Fatalf("engine sent %d times, want a second attempt after the drop", len(fb.engineCalls))
	}
	if out.Outcome != outcomeDone || out.EngineTooFull || !out.EngineDelivered {
		t.Errorf("outcome %+v, want done with the engine delivered", out)
	}
	if out.Version["goLibrespot"] != "present" {
		t.Errorf("done is reported on the speaker's own word that the engine is there, got %v", out.Version)
	}
}

// A push that keeps breaking off never ends as success while the speaker
// reports the engine missing, and the record that finishes the job later
// stays.
func TestSpeakerUpdateEngineDropsUntilTheWindowEnds(t *testing.T) {
	fb := newOtaFakeBox()
	fb.rebootsTo(60*time.Second, func(time.Time) map[string]string { return newVersion("missing") })
	fb.engineReplies = []engineReply{{err: carloDrop}}
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != outcomePartial || out.Unmet != unmetSpotifyEngine || out.EngineTooFull {
		t.Errorf("outcome %+v, want partial with the engine outstanding and no space verdict", out)
	}
	if len(fb.engineCalls) < 2 {
		t.Errorf("engine sent %d times inside the window, want repeated attempts", len(fb.engineCalls))
	}
	if len(fb.clears) != 0 {
		t.Errorf("intent cleared %d times; it must stay so the next run delivers the engine", len(fb.clears))
	}
}

// The engine-only repair and the restore after an incomplete update share the
// classifier: a dropped push there is no space verdict either.
func TestEngineOnlyDropIsNotTooFull(t *testing.T) {
	ff := newFleetFake()
	b := ff.add("e", "missing")
	b.version = newVersion("missing")
	b.engine = []engineReply{{err: carloDrop}}
	out, err := orchestrateFleet(ff.seams(), fleetPolicy, []FleetTarget{{Host: "e", Port: 1, Name: "E", EngineOnly: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := out.Rows[0]; r.EngineTooFull || r.Outcome == outcomeDone {
		t.Errorf("row %+v: a dropped push is neither success nor a full speaker", r)
	}
}

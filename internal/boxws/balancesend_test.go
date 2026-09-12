// The send path, and the balance write that is the reason it exists.
//
// boxws was a listener only until 2026-09-10, so these are the first tests in
// this package that need a real socket rather than a hand-fed frame: the point
// of the exercise is that the exact bytes Bose's own client sends reach the
// peer, and a handleMessage-style test cannot show that.
//
// The envelope comes from gesellix/Bose-SoundTouch#699, which read it out of
// Stockholm, the web app the speaker itself serves. Pinning it byte for byte is
// deliberate: it was measured off somebody else's firmware, we cannot re-derive
// it from anything in this repo, and a well-meant reformat of the frame is
// exactly the kind of change that would break a write nobody tests on hardware
// every release.

package boxws

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// gabboServer stands in for the speaker's WebSocket. It accepts the gabbo
// subprotocol, then forwards everything the client sends to the returned
// channel so a test can assert on the raw frame.
func gabboServer(t *testing.T) (url string, got chan string) {
	t.Helper()
	got = make(chan string, 8)
	up := websocket.Upgrader{
		Subprotocols:    []string{"gabbo"},
		CheckOrigin:     func(*http.Request) bool { return true },
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.TextMessage {
				select {
				case got <- string(data):
				default:
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), got
}

// runClient starts a client against url and waits until it is connected.
func runClient(t *testing.T, url string) *Client {
	t.Helper()
	c := New(slog.New(slog.NewTextHandler(io.Discard, nil)), url, &recHandler{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for !c.Connected() {
		if time.Now().After(deadline) {
			t.Fatal("client never connected to the test socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return c
}

// The envelope, byte for byte. Stockholm's setBalance sends url="balance" with
// method="POST", mainNode="balanceSet" on the info element, and the value in
// <balance><targetBalance>. Every one of those four is load-bearing on the
// firmware side, and none of them can be re-derived from this repo.
func TestBalanceFrameIsTheEnvelopeBoseOwnClientSends(t *testing.T) {
	want := `<msg><header deviceID="AABBCCDDEEFF" url="balance" method="POST">` +
		`<request requestID="1"><info mainNode="balanceSet" type="new"/></request>` +
		`</header><body><balance><targetBalance>-3</targetBalance></balance></body></msg>`
	if got := BalanceFrame("AABBCCDDEEFF", -3, 1); got != want {
		t.Fatalf("frame drifted from the shape Stockholm sends\n got: %s\nwant: %s", got, want)
	}
}

// A negative value must survive as a negative value: the balance is signed, and
// negative is left.
func TestBalanceFrameKeepsTheSign(t *testing.T) {
	if !strings.Contains(BalanceFrame("AA", -7, 2), "<targetBalance>-7</targetBalance>") {
		t.Fatal("a left-hand balance lost its sign in the frame")
	}
	if !strings.Contains(BalanceFrame("AA", 7, 3), "<targetBalance>7</targetBalance>") {
		t.Fatal("a right-hand balance did not survive the frame")
	}
}

// The write reaches the peer, on the socket, unaltered.
func TestSetBalanceSendsTheFrameOnTheSocket(t *testing.T) {
	url, got := gabboServer(t)
	c := runClient(t, url)

	if err := c.SetBalance("AABBCCDDEEFF", -3); err != nil {
		t.Fatalf("SetBalance: %v", err)
	}
	select {
	case frame := <-got:
		if !strings.Contains(frame, `url="balance"`) || !strings.Contains(frame, `method="POST"`) {
			t.Fatalf("the peer got something that is not a balance write: %s", frame)
		}
		if !strings.Contains(frame, `mainNode="balanceSet"`) {
			t.Fatalf("mainNode is what tells the firmware which node to write; missing: %s", frame)
		}
		if !strings.Contains(frame, "<targetBalance>-3</targetBalance>") {
			t.Fatalf("the value did not arrive: %s", frame)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived at the peer")
	}
}

// Every request gets its own number. Bose's own client counts up, and a
// firmware is entitled to treat a repeated requestID as a duplicate, which
// would make the second nudge of a slider silently do nothing.
func TestEachSendCarriesAFreshRequestID(t *testing.T) {
	url, got := gabboServer(t)
	c := runClient(t, url)

	var ids []string
	for i := 0; i < 3; i++ {
		if err := c.SetBalance("AABBCCDDEEFF", i); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		select {
		case f := <-got:
			ids = append(ids, attrValue(f, "requestID"))
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d never arrived", i)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			t.Fatalf("a frame carried no requestID: %v", ids)
		}
		if seen[id] {
			t.Fatalf("a requestID was reused: %v", ids)
		}
		seen[id] = true
	}
}

// With no live socket the write must fail as "not right now" rather than panic
// on a nil connection or, worse, report success. The client sits without a
// socket every time the box is asleep or rebooting, which is most of the day.
func TestSendWithoutAConnectionRefusesInsteadOfPanicking(t *testing.T) {
	c := newTestClient(&recHandler{}) // never Run, so never connected
	if err := c.SetBalance("AABBCCDDEEFF", 0); err == nil {
		t.Fatal("a write with no socket reported success")
	} else if err != ErrNotConnected {
		t.Fatalf("want ErrNotConnected, got %v", err)
	}
	if err := c.Send("<msg/>"); err != ErrNotConnected {
		t.Fatalf("raw Send: want ErrNotConnected, got %v", err)
	}
}

// An empty deviceID cannot be addressed, and sending the frame anyway would put
// a malformed envelope on the bus. Refuse before the write.
func TestSetBalanceRefusesWithoutADeviceID(t *testing.T) {
	url, got := gabboServer(t)
	c := runClient(t, url)

	if err := c.SetBalance("   ", 0); err == nil {
		t.Fatal("a write with no deviceID was sent")
	}
	select {
	case f := <-got:
		t.Fatalf("something was sent anyway: %s", f)
	case <-time.After(300 * time.Millisecond):
	}
}

// The frame must survive a value that needs escaping. A SoundTouch deviceID is
// hex and never does, so this is about the frame staying well formed if any
// caller ever passes something else.
func TestBalanceFrameEscapesTheDeviceID(t *testing.T) {
	f := BalanceFrame(`a"b&c`, 0, 1)
	if strings.Contains(f, `deviceID="a"b&c"`) {
		t.Fatalf("the attribute was broken by its own value: %s", f)
	}
	if !strings.Contains(f, `deviceID="a&quot;b&amp;c"`) {
		t.Fatalf("escaping missing: %s", f)
	}
}

package boxapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func answer(status int) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("<info/>")), Request: r}, nil
	}
}

func failWith(err error) roundTripFunc {
	return func(*http.Request) (*http.Response, error) { return nil, err }
}

// observe registers a recording observer for host and returns the verdicts.
func observe(t *testing.T, host string) *[]bool {
	t.Helper()
	var got []bool
	ObserveReachability(host, func(ok bool) { got = append(got, ok) })
	t.Cleanup(func() { ObserveReachability("", nil) })
	return &got
}

func clientWith(host string, rt http.RoundTripper) *Client {
	c := New(host)
	c.HTTP = &http.Client{Transport: rt}
	return c
}

// Any HTTP answer is the webserver answering, a firmware error reply included;
// a transport failure is not. A request the caller cancelled says nothing.
func TestClientFeedsReachability(t *testing.T) {
	got := observe(t, "127.0.0.1")
	ctx := context.Background()

	if _, err := clientWith("127.0.0.1", answer(200)).GetRaw(ctx, "/info"); err != nil {
		t.Fatalf("200 read: %v", err)
	}
	var be *BoxError
	if _, err := clientWith("127.0.0.1", answer(500)).GetRaw(ctx, "/info"); !errors.As(err, &be) {
		t.Fatalf("500 read: want *BoxError, got %v", err)
	}
	if _, err := clientWith("127.0.0.1", failWith(context.DeadlineExceeded)).GetRaw(ctx, "/info"); err == nil {
		t.Fatal("timed-out read succeeded")
	}
	if err := clientWith("127.0.0.1", failWith(errors.New("connection refused"))).postXML(ctx, "/key", "<key/>"); err == nil {
		t.Fatal("refused POST succeeded")
	}
	if _, err := clientWith("127.0.0.1", failWith(context.Canceled)).GetRaw(ctx, "/info"); err == nil {
		t.Fatal("cancelled read succeeded")
	}
	want := []bool{true, true, false, false}
	if len(*got) != len(want) {
		t.Fatalf("verdicts = %v, want %v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Fatalf("verdicts = %v, want %v", *got, want)
		}
	}
}

// A request to any other speaker (a group member) is not this speaker's
// webserver and must not count either way.
func TestReachabilityIgnoresOtherHosts(t *testing.T) {
	got := observe(t, "127.0.0.1")
	_, _ = clientWith("192.0.2.1", failWith(errors.New("no route to host"))).GetRaw(context.Background(), "/info")
	if len(*got) != 0 {
		t.Fatalf("a peer read was reported: %v", *got)
	}
}

// The autopair heartbeat talks to :8090 with its own http.Client; the wrapped
// transport reports for it the same way.
func TestObservedTransport(t *testing.T) {
	got := observe(t, "127.0.0.1")
	ok := &http.Client{Transport: ObservedTransport("127.0.0.1", answer(404))}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:8090/info", nil)
	if resp, err := ok.Do(req); err != nil {
		t.Fatalf("404 round trip: %v", err)
	} else {
		resp.Body.Close()
	}
	bad := &http.Client{Transport: ObservedTransport("127.0.0.1", failWith(errors.New("i/o timeout")))}
	req, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1:8090/info", nil)
	if _, err := bad.Do(req); err == nil {
		t.Fatal("failed round trip succeeded")
	}
	if len(*got) != 2 || !(*got)[0] || (*got)[1] {
		t.Fatalf("verdicts = %v, want [true false]", *got)
	}
}

// Without an observer nothing is reported and nothing breaks.
func TestReachabilityWithoutObserver(t *testing.T) {
	ObserveReachability("", nil)
	if _, err := clientWith("127.0.0.1", answer(200)).GetRaw(context.Background(), "/info"); err != nil {
		t.Fatalf("read: %v", err)
	}
}

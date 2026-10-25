package main

import (
	"errors"
	"testing"
)

func TestIsExpectedRSTOnSetupAP(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"linux reset", errors.New("read tcp 192.0.2.100:1->192.0.2.1:80: read: connection reset by peer"), true},
		{"windows reset", errors.New("wsarecv: An existing connection was forcibly closed by the remote host."), true},
		{"windows abort", errors.New(`Post "http://192.0.2.1/goform/aformHandlerConfigureProfileSettings": read tcp 192.0.2.100:58924->192.0.2.1:80: wsarecv: An established connection was aborted by the software in your host machine.`), true},
		{"linux abort", errors.New("read tcp 192.0.2.100:1->192.0.2.1:80: read: software caused connection abort"), true},
		{"eof", errors.New("unexpected EOF"), true},
		{"404", errors.New("goform not present on this model (404)"), false},
		{"refused", errors.New("dial tcp 192.0.2.1:80: connect: connection refused"), false},
	}
	for _, c := range cases {
		if got := isExpectedRSTOnSetupAP(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

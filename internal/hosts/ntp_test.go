package hosts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseNTPServers(t *testing.T) {
	in := "0.bose.pool.ntp.org\n1.bose.pool.ntp.org\n\n# comment\n0.pool.ntp.org # trailing\ntime.nist.gov\n0.pool.ntp.org\n192.0.2.7\nlocalhost\n"
	want := []string{"0.bose.pool.ntp.org", "1.bose.pool.ntp.org", "0.pool.ntp.org", "time.nist.gov"}
	if got := parseNTPServers([]byte(in)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNTPServersMissingFile(t *testing.T) {
	if got := NTPServers(filepath.Join(t.TempDir(), "none")); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestNTPEntriesPinsIPv4AndSkipsFailures(t *testing.T) {
	lookup := func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "a.pool.ntp.org":
			return []string{"2001:db8::1", "192.0.2.10", "192.0.2.11"}, nil
		case "b.pool.ntp.org":
			return nil, errors.New("no such host")
		case "c.pool.ntp.org":
			return []string{"127.0.0.1", "0.0.0.0"}, nil
		case "d.pool.ntp.org":
			return []string{"2001:db8::2"}, nil
		}
		return nil, errors.New("unexpected")
	}
	got := NTPEntries(context.Background(), []string{"a.pool.ntp.org", "b.pool.ntp.org", "c.pool.ntp.org", "d.pool.ntp.org"}, lookup)
	want := []Entry{{IP: "192.0.2.10", Host: "a.pool.ntp.org"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The pinned names land in STR's block next to the redirects, so the
// firmware's own lookups find them in /etc/hosts.
func TestApplyWritesNTPEntries(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(p, []byte("127.0.0.1\tlocalhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := append(DefaultEntries(), Entry{IP: "192.0.2.10", Host: "0.pool.ntp.org"})
	if err := New(p, nil).Apply(entries); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "192.0.2.10\t0.pool.ntp.org") || !strings.Contains(string(b), "streaming.bose.com") {
		t.Fatalf("hosts file missing entries:\n%s", b)
	}
}

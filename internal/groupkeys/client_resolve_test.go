package groupkeys

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestHTTPClientResolvesThroughRoster: a template stores the addresses of the
// day it was saved. After a DHCP renumbering the roster's address for the
// speaker's id wins over the stored one, for the main speaker (where the
// calls go) and for every member (whom the main speaker enrols by address).
func TestHTTPClientResolvesThroughRoster(t *testing.T) {
	var formed formBody
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/box/zone", func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"master":"","members":[]}`))
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&formed)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	portN, err := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	roster := map[string]string{"AAAA": "127.0.0.1", "BBBB": "192.0.2.12"}
	c := NewHTTPClient(HTTPOptions{
		PortHint: func(string) int { return portN },
		PeerIP:   func(id string) string { return roster[id] },
		// The roster knows no id for the stored addresses: a speaker that
		// resolves through the roster never consults this.
		PeerDeviceID: func(string) string { return "" },
	})
	ctx := context.Background()
	// Stored addresses are stale: nobody listens at 192.0.2.99, the member
	// moved from .11 to .12, and CCCC (not in the roster) keeps its own.
	tpl := Template{
		Name:    "Evening",
		Master:  Member{DeviceID: "AAAA", IP: "192.0.2.99"},
		Members: []Member{{DeviceID: "BBBB", IP: "192.0.2.11"}, {DeviceID: "CCCC", IP: "192.0.2.13"}},
	}
	if _, err := c.LiveZone(ctx, tpl.Master); err != nil {
		t.Fatalf("live zone must reach the roster's address: %v", err)
	}
	if err := c.Form(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Fatalf("expected 2 calls on the roster's address, got %d", hits)
	}
	if formed.Master.IP != "127.0.0.1" || formed.Master.DeviceID != "AAAA" {
		t.Fatalf("master must carry the roster's address: %+v", formed.Master)
	}
	if len(formed.Slaves) != 2 || formed.Slaves[0].IP != "192.0.2.12" || formed.Slaves[1].IP != "192.0.2.13" {
		t.Fatalf("members must carry the roster's address, or the stored one when unlisted: %+v", formed.Slaves)
	}
}

// TestHTTPClientRefusesForeignAddress: the roster has no address for the
// template's id, and says another speaker sits at the stored address now. The
// press must fail before a single call goes out, rather than letting that
// speaker lead a group of the wrong members or take its own group apart.
func TestHTTPClientRefusesForeignAddress(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	portN, err := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	c := NewHTTPClient(HTTPOptions{
		PortHint: func(string) int { return portN },
		PeerIP:   func(string) string { return "" },
		PeerDeviceID: func(ip string) string {
			if ip == "127.0.0.1" {
				return "YYYY"
			}
			return ""
		},
	})
	ctx := context.Background()
	master := Member{DeviceID: "AAAA", IP: "127.0.0.1"}
	tpl := Template{Name: "Evening", Master: master, Members: []Member{{DeviceID: "BBBB", IP: "192.0.2.11"}}}
	if _, err := c.LiveZone(ctx, master); err == nil || !strings.Contains(err.Error(), "save the group again") {
		t.Fatalf("live zone must be refused, got %v", err)
	}
	if err := c.Form(ctx, tpl); err == nil {
		t.Fatal("form must be refused")
	}
	if err := c.Dissolve(ctx, master); err == nil {
		t.Fatal("dissolve must be refused")
	}
	if hits != 0 {
		t.Fatalf("no call may reach the speaker at the foreign address, got %d", hits)
	}

	// The same contradiction on a MEMBER stops the form too: the main
	// speaker would enrol whoever answers at that address.
	c2 := NewHTTPClient(HTTPOptions{
		PortHint: func(string) int { return portN },
		PeerDeviceID: func(ip string) string {
			if ip == "192.0.2.11" {
				return "YYYY"
			}
			return ""
		},
	})
	if err := c2.Form(ctx, tpl); err == nil || !strings.Contains(err.Error(), "192.0.2.11") {
		t.Fatalf("form must be refused for the member's foreign address, got %v", err)
	}
	if hits != 0 {
		t.Fatalf("no call may go out for a refused member, got %d", hits)
	}

	// Without a contradiction the stored address stands: the roster simply
	// has no entry, which is the common case for a speaker that never moved.
	c3 := NewHTTPClient(HTTPOptions{
		PortHint:     func(string) int { return portN },
		PeerDeviceID: func(string) string { return "" },
	})
	if err := c3.Dissolve(ctx, master); err != nil || hits != 1 {
		t.Fatalf("stored address must be used when the roster is silent: err=%v hits=%d", err, hits)
	}
}

// TestHTTPClientSelfByIDIgnoresStoredAddress: this speaker is reached over
// loopback whatever address the template stored for it, and the roster's
// verdict on that stale address does not apply to it.
func TestHTTPClientSelfByIDIgnoresStoredAddress(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	portN, err := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	c := NewHTTPClient(HTTPOptions{
		IsSelf:       func(m Member) bool { return strings.EqualFold(m.DeviceID, "SELF") },
		SelfDeviceID: "SELF",
		LocalPort:    portN,
		PeerIP:       func(string) string { return "" },
		PeerDeviceID: func(string) string { return "YYYY" },
	})
	if err := c.Dissolve(context.Background(), Member{DeviceID: "self", IP: "192.0.2.50"}); err != nil || hits != 1 {
		t.Fatalf("self must be reached over loopback: err=%v hits=%d", err, hits)
	}
}

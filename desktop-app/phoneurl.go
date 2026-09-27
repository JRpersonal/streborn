package main

// The address that goes on the QR code for a speaker's phone remote.
//
// It used to be the speaker's IP, and an IP is exactly the thing that changes.
// A router hands out a new lease, the page the phone has on its home screen
// points at nothing, and the person who scanned that code weeks ago has no way
// of knowing why. Reported by Jens for several users on 2026-09-27.
//
// A name survives that, but only a name that is actually true. The FRITZ!Box in
// the reference network serves one per speaker and one of them comes back as
// "B--roPortable.fritz.box", which is the umlaut in the speaker's own name
// mangled twice over: it is served, it looks plausible, and nothing says whether
// it resolves. So no name goes on a QR code until it has been resolved back to
// the speaker's address AND the speaker has answered through it.
//
// Two candidates, in this order:
//
//  1. The name the ROUTER knows, from a reverse lookup. This is ordinary DNS,
//     so every phone on the network can resolve it, which is the whole point.
//  2. The name the SPEAKER answers for itself, str-<id>.local, published by
//     internal/mdnshost. Reliable on iOS, patchy on Android, so it is the
//     fallback rather than the first choice.
//
// The IP is always reported alongside, because a phone with a private-DNS
// setting or on a guest network resolves neither, and there the IP is the only
// thing that works.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PhoneAddress is what the Settings panel needs to draw the QR code and explain
// it.
type PhoneAddress struct {
	// URL is what the QR code should encode: the name when one was proven, the
	// address otherwise.
	URL string `json:"url"`
	// Name is the host name that was proven, empty when none was.
	Name string `json:"name,omitempty"`
	// AddressURL is the plain-IP form, always filled, so the panel can offer it
	// as the fallback and a user on a phone that cannot resolve LAN names has
	// something to type.
	AddressURL string `json:"addressUrl"`
	// Source says which candidate won: "router", "speaker" or "address".
	Source string `json:"source"`
	// Note is one short sentence for the log and the diagnostic bundle saying
	// why a candidate was rejected. Not shown in the UI.
	Note string `json:"note,omitempty"`
}

// phoneNameProbe is the pair of questions asked about a candidate name, injected
// so the decision can be tested without a network.
type phoneNameProbe struct {
	// resolve returns the addresses a name points at.
	resolve func(name string) ([]string, error)
	// identity returns a value that is the same for two requests to the same
	// speaker and different for two different speakers.
	identity func(hostport string) (string, error)
}

// PhoneAddressFor is the bound method the Settings panel calls.
func (a *App) PhoneAddressFor(host string, port int) PhoneAddress {
	if port == 0 {
		port = 8888
	}
	p := phoneNameProbe{
		resolve: func(name string) ([]string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return net.DefaultResolver.LookupHost(ctx, name)
		},
		identity: a.phoneSpeakerIdentity,
	}
	out := pickPhoneAddress(host, port, a.phoneNameCandidates(host, port), p)
	if a.logger != nil && out.Note != "" {
		a.logger.Info("phone remote address", "host", host, "chose", out.Source, "why", out.Note)
	}
	return out
}

// phoneNameCandidates collects the names worth trying, best first.
func (a *App) phoneNameCandidates(host string, port int) []string {
	var out []string
	// The router's name for this address. A FRITZ!Box serves one per DHCP lease;
	// plenty of routers serve none, and then this list is simply shorter.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if names, err := net.DefaultResolver.LookupAddr(ctx, host); err == nil {
		for _, n := range names {
			if n = strings.TrimSuffix(strings.TrimSpace(n), "."); n != "" {
				out = append(out, n)
			}
		}
	}
	// The name the speaker answers for itself. Asked of the speaker rather than
	// derived here: the label comes from internal/mdnshost, which this module
	// cannot import, and a second copy of that derivation is the kind of drift
	// the anonymiser has already paid for.
	if ver, err := a.BoxAgentVersion(host, port); err == nil {
		if n := strings.TrimSuffix(strings.TrimSpace(ver["mdnsName"]), "."); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// pickPhoneAddress is the whole decision, with the network behind two function
// values so the matrix it has to get right can be written down as a test.
func pickPhoneAddress(host string, port int, candidates []string, p phoneNameProbe) PhoneAddress {
	addr := fmt.Sprintf("http://%s/", net.JoinHostPort(host, strconv.Itoa(port)))
	out := PhoneAddress{URL: addr, AddressURL: addr, Source: "address"}
	if host == "" {
		return out
	}
	want, err := p.identity(net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil || want == "" {
		out.Note = "the speaker did not answer on its address, so no name could be checked against it"
		return out
	}
	var notes []string
	for _, name := range candidates {
		if name == "" || name == host {
			continue
		}
		// It must point HERE. A stale lease in the router's table resolves
		// perfectly and answers a different speaker, which would put one
		// speaker's QR code on another's panel.
		ips, rerr := p.resolve(name)
		if rerr != nil {
			notes = append(notes, name+" does not resolve")
			continue
		}
		if !containsHost(ips, host) {
			notes = append(notes, name+" resolves elsewhere")
			continue
		}
		// And the speaker must answer THROUGH it. Resolving is not reaching:
		// this is the step that catches a name the router serves for a lease it
		// no longer owns, and a name mangled on the way out of the router.
		got, gerr := p.identity(net.JoinHostPort(name, strconv.Itoa(port)))
		if gerr != nil {
			notes = append(notes, name+" resolves but the speaker does not answer through it")
			continue
		}
		if got != want {
			notes = append(notes, name+" answers a different speaker")
			continue
		}
		src := "router"
		if strings.HasSuffix(strings.ToLower(name), ".local") {
			src = "speaker"
		}
		return PhoneAddress{
			URL:        fmt.Sprintf("http://%s/", net.JoinHostPort(name, strconv.Itoa(port))),
			Name:       name,
			AddressURL: addr,
			Source:     src,
			Note:       strings.Join(notes, "; "),
		}
	}
	if len(notes) > 0 {
		out.Note = strings.Join(notes, "; ")
	} else {
		out.Note = "no name for this speaker on this network"
	}
	return out
}

// containsHost reports whether ips holds host, comparing as addresses so
// "192.168.178.21" and "192.168.178.021" are not treated as different hosts.
func containsHost(ips []string, host string) bool {
	want := net.ParseIP(host)
	for _, ip := range ips {
		if want != nil {
			if got := net.ParseIP(ip); got != nil && got.Equal(want) {
				return true
			}
			continue
		}
		if strings.EqualFold(ip, host) {
			return true
		}
	}
	return false
}

// phoneSpeakerIdentity asks a speaker who it is, through whatever host:port it
// is given. The agent publishes no device id on this endpoint, so the identity
// is the tuple that is stable for one speaker and differs between two: its own
// name, its model, and its uptime rounded hard enough that two calls a moment
// apart agree while two different speakers almost never do.
func (a *App) phoneSpeakerIdentity(hostport string) (string, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", err
	}
	port := 0
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return "", err
	}
	ver, err := a.boxAgentVersionAt(host, port)
	if err != nil {
		return "", err
	}
	if ver["version"] == "" {
		return "", errors.New("no agent behind this name")
	}
	return ver["friendlyName"] + "|" + ver["model"] + "|" + ver["agentBinarySha256"], nil
}

// boxAgentVersionAt is BoxAgentVersion without the per-record port memo: this
// asks a NAME, and the memo is keyed by address.
func (a *App) boxAgentVersionAt(host string, port int) (map[string]string, error) {
	url := fmt.Sprintf("http://%s/api/agent/version", net.JoinHostPort(host, strconv.Itoa(port)))
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	client := a.httpClient
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second}
	}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}

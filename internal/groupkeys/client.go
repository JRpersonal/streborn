package groupkeys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// HTTPOptions configures the HTTP client that reaches the main speaker's
// agent.
type HTTPOptions struct {
	// IsSelf reports whether m is THIS speaker. Then the calls loop back to
	// this agent over 127.0.0.1:8888, the address the stream proxy already
	// hands the firmware, instead of going out and back over the LAN (where a
	// whitelisted chassis only reaches itself through the :17008 REDIRECT).
	IsSelf func(m Member) bool
	// PortHint returns the agent port a peer answered on last (0 when
	// unknown), from the peer roster. It is tried first; 17008 and 8888
	// follow, the way the desktop app's port fallback works.
	PortHint func(ip string) int
	// PeerIP returns the address the peer roster currently holds for a
	// speaker's deviceID ("" when the roster has no entry). A template stores
	// the address a speaker had when it was saved; a DHCP renumbering moves
	// the speakers around, and addressing the stored address then reaches a
	// different speaker, which would lead a group of the wrong members. The
	// roster follows a speaker by its id, so the id is resolved through it
	// first and the stored address is only the fallback.
	PeerIP func(deviceID string) string
	// PeerDeviceID returns the deviceID the roster knows the speaker at ip by
	// ("" when unknown). When the roster has no address for a template's id,
	// the stored address is used, unless the roster says another speaker sits
	// there now: then the press is refused rather than sent to the wrong box.
	PeerDeviceID func(ip string) string
	// SelfDeviceID is the id this speaker announces (the one IsSelf matches
	// by). When set and IsSelf matches a member by address only, the member's
	// id is checked against the firmware id over loopback: a two-chip chassis
	// named by its firmware id passes, a stale address that became this
	// speaker's is refused.
	SelfDeviceID string
	// LocalPort is this agent's own listen port for the loopback case.
	// Defaults to 8888.
	LocalPort int
}

// Budgets per call. The form's is the widest: handleZoneForm may spend 8 s
// waking the main speaker before the firmware call starts, and the app gives
// it 45 s for the same reason.
const (
	zoneReadTimeout = 8 * time.Second
	formTimeout     = 45 * time.Second
	dissolveTimeout = 15 * time.Second
	playLastTimeout = 20 * time.Second
	infoTimeout     = 3 * time.Second
)

// httpClient is the MasterClient over the agent's REST API.
type httpClient struct {
	opts HTTPOptions
	// selfIDs caches the firmware deviceID per main-speaker address, so a
	// press costs one /info read the first time and none after that.
	mu      sync.Mutex
	selfIDs map[string]string
}

// NewHTTPClient returns the MasterClient that talks to the main speaker's
// agent over HTTP.
func NewHTTPClient(opts HTTPOptions) MasterClient {
	if opts.LocalPort == 0 {
		opts.LocalPort = 8888
	}
	return &httpClient{opts: opts, selfIDs: map[string]string{}}
}

// isSelf reports whether m is this speaker.
func (c *httpClient) isSelf(m Member) bool {
	return c.opts.IsSelf != nil && c.opts.IsSelf(m)
}

// resolve returns m with the address the speaker has NOW: the roster's
// address for its deviceID when the roster knows it, the stored address
// otherwise. It refuses a stored address that belongs to a different speaker
// by now, so a renumbered LAN cannot make the press form or dissolve the
// wrong group: for a LAN address the roster's id for it is the witness, for
// this speaker's own address (matched by IsSelf without an id match) the
// firmware id its own agent reports over loopback is.
func (c *httpClient) resolve(ctx context.Context, m Member) (Member, error) {
	m.DeviceID = strings.TrimSpace(m.DeviceID)
	m.IP = strings.TrimSpace(m.IP)
	if m.DeviceID == "" {
		return m, nil
	}
	if c.opts.PeerIP != nil {
		if ip := strings.TrimSpace(c.opts.PeerIP(m.DeviceID)); ip != "" {
			m.IP = ip
			return m, nil
		}
	}
	if c.isSelf(m) {
		if c.opts.SelfDeviceID != "" && !strings.EqualFold(m.DeviceID, c.opts.SelfDeviceID) {
			// Matched by address only. A two-chip chassis is legitimately
			// named by its firmware id, which differs from the announced one;
			// another speaker's stale address that became ours is not.
			if fw := c.firmwareID(ctx, "127.0.0.1"); fw != "" && !strings.EqualFold(fw, m.DeviceID) {
				return m, fmt.Errorf("the saved address %s is this speaker's now, not %s's (addresses changed?), save the group again", m.IP, m.DeviceID)
			}
		}
		return m, nil
	}
	if m.IP != "" && c.opts.PeerDeviceID != nil {
		if id := strings.TrimSpace(c.opts.PeerDeviceID(m.IP)); id != "" && !strings.EqualFold(id, m.DeviceID) {
			return m, fmt.Errorf("another speaker answers at %s now (addresses changed?), save the group again", m.IP)
		}
	}
	return m, nil
}

// bases lists the base URLs to try for master, in order.
func (c *httpClient) bases(ctx context.Context, master Member) ([]string, error) {
	master, err := c.resolve(ctx, master)
	if err != nil {
		return nil, err
	}
	if c.isSelf(master) {
		return []string{fmt.Sprintf("http://127.0.0.1:%d", c.opts.LocalPort)}, nil
	}
	ip := master.IP
	if ip == "" {
		return nil, errors.New("the main speaker has no address")
	}
	ports := []string{"17008", "8888"}
	if c.opts.PortHint != nil {
		if p := c.opts.PortHint(ip); p > 0 {
			hint := fmt.Sprint(p)
			ports = append([]string{hint}, ports...)
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, "http://"+net.JoinHostPort(ip, p))
	}
	return out, nil
}

// do sends one request to the first base that answers with a non-404 status
// (a stock Bose web server on :8888 answers 404 for every agent path). The
// response body is read in full, capped, and returned with the status.
func (c *httpClient) do(ctx context.Context, master Member, method, path, body string, timeout time.Duration) (int, []byte, error) {
	bases, err := c.bases(ctx, master)
	if err != nil {
		return 0, nil, err
	}
	client := &http.Client{Timeout: timeout}
	var lastErr error
	for _, base := range bases {
		var rdr io.Reader
		if body != "" {
			rdr = bytes.NewReader([]byte(body))
		}
		req, rerr := http.NewRequestWithContext(ctx, method, base+path, rdr)
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, derr := client.Do(req)
		if derr != nil {
			lastErr = derr
			if ctx.Err() != nil {
				break
			}
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("%s: 404", base)
			continue
		}
		return resp.StatusCode, b, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no agent port answered")
	}
	return 0, nil, lastErr
}

// zoneAnswer is the subset of GET /api/box/zone the toggle needs.
type zoneAnswer struct {
	Master  string `json:"master"`
	Members []struct {
		DeviceID string `json:"deviceID"`
		IP       string `json:"ip"`
	} `json:"members"`
}

func (c *httpClient) LiveZone(ctx context.Context, master Member) (LiveZone, error) {
	code, b, err := c.do(ctx, master, http.MethodGet, "/api/box/zone", "", zoneReadTimeout)
	if err != nil {
		return LiveZone{}, err
	}
	if code != http.StatusOK {
		return LiveZone{}, httpErr(code, b)
	}
	var za zoneAnswer
	if err := json.Unmarshal(b, &za); err != nil {
		return LiveZone{}, fmt.Errorf("zone answer: %w", err)
	}
	lz := LiveZone{Master: strings.TrimSpace(za.Master)}
	for _, m := range za.Members {
		lz.Members = append(lz.Members, Member{DeviceID: strings.TrimSpace(m.DeviceID), IP: strings.TrimSpace(m.IP)})
	}
	if lz.Master != "" {
		lz.SelfID = c.selfID(ctx, master)
	}
	return lz, nil
}

// selfID is the deviceID the main speaker's own firmware reports, read once
// per address and cached. "" when it cannot be read; the caller then relies
// on the template's id alone.
func (c *httpClient) selfID(ctx context.Context, master Member) string {
	master, err := c.resolve(ctx, master)
	if err != nil {
		return ""
	}
	host := master.IP
	if c.isSelf(master) {
		host = "127.0.0.1"
	}
	return c.firmwareID(ctx, host)
}

// firmwareID reads the deviceID the firmware at host reports (GET /info),
// once per host: the answer is cached, so a press costs one read the first
// time and none after that. "" when it cannot be read.
func (c *httpClient) firmwareID(ctx context.Context, host string) string {
	if host == "" {
		return ""
	}
	c.mu.Lock()
	id, ok := c.selfIDs[host]
	c.mu.Unlock()
	if ok {
		return id
	}
	ictx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()
	info, err := boxapi.New(host).GetInfo(ictx)
	if err != nil {
		return ""
	}
	id = strings.TrimSpace(info.DeviceID)
	if id != "" {
		c.mu.Lock()
		c.selfIDs[host] = id
		c.mu.Unlock()
	}
	return id
}

// formBody mirrors the desktop app's ZoneSpec: the agent resolves the real
// deviceIDs itself, and mode is native (the mirror switch is gone from the
// app).
type formBody struct {
	Master    wireMember   `json:"master"`
	Slaves    []wireMember `json:"slaves"`
	Name      string       `json:"name"`
	Stereo    bool         `json:"stereo"`
	Mode      string       `json:"mode"`
	Permanent bool         `json:"permanent"`
}

type wireMember struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip"`
}

func (c *httpClient) Form(ctx context.Context, tpl Template) error {
	// The main speaker enrols each member from whatever box answers at the
	// member's address, so the members are resolved to their current
	// addresses the same way the main speaker is, and a member whose stored
	// address now belongs to another speaker stops the form.
	master, err := c.resolve(ctx, tpl.Master)
	if err != nil {
		return err
	}
	body := formBody{
		Master:    wireMember{DeviceID: master.DeviceID, IP: master.IP},
		Name:      tpl.Name,
		Mode:      "native",
		Permanent: tpl.Permanent,
	}
	for _, m := range tpl.Members {
		rm, err := c.resolve(ctx, m)
		if err != nil {
			return err
		}
		body.Slaves = append(body.Slaves, wireMember{DeviceID: rm.DeviceID, IP: rm.IP})
	}
	b, _ := json.Marshal(body)
	code, rb, err := c.do(ctx, tpl.Master, http.MethodPost, "/api/box/zone", string(b), formTimeout)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return httpErr(code, rb)
	}
	return okOrError(rb)
}

func (c *httpClient) Dissolve(ctx context.Context, master Member) error {
	code, rb, err := c.do(ctx, master, http.MethodDelete, "/api/box/zone", "", dissolveTimeout)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return httpErr(code, rb)
	}
	return okOrError(rb)
}

// Idle reads the main speaker's now-playing (GET /api/status proxies the
// firmware's XML) and reports true unless it is playing or buffering. A
// paused speaker counts as idle: the press is meant to end in music.
func (c *httpClient) Idle(ctx context.Context, master Member) (bool, error) {
	code, b, err := c.do(ctx, master, http.MethodGet, "/api/status", "", zoneReadTimeout)
	if err != nil {
		return true, err
	}
	if code != http.StatusOK {
		return true, httpErr(code, b)
	}
	s := string(b)
	busy := strings.Contains(s, "PLAY_STATE") || strings.Contains(s, "BUFFERING_STATE")
	return !busy, nil
}

func (c *httpClient) PlayLast(ctx context.Context, master Member) error {
	code, rb, err := c.do(ctx, master, http.MethodPost, "/api/box/power", `{"on":true}`, playLastTimeout)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return httpErr(code, rb)
	}
	return nil
}

// okOrError turns the zone endpoints' {"ok":false,"error":"..."} answers into
// an error; any other JSON answer is success. A body that is not JSON is an
// error too: an agent that predates an endpoint answers its index page with
// 200, which must not pass as a formed group.
func okOrError(b []byte) error {
	var r struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return errors.New("the main speaker's agent gave an unexpected answer (older STR version?)")
	}
	if r.OK == nil || *r.OK {
		return nil
	}
	if r.Error == "" {
		r.Error = "the speaker reported failure"
	}
	return errors.New(r.Error)
}

func httpErr(code int, b []byte) error {
	msg := strings.TrimSpace(string(b))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if msg == "" {
		return fmt.Errorf("http %d", code)
	}
	return fmt.Errorf("http %d: %s", code, msg)
}

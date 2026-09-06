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

// bases lists the base URLs to try for master, in order.
func (c *httpClient) bases(master Member) ([]string, error) {
	if c.opts.IsSelf != nil && c.opts.IsSelf(master) {
		return []string{fmt.Sprintf("http://127.0.0.1:%d", c.opts.LocalPort)}, nil
	}
	ip := strings.TrimSpace(master.IP)
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
	bases, err := c.bases(master)
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
	ip := strings.TrimSpace(master.IP)
	if ip == "" {
		return ""
	}
	c.mu.Lock()
	id, ok := c.selfIDs[ip]
	c.mu.Unlock()
	if ok {
		return id
	}
	ictx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()
	host := ip
	if c.opts.IsSelf != nil && c.opts.IsSelf(master) {
		host = "127.0.0.1"
	}
	info, err := boxapi.New(host).GetInfo(ictx)
	if err != nil {
		return ""
	}
	id = strings.TrimSpace(info.DeviceID)
	if id != "" {
		c.mu.Lock()
		c.selfIDs[ip] = id
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
	body := formBody{
		Master:    wireMember{DeviceID: tpl.Master.DeviceID, IP: tpl.Master.IP},
		Name:      tpl.Name,
		Mode:      "native",
		Permanent: tpl.Permanent,
	}
	for _, m := range tpl.Members {
		body.Slaves = append(body.Slaves, wireMember{DeviceID: m.DeviceID, IP: m.IP})
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

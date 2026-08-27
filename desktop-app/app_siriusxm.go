package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sxmRESTBase        = "https://player.siriusxm.com/rest/v2/experience/modules/"
	sxmLiveHLSBase     = "https://siriusxm-priprodlive.akamaized.net"
	sxmUserAgent       = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_12_6) AppleWebKit/604.5.6 (KHTML, like Gecko) Version/11.0.3 Safari/604.5.6"
	sxmListenAddr      = "0.0.0.0:8000"
	sxmListenPort      = 8000
	sxmPlaylistPoll    = 1 * time.Second
	sxmIdleStop        = 10 * time.Second
	sxmStartupSegments = 2
)

var siriusHLSKey = mustSXMBase64("0Nsco7MAgxowGvkUT8aYag==")

func mustSXMBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

type siriusXMConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type siriusXMState struct {
	SXMRunning   bool   `json:"sxm_running"`
	RelayRunning bool   `json:"relay_running"`
	SXMScript    string `json:"sxm_script,omitempty"`
	RelayScript  string `json:"relay_script,omitempty"`
	HLSURL       string `json:"hls_url"`
	RelayURL     string `json:"relay_url"`
	Native       bool   `json:"native"`
	Error        string `json:"error,omitempty"`
}

type sxmChannel struct {
	GUID string
	ID   string
	Name string
	Num  string
	Logo string
}

type sxmPlaylistInfo struct {
	URL       string
	ChannelID string
	Artist    string
	Title     string
}

type sxmMediaSegment struct {
	Seq      int64
	URL      string
	Duration float64
}

type sxmMediaPlaylist struct {
	Segments []sxmMediaSegment
	MediaSeq int64
	Target   float64
	EndList  bool
}

type siriusXMClient struct {
	mu       sync.Mutex
	username string
	password string
	client   *http.Client
	channels map[string]sxmChannel
}

type sxmRelayServer struct {
	mu     sync.Mutex
	client *siriusXMClient
	server *http.Server
	ln     net.Listener
	relays map[string]*sxmStationRelay
	logger *slog.Logger
}

func newSXMRelayServer(client *siriusXMClient, logger *slog.Logger) *sxmRelayServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &sxmRelayServer{
		client: client,
		server: nil,
		ln:     nil,
		relays: make(map[string]*sxmStationRelay),
		logger: logger,
	}
}

type sxmStationRelay struct {
	mu           sync.Mutex
	mount        string
	stationID    string
	name         string
	client       *siriusXMClient
	logger       *slog.Logger
	listeners    map[*sxmListener]struct{}
	stop         chan struct{}
	started      bool
	lastListener time.Time
}

type sxmListener struct {
	mu     sync.Mutex
	ch     chan []byte
	closed bool
}

func newSiriusXMClient(username, password string) *siriusXMClient {
	jar, _ := cookiejar.New(nil)
	return &siriusXMClient{
		username: username,
		password: password,
		client: &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
		},
		channels: make(map[string]sxmChannel),
	}
}

func (c *siriusXMClient) request(ctx context.Context, method, endpoint string, params url.Values, body []byte) (*http.Response, error) {
	u, err := url.Parse(sxmRESTBase + endpoint)
	if err != nil {
		return nil, err
	}
	if params != nil {
		u.RawQuery = params.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", sxmUserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.client.Do(req)
}

func (c *siriusXMClient) apiJSON(ctx context.Context, method, endpoint string, params url.Values, payload any, auth bool) (map[string]any, error) {
	if auth && !c.sessionAuthenticated() && !c.authenticate(ctx) {
		return nil, errors.New("SiriusXM authentication failed")
	}
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	resp, err := c.request(ctx, method, endpoint, params, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SiriusXM HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *siriusXMClient) cookies() []*http.Cookie {
	if c.client == nil || c.client.Jar == nil {
		return nil
	}
	u, _ := url.Parse("https://player.siriusxm.com")
	return c.client.Jar.Cookies(u)
}

func (c *siriusXMClient) loggedIn() bool {
	for _, ck := range c.cookies() {
		if ck.Name == "SXMDATA" && ck.Value != "" {
			return true
		}
	}
	return false
}

func (c *siriusXMClient) sessionAuthenticated() bool {
	var alb, js bool
	for _, ck := range c.cookies() {
		switch ck.Name {
		case "AWSALB":
			alb = true
		case "JSESSIONID":
			js = true
		}
	}
	return alb && js
}

func (c *siriusXMClient) login(ctx context.Context) bool {
	payload := map[string]any{
		"moduleList": map[string]any{
			"modules": []any{map[string]any{
				"moduleRequest": map[string]any{
					"resultTemplate": "web",
					"deviceInfo": map[string]any{
						"osVersion": "Mac", "platform": "Web",
						"sxmAppVersion": "3.1802.10011.0",
						"browser":       "Safari", "browserVersion": "11.0.3",
						"appRegion": "US", "deviceModel": "K2WebClient",
						"clientDeviceId": "null", "player": "html5",
						"clientDeviceType": "web",
					},
					"standardAuth": map[string]any{
						"username": c.username, "password": c.password,
					},
				},
			}},
		},
	}
	data, err := c.apiJSON(ctx, http.MethodPost, "modify/authentication", nil, payload, false)
	if err != nil {
		return false
	}
	resp, _ := data["ModuleListResponse"].(map[string]any)
	status, _ := resp["status"].(float64)
	return status == 1 && c.loggedIn()
}

func (c *siriusXMClient) authenticate(ctx context.Context) bool {
	if !c.loggedIn() && !c.login(ctx) {
		return false
	}
	payload := map[string]any{
		"moduleList": map[string]any{
			"modules": []any{map[string]any{
				"moduleRequest": map[string]any{
					"resultTemplate": "web",
					"deviceInfo": map[string]any{
						"osVersion": "Mac", "platform": "Web",
						"clientDeviceType": "web",
						"sxmAppVersion":    "3.1802.10011.0",
						"browser":          "Safari", "browserVersion": "11.0.3",
						"appRegion": "US", "deviceModel": "K2WebClient",
						"player": "html5", "clientDeviceId": "null",
					},
				},
			}},
		},
	}
	data, err := c.apiJSON(ctx, http.MethodPost, "resume?OAtrial=false", nil, payload, false)
	if err != nil {
		return false
	}
	resp, _ := data["ModuleListResponse"].(map[string]any)
	status, _ := resp["status"].(float64)
	return status == 1 && c.sessionAuthenticated()
}

func (c *siriusXMClient) cookie(name string) string {
	for _, ck := range c.cookies() {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

func (c *siriusXMClient) sxmToken() string {
	v := c.cookie("SXMAKTOKEN")
	if i := strings.IndexByte(v, '='); i >= 0 {
		v = v[i+1:]
	}
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return v
}

func (c *siriusXMClient) gupID() string {
	v := c.cookie("SXMDATA")
	if v == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(v); err == nil {
		v = decoded
	}
	var obj map[string]any
	if json.Unmarshal([]byte(v), &obj) != nil {
		return ""
	}
	id, _ := obj["gupId"].(string)
	return id
}

func (c *siriusXMClient) getChannels(ctx context.Context) ([]sxmChannel, error) {
	c.mu.Lock()
	if len(c.channels) > 0 {
		out := make([]sxmChannel, 0, len(c.channels))
		for _, ch := range c.channels {
			out = append(out, ch)
		}
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	payload := map[string]any{
		"moduleList": map[string]any{
			"modules": []any{map[string]any{
				"moduleArea": "Discovery",
				"moduleType": "ChannelListing",
				"moduleRequest": map[string]any{
					"consumeRequests": []any{},
					"resultTemplate":  "responsive",
					"alerts":          []any{},
					"profileInfos":    []any{},
				},
			}},
		},
	}
	data, err := c.apiJSON(ctx, http.MethodPost, "get", nil, payload, true)
	if err != nil {
		return nil, err
	}
	resp, _ := data["ModuleListResponse"].(map[string]any)
	moduleList, _ := resp["moduleList"].(map[string]any)
	modules, _ := moduleList["modules"].([]any)
	if len(modules) == 0 {
		return nil, errors.New("SiriusXM channel listing missing")
	}
	mod, _ := modules[0].(map[string]any)
	moduleResp, _ := mod["moduleResponse"].(map[string]any)
	content, _ := moduleResp["contentData"].(map[string]any)
	listing, _ := content["channelListing"].(map[string]any)
	rawChannels, _ := listing["channels"].([]any)

	out := make([]sxmChannel, 0, len(rawChannels))
	for _, raw := range rawChannels {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ch := sxmChannel{
			GUID: stringValue(obj["channelGuid"]),
			ID:   stringValue(obj["channelId"]),
			Name: stringValue(obj["name"]),
			Num:  stringValue(obj["siriusChannelNumber"]),
		}
		if imgs, ok := obj["images"].(map[string]any); ok {
			if arr, ok := imgs["images"].([]any); ok && len(arr) > 3 {
				if img, ok := arr[3].(map[string]any); ok {
					ch.Logo = stringValue(img["url"])
				}
			}
		}
		if ch.ID != "" {
			out = append(out, ch)
		}
	}

	c.mu.Lock()
	c.channels = make(map[string]sxmChannel, len(out))
	for _, ch := range out {
		c.channels[strings.ToLower(ch.ID)] = ch
	}
	c.mu.Unlock()
	return out, nil
}

func stringValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func (c *siriusXMClient) channel(ctx context.Context, id string) (sxmChannel, error) {
	channels, err := c.getChannels(ctx)
	if err != nil {
		return sxmChannel{}, err
	}
	key := strings.ToLower(strings.TrimSpace(id))
	for _, ch := range channels {
		if strings.ToLower(ch.ID) == key || strings.ToLower(ch.Name) == key || ch.Num == key {
			return ch, nil
		}
	}
	return sxmChannel{}, fmt.Errorf("SiriusXM channel %q not found", id)
}

func (c *siriusXMClient) playlistInfo(ctx context.Context, ch sxmChannel, attempts int) (sxmPlaylistInfo, error) {
	if c.sxmToken() == "" || c.gupID() == "" {
		if !c.authenticate(ctx) {
			return sxmPlaylistInfo{}, errors.New("SiriusXM session unavailable")
		}
	}
	params := url.Values{
		"assetGUID":       {ch.GUID},
		"ccRequestType":   {"AUDIO_VIDEO"},
		"channelId":       {ch.ID},
		"hls_output_mode": {"custom"},
		"marker_mode":     {"all_separate_cue_points"},
		"result-template": {"web"},
		"time":            {strconv.FormatInt(time.Now().UnixMilli(), 10)},
		"timestamp":       {time.Now().UTC().Format(time.RFC3339Nano) + "Z"},
	}
	data, err := c.apiJSON(ctx, http.MethodGet, "tune/now-playing-live", params, nil, true)
	if err != nil {
		return sxmPlaylistInfo{}, err
	}
	resp, _ := data["ModuleListResponse"].(map[string]any)
	messages, _ := resp["messages"].([]any)
	code := 0
	if len(messages) > 0 {
		if msg, ok := messages[0].(map[string]any); ok {
			if n, ok := msg["code"].(float64); ok {
				code = int(n)
			}
		}
	}
	if (code == 201 || code == 208) && attempts > 0 {
		if !c.authenticate(ctx) {
			return sxmPlaylistInfo{}, errors.New("SiriusXM reauthentication failed")
		}
		return c.playlistInfo(ctx, ch, attempts-1)
	}
	if code != 100 {
		return sxmPlaylistInfo{}, fmt.Errorf("SiriusXM playlist request returned message code %d", code)
	}

	moduleList, _ := resp["moduleList"].(map[string]any)
	modules, _ := moduleList["modules"].([]any)
	if len(modules) == 0 {
		return sxmPlaylistInfo{}, errors.New("SiriusXM live channel data missing")
	}
	mod, _ := modules[0].(map[string]any)
	moduleResp, _ := mod["moduleResponse"].(map[string]any)
	live, _ := moduleResp["liveChannelData"].(map[string]any)
	hlsInfos, _ := live["hlsAudioInfos"].([]any)

	var large string
	for _, raw := range hlsInfos {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(obj["size"]) == "LARGE" {
			large = strings.ReplaceAll(stringValue(obj["url"]), "%Live_Primary_HLS%", sxmLiveHLSBase)
			break
		}
	}
	if large == "" {
		return sxmPlaylistInfo{}, errors.New("SiriusXM LARGE HLS playlist unavailable")
	}

	q := url.Values{"token": {c.sxmToken()}, "consumer": {"k2"}, "gupId": {c.gupID()}}
	u, err := url.Parse(large)
	if err != nil {
		return sxmPlaylistInfo{}, err
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return sxmPlaylistInfo{}, err
	}
	req.Header.Set("User-Agent", sxmUserAgent)
	res, err := c.client.Do(req)
	if err != nil {
		return sxmPlaylistInfo{}, err
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	res.Body.Close()

	if res.StatusCode == http.StatusForbidden && attempts > 0 {
		if !c.authenticate(ctx) {
			return sxmPlaylistInfo{}, errors.New("SiriusXM reauthentication failed")
		}
		return c.playlistInfo(ctx, ch, attempts-1)
	}
	if res.StatusCode != http.StatusOK {
		return sxmPlaylistInfo{}, fmt.Errorf("SiriusXM variant playlist HTTP %d", res.StatusCode)
	}

	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".m3u8") {
			ref, _ := url.Parse(line)
			variant := u.ResolveReference(ref)
			return sxmPlaylistInfo{
				URL:       variant.String(),
				ChannelID: ch.ID,
			}, nil
		}
	}
	return sxmPlaylistInfo{}, errors.New("SiriusXM variant URL missing")
}

func (c *siriusXMClient) getPlaylist(ctx context.Context, info sxmPlaylistInfo) (sxmMediaPlaylist, error) {
	u, err := url.Parse(info.URL)
	if err != nil {
		return sxmMediaPlaylist{}, err
	}
	q := url.Values{"token": {c.sxmToken()}, "consumer": {"k2"}, "gupId": {c.gupID()}}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return sxmMediaPlaylist{}, err
	}
	req.Header.Set("User-Agent", sxmUserAgent)
	res, err := c.client.Do(req)
	if err != nil {
		return sxmMediaPlaylist{}, err
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return sxmMediaPlaylist{}, fmt.Errorf("SiriusXM media playlist HTTP %d", res.StatusCode)
	}
	return parseSXMPlaylist(string(body), u), nil
}

func parseSXMPlaylist(body string, base *url.URL) sxmMediaPlaylist {
	pl := sxmMediaPlaylist{Target: 10}
	var seq int64
	var dur float64
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			seq, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")), 10, 64)
			pl.MediaSeq = seq
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			pl.Target, _ = strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")), 64)
		case strings.HasPrefix(line, "#EXT-X-ENDLIST"):
			pl.EndList = true
		case strings.HasPrefix(line, "#EXTINF:"):
			value := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(value, ','); i >= 0 {
				value = value[:i]
			}
			dur, _ = strconv.ParseFloat(strings.TrimSpace(value), 64)
		case line != "" && !strings.HasPrefix(line, "#"):
			ref, _ := url.Parse(line)
			abs := base.ResolveReference(ref)
			pl.Segments = append(pl.Segments, sxmMediaSegment{
				Seq:      seq,
				URL:      abs.String(),
				Duration: dur,
			})
			seq++
			dur = 0
		}
	}
	return pl
}

func decryptSXM(data []byte) ([]byte, error) {
	if len(data) < aes.BlockSize*2 || (len(data)-aes.BlockSize)%aes.BlockSize != 0 {
		return nil, errors.New("invalid encrypted SiriusXM segment length")
	}
	iv := data[:aes.BlockSize]
	ct := data[aes.BlockSize:]
	out := make([]byte, len(ct))
	block, err := aes.NewCipher(siriusHLSKey)
	if err != nil {
		return nil, err
	}
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ct)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) {
		return nil, errors.New("invalid AES padding")
	}
	for _, b := range out[len(out)-pad:] {
		if int(b) != pad {
			return nil, errors.New("invalid AES padding bytes")
		}
	}
	out = out[:len(out)-pad]
	if len(out) >= 3 && string(out[:3]) == "ID3" && len(out) >= 10 {
		size := int(out[6]&0x7f)<<21 | int(out[7]&0x7f)<<14 | int(out[8]&0x7f)<<7 | int(out[9]&0x7f)
		if size+10 <= len(out) {
			out = out[size+10:]
		}
	}
	return out, nil
}

func normalizeSXM(data []byte) ([]byte, error) {
	if len(data) >= 2 && data[0] == 0xFF && (data[1]&0xF6) == 0xF0 {
		return data, nil
	}
	return decryptSXM(data)
}

func fetchSegment(ctx context.Context, c *siriusXMClient, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	q := url.Values{"token": {c.sxmToken()}, "consumer": {"k2"}, "gupId": {c.gupID()}}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", sxmUserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SiriusXM segment HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func (s *sxmRelayServer) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return nil
	}
	ln, err := net.Listen("tcp", sxmListenAddr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	s.ln = ln
	s.server = &http.Server{Handler: mux}
	go func() {
		err := s.server.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("SiriusXM native relay stopped", "err", err)
		}
	}()
	return nil
}

func (s *sxmRelayServer) stop() {
	s.mu.Lock()
	srv := s.server
	ln := s.ln
	s.server = nil
	s.ln = nil
	for _, r := range s.relays {
		r.mu.Lock()
		if r.started {
			close(r.stop)
			r.started = false
		}
		r.mu.Unlock()
	}
	s.relays = make(map[string]*sxmStationRelay)
	s.mu.Unlock()

	if srv != nil {
		_ = srv.Shutdown(context.Background())
	}
	if ln != nil {
		_ = ln.Close()
	}
}

func (s *sxmRelayServer) handle(w http.ResponseWriter, req *http.Request) {
	mount := strings.Trim(req.URL.Path, "/")
	if mount == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ST Reborn SiriusXM native relay\n")
		return
	}
	station, ok := nativeStationByMount(mount)
	if !ok {
		http.NotFound(w, req)
		return
	}

	s.mu.Lock()
	r := s.relays[mount]
	if r == nil {
		r = &sxmStationRelay{
			mount: mount, stationID: station.ID, name: station.Name,
			client: s.client, logger: s.logger,
			listeners:    make(map[*sxmListener]struct{}),
			stop:         make(chan struct{}),
			lastListener: time.Now(),
		}
		s.relays[mount] = r
	}
	s.mu.Unlock()

	l := &sxmListener{ch: make(chan []byte, 48)}
	r.addListener(l)
	defer r.removeListener(l)

	w.Header().Set("Content-Type", "audio/aac")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "close")

	first, ok := l.next(45 * time.Second)
	if !ok {
		http.Error(w, "SiriusXM station did not produce audio", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(first)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	for {
		b, ok := l.next(20 * time.Second)
		if !ok {
			return
		}
		if _, err := w.Write(b); err != nil {
			return
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (r *sxmStationRelay) addListener(l *sxmListener) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners[l] = struct{}{}
	r.lastListener = time.Now()
	if !r.started {
		r.started = true
		r.stop = make(chan struct{})
		go r.run()
	}
}

func (r *sxmStationRelay) removeListener(l *sxmListener) {
	r.mu.Lock()
	delete(r.listeners, l)
	r.lastListener = time.Now()
	r.mu.Unlock()
	l.close()
}

func (l *sxmListener) next(timeout time.Duration) ([]byte, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case b := <-l.ch:
		if b == nil {
			return nil, false
		}
		return b, true
	case <-timer.C:
		return nil, false
	}
}

func (l *sxmListener) close() {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.ch)
	}
	l.mu.Unlock()
}

func (l *sxmListener) send(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	select {
	case l.ch <- b:
	default:
		select {
		case <-l.ch:
		default:
		}
		select {
		case l.ch <- b:
		default:
		}
	}
}

func (r *sxmStationRelay) broadcast(b []byte) {
	r.mu.Lock()
	listeners := make([]*sxmListener, 0, len(r.listeners))
	for l := range r.listeners {
		listeners = append(listeners, l)
	}
	r.mu.Unlock()
	for _, l := range listeners {
		l.send(b)
	}
}

func (r *sxmStationRelay) run() {
	defer func() {
		r.mu.Lock()
		r.started = false
		r.mu.Unlock()
	}()

	ctx := context.Background()
	seen := make(map[int64]struct{})
	nextSeq := int64(-1)
	var info sxmPlaylistInfo
	buffer := make([][]byte, 0, sxmStartupSegments)

	// Authenticate and obtain a fresh media-playlist URL when the station
	// worker is first requested.
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		ch, err := r.client.channel(ctx, r.stationID)
		if err == nil {
			info, err = r.client.playlistInfo(ctx, ch, 3)
		}
		if err == nil {
			break
		}
		r.logger.Warn("SiriusXM station startup failed", "station", r.name, "err", err)
		if !sleepSXM(r.stop, 3*time.Second) {
			return
		}
	}

	for {
		select {
		case <-r.stop:
			return
		default:
		}

		r.mu.Lock()
		active := len(r.listeners) > 0
		lastListener := r.lastListener
		r.mu.Unlock()
		if !active && time.Since(lastListener) > sxmIdleStop {
			return
		}

		pl, err := r.client.getPlaylist(ctx, info)
		if err != nil {
			r.logger.Warn("SiriusXM playlist refresh failed", "station", r.name, "err", err)
			// Rebuild the authenticated playlist URL. This also handles 403
			// session expiry without requiring the user to press Play again.
			ch, chErr := r.client.channel(ctx, r.stationID)
			if chErr == nil {
				info, chErr = r.client.playlistInfo(ctx, ch, 3)
			}
			if chErr != nil {
				if !sleepSXM(r.stop, 2*time.Second) {
					return
				}
				continue
			}
			time.Sleep(250 * time.Millisecond)
			continue
		}

		if len(pl.Segments) == 0 {
			if !sleepSXM(r.stop, sxmPlaylistPoll) {
				return
			}
			continue
		}

		if nextSeq < 0 {
			latest := pl.Segments[len(pl.Segments)-1].Seq
			nextSeq = latest - int64(sxmStartupSegments) + 1
			if nextSeq < pl.Segments[0].Seq {
				nextSeq = pl.Segments[0].Seq
			}
		}
		if nextSeq < pl.Segments[0].Seq {
			nextSeq = pl.Segments[0].Seq
			seen = make(map[int64]struct{})
			buffer = buffer[:0]
		}

		progressed := false
		for _, seg := range pl.Segments {
			if seg.Seq < nextSeq {
				continue
			}
			if _, ok := seen[seg.Seq]; ok {
				continue
			}

			data, err := fetchSegment(ctx, r.client, seg.URL)
			if err != nil {
				r.logger.Warn("SiriusXM segment fetch failed", "station", r.name, "seq", seg.Seq, "err", err)
				// Move forward; one bad segment must not kill the station.
				seen[seg.Seq] = struct{}{}
				nextSeq = seg.Seq + 1
				progressed = true
				continue
			}

			data, err = normalizeSXM(data)
			if err != nil {
				r.logger.Warn("SiriusXM segment decrypt/decode failed", "station", r.name, "seq", seg.Seq, "err", err)
				seen[seg.Seq] = struct{}{}
				nextSeq = seg.Seq + 1
				progressed = true
				continue
			}

			seen[seg.Seq] = struct{}{}
			nextSeq = seg.Seq + 1
			progressed = true

			// Prebuffer two HLS segments before exposing audio to a new listener.
			buffer = append(buffer, data)
			if len(buffer) < sxmStartupSegments {
				continue
			}

			for _, chunk := range buffer {
				r.broadcast(chunk)
			}
			buffer = buffer[:0]

			r.mu.Lock()
			stillActive := len(r.listeners) > 0
			r.mu.Unlock()
			if !stillActive {
				return
			}
		}

		if len(seen) > 300 {
			keep := make(map[int64]struct{})
			for seq := range seen {
				if seq >= nextSeq-100 {
					keep[seq] = struct{}{}
				}
			}
			seen = keep
		}

		if !progressed {
			wait := time.Duration(pl.Target * float64(time.Second) / 2)
			if wait < 500*time.Millisecond {
				wait = 500 * time.Millisecond
			}
			if wait > 5*time.Second {
				wait = 5 * time.Second
			}
			if !sleepSXM(r.stop, wait) {
				return
			}
		}
	}
}

func sleepSXM(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

func nativeStationByMount(mount string) (nativeStation, bool) {
	for _, s := range nativeStationList {
		if s.Mount == mount {
			return s, true
		}
	}
	return nativeStation{}, false
}

type nativeStation struct {
	ID    string
	Mount string
	Name  string
}

var nativeStationList = []nativeStation{
	{ID: "siriushits1", Mount: "SiriusXMHits1", Name: "SiriusXM Hits 1"},
	{ID: "9663", Mount: "UnwellMusic", Name: "Unwell Music"},
	{ID: "9614", Mount: "LifewithJohnMayer", Name: "Life with John Mayer"},
	{ID: "thepulse", Mount: "ThePulse", Name: "The Pulse"},
	{ID: "9450", Mount: "PopRocks", Name: "PopRocks"},
	{ID: "totally70s", Mount: "70son7", Name: "70s on 7"},
	{ID: "big80s", Mount: "80son8", Name: "80s on 8"},
	{ID: "8206", Mount: "90son9", Name: "90s on 9"},
	{ID: "8208", Mount: "Pop2K", Name: "Pop2K"},
	{ID: "9556", Mount: "The10sSpot", Name: "The 10s Spot"},
	{ID: "9608", Mount: "TheKellyClarksonConnection", Name: "The Kelly Clarkson Connection"},
	{ID: "9406", Mount: "PitbullsGlobalization", Name: "Pitbull's Globalization"},
	{ID: "thebridge", Mount: "TheBridge", Name: "The Bridge"},
	{ID: "9420", Mount: "YachtRockRadio", Name: "Yacht Rock Radio"},
	{ID: "starlite", Mount: "TheBlend", Name: "The Blend"},
	{ID: "coffeehouse", Mount: "TheCoffeeHouse", Name: "The Coffee House"},
	{ID: "9446", Mount: "TheBeatlesChannel", Name: "The Beatles Channel"},
	{ID: "9523", Mount: "BobMarleysTuffGong", Name: "Bob Marley's Tuff Gong"},
	{ID: "estreetradio", Mount: "EStreetRadio", Name: "E Street Radio"},
	{ID: "undergroundgarage", Mount: "UndergroundGarage", Name: "Underground Garage"},
	{ID: "8370", Mount: "PearlJamRadio", Name: "Pearl Jam Radio"},
	{ID: "gratefuldead", Mount: "GratefulDead", Name: "Grateful Dead"},
	{ID: "radiomargaritaville", Mount: "RadioMargaritaville", Name: "Radio Margaritaville"},
	{ID: "classicrewind", Mount: "ClassicRewind", Name: "Classic Rewind"},
	{ID: "classicvinyl", Mount: "ClassicVinyl", Name: "Classic Vinyl"},
	{ID: "9611", Mount: "Alt2K", Name: "Alt2K"},
	{ID: "thespectrum", Mount: "TheSpectrum", Name: "The Spectrum"},
	{ID: "9139", Mount: "PhishRadio", Name: "Phish Radio"},
	{ID: "9506", Mount: "DaveMatthewsBandRadio", Name: "Dave Matthews Band Radio"},
	{ID: "9407", Mount: "TomPettyRadio", Name: "Tom Petty Radio"},
	{ID: "9507", Mount: "U2XRadio", Name: "U2 X-Radio"},
	{ID: "firstwave", Mount: "1stWave", Name: "1st Wave"},
	{ID: "90salternative", Mount: "Lithium", Name: "Lithium"},
	{ID: "leftofcenter", Mount: "SiriusXMU", Name: "SiriusXMU"},
	{ID: "altnation", Mount: "AltNation", Name: "Alt Nation"},
	{ID: "octane", Mount: "Octane", Name: "Octane"},
	{ID: "buzzsaw", Mount: "OzzysBoneyard", Name: "Ozzy's Boneyard"},
	{ID: "hairnation", Mount: "HairNation", Name: "Hair Nation"},
	{ID: "hardattack", Mount: "LiquidMetal", Name: "Liquid Metal"},
	{ID: "9413", Mount: "SiriusXMTurbo", Name: "SiriusXM Turbo"},
	{ID: "9669", Mount: "MaximumMetallica", Name: "Maximum Metallica"},
	{ID: "9471", Mount: "RockTheBellsRadio", Name: "Rock The Bells Radio"},
	{ID: "hiphopnation", Mount: "HipHopNation", Name: "Hip-Hop Nation"},
	{ID: "shade45", Mount: "Shade45", Name: "Shade 45"},
	{ID: "hotjamz", Mount: "TheHeat", Name: "The Heat"},
	{ID: "heartandsoul", Mount: "HeartAndSoul", Name: "Heart & Soul"},
	{ID: "9609", Mount: "TheFlow", Name: "The Flow"},
	{ID: "9610", Mount: "Flex2K", Name: "Flex2K"},
	{ID: "9339", Mount: "SiriusXMFLY", Name: "SiriusXM FLY"},
	{ID: "8228", Mount: "TheGroove", Name: "The Groove"},
	{ID: "thebeat", Mount: "BPM", Name: "BPM"},
	{ID: "9472", Mount: "DiplosRevolution", Name: "Diplo's Revolution"},
	{ID: "9145", Mount: "Studio54Radio", Name: "Studio 54 Radio"},
	{ID: "chill", Mount: "SiriusXMChill", Name: "SiriusXM Chill"},
	{ID: "newcountry", Mount: "TheHighway", Name: "The Highway"},
	{ID: "9340", Mount: "Y2Kountry", Name: "Y2Kountry"},
	{ID: "primecountry", Mount: "PrimeCountry", Name: "Prime Country"},
	{ID: "9418", Mount: "NoShoesRadio", Name: "No Shoes Radio"},
	{ID: "9599", Mount: "CarriesCountry", Name: "Carrie's Country"},
	{ID: "theroadhouse", Mount: "WilliesRoadhouse", Name: "Willie's Roadhouse"},
	{ID: "outlawcountry", Mount: "OutlawCountry", Name: "Outlaw Country"},
	{ID: "9607", Mount: "ChrisStapletonRadio", Name: "Chris Stapleton Radio"},
	{ID: "9684", Mount: "MorganWallenRadio", Name: "Morgan Wallen Radio"},
	{ID: "bluegrass", Mount: "BluegrassJunction", Name: "Bluegrass Junction"},
	{ID: "symphonyhall", Mount: "SymphonyHall", Name: "Symphony Hall"},
	{ID: "purejazz", Mount: "RealJazz", Name: "Real Jazz"},
	{ID: "jazzcafe", Mount: "Watercolors", Name: "Watercolors"},
	{ID: "broadwaysbest", Mount: "OnBroadway", Name: "On Broadway"},
	{ID: "siriuslysinatra", Mount: "SiriuslySinatra", Name: "Siriusly Sinatra"},
	{ID: "8205", Mount: "40sJunction", Name: "40s Junction"},
	{ID: "siriusgold", Mount: "50sGold", Name: "50s Gold"},
	{ID: "60svibrations", Mount: "60sGold", Name: "60s Gold"},
	{ID: "soultown", Mount: "SmokeysSoulTown", Name: "Smokey's Soul Town"},
	{ID: "siriusblues", Mount: "BBKingsBluesville", Name: "BB King's Bluesville"},
	{ID: "elvisradio", Mount: "ElvisRadio", Name: "Elvis Radio"},
	{ID: "praise", Mount: "KirkFranklinsPraise", Name: "Kirk Franklin's Praise"},
	{ID: "spirit", Mount: "TheMessage", Name: "The Message"},
	{ID: "9675", Mount: "MessageWorship", Name: "Message Worship"},
	{ID: "9494", Mount: "NetflixIsAJokeRadio", Name: "Netflix Is A Joke Radio"},
	{ID: "9408", Mount: "ComedyGreats", Name: "Comedy Greats"},
	{ID: "9356", Mount: "ComedyCentralRadio", Name: "Comedy Central Radio"},
	{ID: "9469", Mount: "KevinHartsLOLRadio", Name: "Kevin Hart's LOL Radio"},
	{ID: "bluecollarcomedy", Mount: "ComedyRoundup", Name: "Comedy Roundup"},
	{ID: "laughbreak", Mount: "PureComedy", Name: "Pure Comedy"},
	{ID: "rawdog", Mount: "SebastianManiscalcoCmdy", Name: "SebastianManiscalco Cmdy"},
	{ID: "howardstern100", Mount: "Howard100", Name: "Howard 100"},
	{ID: "howardstern101", Mount: "Howard101", Name: "Howard 101"},
	{ID: "9409", Mount: "RadioAndy", Name: "Radio Andy"},
	{ID: "8184", Mount: "FactionTalk", Name: "Faction Talk"},
	{ID: "9580", Mount: "ConanOBrienRadio", Name: "Conan O'Brien Radio"},
	{ID: "9674", Mount: "RoadTripRadio", Name: "Road Trip Radio"},
	{ID: "9638", Mount: "Dateline247", Name: "Dateline 24/7"},
	{ID: "9390", Mount: "TODAYShowRadio", Name: "TODAY Show Radio"},
	{ID: "siriusstars", Mount: "Stars", Name: "Stars"},
	{ID: "doctorradio", Mount: "DoctorRadio", Name: "Doctor Radio"},
	{ID: "9673", Mount: "TheMegynKellyChannel", Name: "The Megyn Kelly Channel"},
	{ID: "cnbc", Mount: "CNBC", Name: "CNBC"},
	{ID: "9369", Mount: "FOXBusiness", Name: "FOX Business"},
	{ID: "foxnewschannel", Mount: "FOXNewsChannel", Name: "FOX News Channel"},
	{ID: "9410", Mount: "FOXNewsHeadlines247", Name: "FOX News Headlines 24/7"},
	{ID: "cnn", Mount: "CNN", Name: "CNN"},
	{ID: "8367", Mount: "MSNOW", Name: "MS NOW"},
	{ID: "8239", Mount: "PRXRemix", Name: "PRX Remix"},
	{ID: "bbcworld", Mount: "BBCWorldService", Name: "BBC World Service"},
	{ID: "bloombergradio", Mount: "BloombergRadio", Name: "Bloomberg Radio"},
	{ID: "nprnow", Mount: "NPRNow", Name: "NPR Now"},
	{ID: "9449", Mount: "Triumph", Name: "Triumph"},
	{ID: "indietalk", Mount: "POTUSPolitics", Name: "POTUS Politics"},
	{ID: "siriuspatriot", Mount: "SiriusXMPatriot", Name: "SiriusXM Patriot"},
	{ID: "8238", Mount: "SiriusXMUrbanView", Name: "SiriusXM Urban View"},
	{ID: "siriusleft", Mount: "SiriusXMProgress", Name: "SiriusXM Progress"},
	{ID: "9392", Mount: "JoelOsteenRadio", Name: "Joel Osteen Radio"},
	{ID: "thecatholicchannel", Mount: "TheCatholicChannel", Name: "The Catholic Channel"},
	{ID: "ewtnglobal", Mount: "EWTNRadio", Name: "EWTN Radio"},
	{ID: "8307", Mount: "FamilyTalk", Name: "Family Talk"},
	{ID: "9359", Mount: "BusinessRadio", Name: "Business Radio"},
	{ID: "9530", Mount: "DisneyHits", Name: "Disney Hits"},
	{ID: "8216", Mount: "KidsPlace", Name: "Kids Place"},
	{ID: "9366", Mount: "KIDZBOPRadio", Name: "KIDZ BOP Radio"},
	{ID: "9600", Mount: "CoComelonAndFriends", Name: "CoComelon & Friends"},
	{ID: "9133", Mount: "HolyCultureRadio", Name: "Holy Culture Radio"},
	{ID: "9129", Mount: "HURVoices", Name: "HUR Voices"},
	{ID: "9130", Mount: "HBCU", Name: "HBCU"},
	{ID: "9131", Mount: "BYUradio", Name: "BYUradio"},
	{ID: "9132", Mount: "KoreaToday", Name: "Korea Today"},
	{ID: "9411", Mount: "SLAMRadio", Name: "SLAM Radio"},
	{ID: "roaddogtrucking", Mount: "RoadDogTrucking", Name: "Road Dog Trucking"},
	{ID: "9367", Mount: "RURALRadio", Name: "RURAL Radio"},
	{ID: "radioclassics", Mount: "RadioClassics", Name: "Radio Classics"},
	{ID: "8215", Mount: "Escape", Name: "Escape"},
	{ID: "8229", Mount: "BillGaithersEnlighten", Name: "Bill Gaither's enLighten"},
	{ID: "9593", Mount: "HitsUno", Name: "Hits Uno"},
	{ID: "rumbon", Mount: "Caliente", Name: "Caliente"},
	{ID: "9186", Mount: "Aguila", Name: "\u00c1guila"},
	{ID: "9135", Mount: "EnVivo", Name: "En Vivo"},
	{ID: "9134", Mount: "LatinVault", Name: "Latin Vault"},
	{ID: "9582", Mount: "AttitudeFranco", Name: "Attitude Franco"},
	{ID: "9583", Mount: "MixtapeNorth", Name: "Mixtape: North"},
	{ID: "9358", Mount: "TheIndigiverse", Name: "The Indigiverse"},
	{ID: "9584", Mount: "RacinesMusicales", Name: "Racines Musicales"},
	{ID: "9172", Mount: "CanadaTalks", Name: "Canada Talks"},
	{ID: "8259", Mount: "SiriusXMComedyClub", Name: "SiriusXM Comedy Club"},
	{ID: "cbcradioone", Mount: "CBCRadioOne", Name: "CBC Radio One"},
	{ID: "premiereplus", Mount: "ICIPremiere", Name: "ICI Premi\u00e8re"},
	{ID: "9585", Mount: "TopOfTheCountryRadio", Name: "Top of the Country Radio"},
	{ID: "8244", Mount: "TheVerge", Name: "The Verge"},
	{ID: "8246", Mount: "InfluenceFranco", Name: "Influence Franco"},
	{ID: "9353", Mount: "SiriusXM300", Name: "SiriusXM 300"},
	{ID: "9415", Mount: "RoadTripRadio301", Name: "Road Trip Radio"},
	{ID: "9543", Mount: "AndyCohensKikiLounge", Name: "Andy Cohen's Kiki Lounge"},
	{ID: "9557", Mount: "Mosaic", Name: "Mosaic"},
	{ID: "thevault", Mount: "DeepTracks", Name: "Deep Tracks"},
	{ID: "jamon", Mount: "JamOn309", Name: "Jam On 309"},
	{ID: "9547", Mount: "BonJoviRadio", Name: "Bon Jovi Radio"},
	{ID: "faction", Mount: "GreenDaysIdiotNation", Name: "Green Day's Idiot Nation"},
	{ID: "9570", Mount: "RedHotChiliPeppers", Name: "Red Hot Chili Peppers"},
	{ID: "9364", Mount: "SiriusXMSilk330", Name: "SiriusXM Silk 330"},
	{ID: "reggaerhythms", Mount: "ShaggyBoombasticRadio", Name: "Shaggy Boombastic Radio"},
	{ID: "9623", Mount: "RadioMonaco", Name: "Radio Monaco"},
	{ID: "9365", Mount: "Utopia", Name: "Utopia"},
	{ID: "9475", Mount: "BakersfieldBeat", Name: "Bakersfield Beat"},
	{ID: "9178", Mount: "RedWhiteAndBooze", Name: "Red White & Booze"},
	{ID: "9468", Mount: "NorthAmericana", Name: "North Americana"},
	{ID: "9414", Mount: "GrownFolkJAMZ", Name: "Grown Folk JAMZ"},
	{ID: "8237", Mount: "CSPANRadio", Name: "C-SPAN Radio"},
	{ID: "9479", Mount: "TheBillyGrahamChannel", Name: "The Billy Graham Channel"},
	{ID: "9481", Mount: "LimitedEdition1", Name: "Limited Edition 1"},
	{ID: "9545", Mount: "LimitedEdition2", Name: "Limited Edition 2"},
	{ID: "9546", Mount: "LimitedEdition3", Name: "Limited Edition 3"},
	{ID: "9398", Mount: "LimitedEdition4", Name: "Limited Edition 4"},
	{ID: "9399", Mount: "LimitedEdition5", Name: "Limited Edition 5"},
	{ID: "9400", Mount: "LimitedEdition6", Name: "Limited Edition 6"},
	{ID: "9401", Mount: "LimitedEdition7", Name: "Limited Edition 7"},
	{ID: "9402", Mount: "LimitedEdition8", Name: "Limited Edition 8"},
	{ID: "9403", Mount: "LimitedEdition9", Name: "Limited Edition 9"},
	{ID: "9548", Mount: "LittleMissTwainRadio", Name: "Little Miss Twain Radio"},
	{ID: "9549", Mount: "LimitedEdition11", Name: "Limited Edition 11"},
	{ID: "9482", Mount: "LimitedEdition12", Name: "Limited Edition 12"},
	{ID: "9572", Mount: "PopTop500", Name: "Pop Top 500"},
	{ID: "9573", Mount: "80son8Top500", Name: "80s on 8 Top 500"},
	{ID: "9574", Mount: "90son9Top500", Name: "90s on 9 Top 500"},
	{ID: "9575", Mount: "ClassicRockTop1000", Name: "Classic Rock Top 1000"},
	{ID: "9577", Mount: "HipHopChronicles", Name: "Hip-Hop Chronicles"},
	{ID: "9576", Mount: "CountryTop1000", Name: "Country Top 1000"},
	{ID: "9571", Mount: "BillboardTop500", Name: "Billboard Top 500"},
	{ID: "9342", Mount: "HolidayTraditions", Name: "Holiday Traditions"},
	{ID: "9634", Mount: "DisneyJrRadio", Name: "Disney Jr. Radio"},
	{ID: "9492", Mount: "PandoraNow", Name: "Pandora Now"},
	{ID: "9502", Mount: "SiriusXMKPop", Name: "SiriusXM K-Pop"},
	{ID: "siriuslove", Mount: "SiriusXMLove", Name: "SiriusXM Love"},
	{ID: "9661", Mount: "SiriusXO", Name: "SiriusXO"},
	{ID: "8207", Mount: "TheLoft", Name: "The Loft"},
	{ID: "9352", Mount: "PettysBuriedTreasure", Name: "Petty's Buried Treasure"},
	{ID: "9175", Mount: "RockBar", Name: "RockBar"},
	{ID: "9375", Mount: "ClassicRockParty", Name: "Classic Rock Party"},
	{ID: "9397", Mount: "SwaysUniverse", Name: "Sway's Universe"},
	{ID: "9558", Mount: "SteviesCoolestSongs", Name: "Stevie's Coolest Songs"},
	{ID: "9541", Mount: "RTBMixdown", Name: "RTB - Mixdown"},
	{ID: "9564", Mount: "SoundCloudRadio", Name: "SoundCloud Radio"},
	{ID: "9526", Mount: "SteveAokisRemixRadio", Name: "Steve Aoki's Remix Radio"},
	{ID: "9527", Mount: "AStateOfArmin", Name: "A State of Armin"},
	{ID: "9671", Mount: "ExpertsOnlyRadio", Name: "Experts Only Radio"},
	{ID: "9665", Mount: "OneWorldRadio", Name: "One World Radio"},
	{ID: "9630", Mount: "SaviorSundayDaily", Name: "Savior Sunday Daily"},
	{ID: "9590", Mount: "OutsidersRadio", Name: "Outsiders Radio"},
	{ID: "8227", Mount: "TheVillage", Name: "The Village"},
	{ID: "metropolitanopera", Mount: "MetOperaRadio", Name: "Met Opera Radio"},
	{ID: "siriuspops", Mount: "SiriusXMPops", Name: "SiriusXM Pops"},
	{ID: "spa73", Mount: "Spa", Name: "Spa"},
	{ID: "9501", Mount: "TheTragicallyHipRadio", Name: "The Tragically Hip Radio"},
	{ID: "9513", Mount: "Iceberg", Name: "Iceberg"},
	{ID: "energie2", Mount: "LesTubesFranco", Name: "Les Tubes Franco"},
	{ID: "9529", Mount: "ChuchosCubaBeyond", Name: "Chucho's Cuba & Beyond"},
	{ID: "9672", Mount: "CeliaCruzAZUCAR", Name: "Celia Cruz AZ\u00daCAR!"},
	{ID: "9188", Mount: "Caricia", Name: "Caricia"},
	{ID: "8225", Mount: "Viva", Name: "Viva"},
	{ID: "9187", Mount: "Latidos", Name: "Latidos"},
	{ID: "9185", Mount: "FlowNacion", Name: "Flow Naci\u00f3n"},
	{ID: "9189", Mount: "Luna", Name: "Luna"},
	{ID: "9190", Mount: "Rumbon", Name: "Rumb\u00f3n"},
	{ID: "9191", Mount: "LaKueva", Name: "La Kueva"},
	{ID: "9667", Mount: "SiriusXMDhamaka", Name: "SiriusXM Dhamaka"},
	{ID: "9597", Mount: "SiriusXMAppOriginals", Name: "SiriusXM App Originals"},
	{ID: "9680", Mount: "ComedyInFull", Name: "Comedy in Full"},
	{ID: "9662", Mount: "UnwellOnAir", Name: "Unwell On Air"},
	{ID: "9636", Mount: "SmartLessRadio", Name: "SmartLess Radio"},
	{ID: "9601", Mount: "TheJeffLewisChannel", Name: "The Jeff Lewis Channel"},
	{ID: "9405", Mount: "EntertainmentNow", Name: "Entertainment Now"},
	{ID: "9565", Mount: "FreakonomicsRadio", Name: "Freakonomics Radio"},
	{ID: "9443", Mount: "RamseyNetwork", Name: "Ramsey Network"},
	{ID: "9581", Mount: "LawCrime", Name: "Law&Crime"},
	{ID: "9685", Mount: "2020TrueCrime", Name: "20/20 True Crime"},
	{ID: "9686", Mount: "ABCNewsLive", Name: "ABC News Live"},
	{ID: "9639", Mount: "NBCNewsNOW", Name: "NBC News NOW"},
	{ID: "9637", Mount: "FOXWeather", Name: "FOX Weather"},
}

var nativeSiriusMu sync.Mutex
var nativeSiriusClient *siriusXMClient
var nativeSiriusServer *sxmRelayServer

func siriusXMConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "ST Reborn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "siriusxm.json"), nil
}

func readSiriusXMConfig() (siriusXMConfig, error) {
	p, err := siriusXMConfigPath()
	if err != nil {
		return siriusXMConfig{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return siriusXMConfig{}, nil
		}
		return siriusXMConfig{}, err
	}
	var c siriusXMConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return siriusXMConfig{}, err
	}
	return c, nil
}

func writeSiriusXMConfig(c siriusXMConfig) error {
	p, err := siriusXMConfigPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (a *App) SiriusXMConfig() (map[string]string, error) {
	c, err := readSiriusXMConfig()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"username":     c.Username,
		"sxm_script":   "",
		"relay_script": "",
	}, nil
}

func (a *App) SaveSiriusXMConfig(username, password, _, _ string) error {
	username = strings.TrimSpace(username)
	old, _ := readSiriusXMConfig()
	if password == "__KEEP__" {
		password = old.Password
	}
	if username == "" || password == "" {
		return errors.New("SiriusXM username and password are required")
	}
	return writeSiriusXMConfig(siriusXMConfig{
		Username: username,
		Password: password,
	})
}

func (a *App) StartSiriusXM() error {
	if runtime.GOOS != "windows" {
		return errors.New("the native SiriusXM integration currently supports Windows desktop builds")
	}

	nativeSiriusMu.Lock()
	defer nativeSiriusMu.Unlock()

	c, err := readSiriusXMConfig()
	if err != nil {
		return err
	}
	if c.Username == "" || c.Password == "" {
		return errors.New("save your SiriusXM login first")
	}

	if nativeSiriusClient == nil ||
		nativeSiriusClient.username != c.Username ||
		nativeSiriusClient.password != c.Password {
		nativeSiriusClient = newSiriusXMClient(c.Username, c.Password)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if !nativeSiriusClient.authenticate(ctx) {
		return errors.New("SiriusXM authentication failed")
	}

	if nativeSiriusServer == nil {
		nativeSiriusServer = newSXMRelayServer(nativeSiriusClient, a.logger)
	}
	if err := nativeSiriusServer.start(); err != nil {
		return fmt.Errorf("could not start native SiriusXM relay on port %d: %w", sxmListenPort, err)
	}

	a.logger.Info("Native SiriusXM started", "listen", sxmListenAddr)
	return nil
}

func (a *App) StopSiriusXM() {
	nativeSiriusMu.Lock()
	defer nativeSiriusMu.Unlock()
	if nativeSiriusServer != nil {
		nativeSiriusServer.stop()
		nativeSiriusServer = nil
	}
	nativeSiriusClient = nil
	a.logger.Info("Native SiriusXM stopped")
}

func (a *App) SiriusXMStatus() (siriusXMState, error) {
	_, err := readSiriusXMConfig()
	if err != nil {
		return siriusXMState{}, err
	}
	nativeSiriusMu.Lock()
	running := nativeSiriusServer != nil && nativeSiriusServer.server != nil
	nativeSiriusMu.Unlock()
	return siriusXMState{
		SXMRunning:   running,
		RelayRunning: running,
		HLSURL:       fmt.Sprintf("http://127.0.0.1:%d/", sxmListenPort),
		RelayURL:     fmt.Sprintf("http://127.0.0.1:%d/", sxmListenPort),
		Native:       true,
	}, nil
}

func (a *App) SiriusXMURL(host, mount string) (string, error) {
	mount = strings.Trim(mount, "/")
	if mount == "" {
		return "", errors.New("station mount is required")
	}
	ip, err := resolveSXMLocalIP(host)
	if err != nil {
		return "", fmt.Errorf("could not determine the PC LAN address for the speaker: %w", err)
	}
	return fmt.Sprintf("http://%s:%d/%s", ip, sxmListenPort, url.PathEscape(mount)), nil
}

func (a *App) SiriusXMPlay(host string, port int, mount, name string) error {
	if err := a.StartSiriusXM(); err != nil {
		return err
	}
	streamURL, err := a.SiriusXMURL(host, mount)
	if err != nil {
		return err
	}
	// The native relay outputs a continuous ADTS-AAC stream.
	return a.PlayURL(host, port, streamURL, name, "", "", "", "", "AAC")
}

func (a *App) SiriusXMStations() ([]map[string]string, error) {
	out := make([]map[string]string, 0, len(nativeStationList))
	for _, s := range nativeStationList {
		out = append(out, map[string]string{
			"id": s.ID, "mount": s.Mount, "name": s.Name,
		})
	}
	return out, nil
}

func resolveSXMLocalIP(host string) (string, error) {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(host, "9"), time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String(), nil
	}
	return "", errors.New("unable to determine local IP")
}
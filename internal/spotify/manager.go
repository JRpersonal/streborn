// Package spotify runs go-librespot as a persistent Spotify Connect
// receiver on the speaker and exposes its audio so the box can play it
// over UPnP, the audio plane of the Spotify-preset feature (#78, P1).
//
// Why go-librespot (devgianlu) and not librespot-org:
//   - Hardware preset buttons 1..6 must recall a saved Spotify playlist
//     autonomously, with no phone app present. That needs the box to be
//     able to say "play URI X" by itself. librespot-org has no local
//     control API, so its only autonomous path is the Spotify Web API
//     with a refreshable OAuth token stored on the box (a security
//     surface we do not want). go-librespot ships a local HTTP API:
//     POST /player/play {uri} plays a URI using its own cached
//     credential, no token plane. See Play below.
//   - GPL-3.0 is fine here: go-librespot runs as a separate sidecar
//     process (exec + localhost HTTP). STR merely aggregates it; the
//     agent stays MIT. The binary is built, attested, audited and
//     credited separately.
//
// Audio shape:
//   - go-librespot runs with the STR Ogg-passthrough patch
//     (.github/patches/go-librespot-passthrough.patch) and
//     audio_output_pipe_passthrough. We point audio_output_pipe at
//     /dev/stdout so it writes the raw Ogg/Vorbis to its stdout (it logs
//     to stderr); the manager drains that and ServeOgg streams it to the
//     box, which decodes the Ogg natively over UPnP. This roughly halves
//     CPU on the weak A8 vs streaming decoded PCM (validated live).
//
// Credentials: zeroconf with persist_credentials. The user taps the
// device once in the Spotify app (the natural "connect to a speaker"
// flow); go-librespot persists the reusable credential under configDir
// and auto-logs-in on every later start, so API-driven recall works
// with no controller attached.
//
// Single consumer by design: one box plays one Spotify stream at a time.
// When no HTTP client is attached the audio is discarded so go-librespot
// never blocks on a full pipe.
package spotify

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/gorilla/websocket"
)

// vorbisRate is Spotify's Ogg/Vorbis sample rate; the Ogg granule position
// counts samples at this rate, which is how the drain turns bytes into a
// measured bitrate.
const vorbisRate = 44100

// flushThreshold batches Ogg pages before each write+flush to the box. The
// Bose firmware leaks memory in proportion to how many tiny HTTP chunks it
// receives, so bigger batches mean less leak. Live sweep (2026-06-05, per-value
// leak rate over a fresh boot):
//
//	  4 KB (per-page)  3.4 MB/min   stable
//	 16 KB            1.4 MB/min   stable
//	128 KB            0.6 MB/min   borderline
//	256 KB            0.38 MB/min  occasional underrun restart (~1 in 3 tracks)
//	512 KB            0.48 MB/min  frequent underrun restarts (no leak gain)
//
// The leak floors at ~0.4 MB/min (the irreducible live-streaming component);
// past ~256 KB there is no gain, only more underruns. Underruns happen because
// the single-goroutine drain blocks while writing a big batch and stops reading
// go-librespot, leaving a gap the box re-fetches over. 256 KB is the chosen
// operating point: lowest leak at the floor, with rare restarts Jens accepted
// in exchange. A lead/jitter buffer (decouple read from write) would remove the
// underruns and let it run here cleanly; tracked as a follow-up. Runtime
// override: /mnt/nv/streborn/spotify-flush-kb. Header replay is exempt.
const flushThreshold = 256 * 1024

// Manager supervises one go-librespot process and brokers its PCM output
// (as a live WAV stream) to at most one HTTP consumer (the speaker),
// plus drives playback through go-librespot's local HTTP API.
type Manager struct {
	binPath    string
	configDir  string
	fallback   string // device name used until the box's friendly name is known
	apiAddr    string // host:port of go-librespot's HTTP API
	logger     *slog.Logger
	bitr       int            // 96/160/320
	client     *http.Client   // short ops: pause/resume/volume/info
	playClient *http.Client   // /player/play: a cold playlist load can take >5s
	box        *boxapi.Client // box REST: friendly name (device_name) + volume bridge

	mu   sync.Mutex
	name string    // device name currently written to config.yml
	sink io.Writer // current HTTP consumer, nil when none
	cmd  *exec.Cmd
	// runCancel restarts the current go-librespot process when called: it
	// cancels the per-process context so the supervise loop relaunches it.
	// Used to re-apply a changed device_name (go-librespot reads its name only
	// at start). nil while no process runs.
	runCancel context.CancelFunc
	// actualKbps is the bitrate measured from the live Ogg stream (body bytes
	// per granule second). 0 until enough of a track has streamed.
	actualKbps int
	// curName/curArtist/curCover hold the currently-playing track's metadata,
	// captured from go-librespot's /events so the desktop app (and later the
	// box display) can show the live artist/title/cover during Spotify playback.
	curName, curArtist, curCover string
	// headerPages holds the current track's Ogg header pages (the BOS page
	// with the Vorbis identification header plus the comment/setup pages).
	// The drain captures them as they stream past; ServeOgg replays them to
	// a freshly-attached box before the live data, so a box that joins
	// mid-track still gets the headers it needs to start decoding (the next
	// real BOS is a whole track away). This is the Icecast late-joiner
	// pattern.
	headerPages []byte
}

// New returns a Manager. binPath is the go-librespot binary, configDir
// the config + credential directory (config.yml is written there on
// Run; the persisted zeroconf credential lives there after the first
// Spotify-app tap). box is the Bose REST client: the manager reads the
// speaker's friendly name from it (so the Spotify Connect device and its
// local mDNS advert carry the speaker's own name, not a hardcoded one) and
// bridges Spotify volume changes onto the box. fallbackName is used only
// until the box answers /info.
func New(binPath, configDir, fallbackName string, box *boxapi.Client, logger *slog.Logger) *Manager {
	if fallbackName == "" {
		fallbackName = "ST Reborn"
	}
	return &Manager{
		binPath:    binPath,
		configDir:  configDir,
		fallback:   fallbackName,
		name:       fallbackName,
		box:        box,
		apiAddr:    "127.0.0.1:3678",
		logger:     logger,
		bitr:       160,
		client:     &http.Client{Timeout: 5 * time.Second},
		playClient: &http.Client{Timeout: 25 * time.Second},
	}
}

// Ready reports whether go-librespot can run: the binary exists. The
// device advertises over zeroconf even before the first tap, so we start
// it whenever the binary is present; playback control just returns an
// error until a credential is cached.
func (m *Manager) Ready() bool {
	if m.binPath == "" {
		return false
	}
	if fi, err := os.Stat(m.binPath); err != nil || fi.IsDir() {
		return false
	}
	return true
}

// configYAML is the go-librespot config the manager writes. /dev/stdout as
// the pipe + passthrough makes go-librespot emit the raw Ogg/Vorbis on its
// stdout (no decode); the box decodes it natively, which on the weak A8
// roughly halves CPU vs streaming decoded PCM. The API server gives us
// local playback control; zeroconf + persist gives a tap-once,
// auto-login-forever credential. Passthrough needs the STR patch
// (.github/patches/go-librespot-passthrough.patch) baked into the binary.
func (m *Manager) configYAML(name string, initialVol int) string {
	host, port := splitHostPort(m.apiAddr)
	var b strings.Builder
	fmt.Fprintf(&b, "device_name: %q\n", name)
	b.WriteString("device_type: speaker\n")
	fmt.Fprintf(&b, "bitrate: %d\n", m.bitr)
	b.WriteString("audio_backend: pipe\n")
	b.WriteString("audio_output_pipe: /dev/stdout\n")
	b.WriteString("audio_output_pipe_format: s16le\n")
	b.WriteString("audio_output_pipe_passthrough: true\n")
	// Volume bridge: the box owns the actual volume (passthrough Ogg can't be
	// scaled by go-librespot), so external_volume makes go-librespot forward
	// Connect volume changes as /events instead of applying them; the manager
	// mirrors those onto the box and back (with echo dedup, see watchVolume /
	// SetVolume). volume_steps 100 makes the value a percent; initial_volume
	// seeds it with the box's real level so the Spotify app shows it correctly.
	b.WriteString("external_volume: true\n")
	b.WriteString("volume_steps: 100\n")
	fmt.Fprintf(&b, "initial_volume: %d\n", initialVol)
	b.WriteString("server:\n")
	b.WriteString("  enabled: true\n")
	fmt.Fprintf(&b, "  address: %s\n", host)
	fmt.Fprintf(&b, "  port: %s\n", port)
	b.WriteString("credentials:\n")
	b.WriteString("  type: zeroconf\n")
	b.WriteString("  zeroconf:\n")
	b.WriteString("    persist_credentials: true\n")
	return b.String()
}

// boxNameAndVolume reads the speaker's friendly name and current volume from
// the Bose REST API. It returns the fallback name and volume 100 when the box
// is not reachable yet (cold boot), so config writing never blocks on it.
func (m *Manager) boxNameAndVolume(ctx context.Context) (name string, vol int) {
	name, vol = m.fallback, 100
	if m.box == nil {
		return name, vol
	}
	st, err := m.box.LoadSettings(ctx)
	if err != nil {
		return name, vol
	}
	if n := strings.TrimSpace(st.Info.Name); n != "" {
		name = n
	}
	if st.Volume.Actual >= 0 && st.Volume.Actual <= 100 {
		vol = st.Volume.Actual
	}
	return name, vol
}

func (m *Manager) ensureConfig(ctx context.Context) error {
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		return err
	}
	name, vol := m.boxNameAndVolume(ctx)
	m.mu.Lock()
	m.name = name
	m.mu.Unlock()
	// No audio cache handling needed: go-librespot does not cache audio to
	// disk (verified in its source; only the tiny config + credential files
	// land in configDir). The NAND-filling cache seen earlier was the old
	// librespot (Rust, --cache), not go-librespot.
	return os.WriteFile(filepath.Join(m.configDir, "config.yml"), []byte(m.configYAML(name, vol)), 0o644)
}

// Run supervises go-librespot until ctx is cancelled, restarting it with
// a short backoff if it exits. It returns immediately (idles) when not
// Ready, so callers can start it unconditionally.
func (m *Manager) Run(ctx context.Context) {
	if !m.Ready() {
		m.logger.Info("spotify manager idle: no go-librespot binary")
		return
	}
	if err := m.ensureConfig(ctx); err != nil {
		m.logger.Warn("spotify: cannot write config, manager idle", "err", err)
		return
	}
	// watchDeviceName stays DISABLED: it flapped the device name on transient
	// /info failures and restarted go-librespot, churning the box.
	// watchVolume is re-enabled now that its goroutine leak is fixed (per-call
	// ctx in volumeStream): it mirrors Spotify-app volume changes onto the box
	// so the Connect remote controls the speaker volume. The box -> Spotify
	// feedback direction is added separately with echo dedup to avoid a loop.
	go m.watchVolume(ctx)
	for ctx.Err() == nil {
		if err := m.runOnce(ctx); err != nil && ctx.Err() == nil {
			m.logger.Warn("go-librespot exited, restarting", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// watchDeviceName re-resolves the speaker's friendly name periodically. When
// it changes (cold boot finally answering /info, or a user rename), it
// rewrites config.yml and restarts go-librespot, but only while no box is
// streaming so playback is never interrupted. This is what makes the Spotify
// Connect device and its local mDNS advert carry the speaker's own name.
func (m *Manager) watchDeviceName(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		name, vol := m.boxNameAndVolume(ctx)
		m.mu.Lock()
		changed := name != m.name
		streaming := m.sink != nil
		restart := m.runCancel
		m.mu.Unlock()
		if !changed || streaming {
			continue
		}
		if err := os.WriteFile(filepath.Join(m.configDir, "config.yml"),
			[]byte(m.configYAML(name, vol)), 0o644); err != nil {
			m.logger.Warn("spotify: rewrite config for name change failed", "err", err)
			continue
		}
		m.mu.Lock()
		m.name = name
		m.mu.Unlock()
		m.logger.Info("spotify: device name changed, restarting go-librespot", "name", name)
		if restart != nil {
			restart()
		}
	}
}

func (m *Manager) runOnce(ctx context.Context) error {
	// go-librespot uses pflag: the long flag needs two dashes (-config_dir
	// is misparsed as a shorthand cluster). HOME is forced into the
	// writable config dir because the box rootfs is read-only and
	// go-librespot otherwise tries to create ~/.config.
	// Per-process context so watchDeviceName can restart just this run (to
	// re-apply a changed device_name) without tearing down the manager.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	cmd := exec.CommandContext(runCtx, m.binPath, "--config_dir", m.configDir)
	cmd.Env = append(os.Environ(), "HOME="+m.configDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = newLogWriter(m.logger)
	if err := cmd.Start(); err != nil {
		return err
	}
	m.mu.Lock()
	m.cmd = cmd
	m.runCancel = runCancel
	m.mu.Unlock()

	// flushBytes is how much is batched before each write+flush to the box
	// (see flushThreshold). Tunable at runtime via a NAND file so the leak can
	// be swept without rebuilding: write the KB value there and restart
	// go-librespot. Falls back to the compiled default.
	flushBytes := flushThreshold
	if b, err := os.ReadFile("/mnt/nv/streborn/spotify-flush-kb"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n >= 1 && n <= 1024 {
			flushBytes = n * 1024
			m.logger.Info("spotify: flush batch overridden", "kb", n)
		}
	}

	// Drain go-librespot's Ogg output page by page and forward whole pages to
	// the box. While no box is attached, capture the current track's header
	// pages and pause go-librespot so it does not race to the end of the
	// playlist unheard; ServeOgg resumes it and replays the headers when a
	// box joins, so a mid-track joiner can still decode.
	r := bufio.NewReaderSize(stdout, 256*1024)
	var hdr []byte
	capturing := false
	paused := false
	// Bitrate measurement: body bytes and the highest granule (sample count at
	// vorbisRate) seen since the current track's BOS. kbps = bytes*8 over the
	// elapsed seconds. This is the real stream rate, not the configured nominal.
	var trackBody, maxGran int64
	// pending batches pages so the box receives large chunks instead of one
	// tiny chunk per page (see flushThreshold: small chunks leak box memory).
	var pending []byte
	// trackNum + forwarded count instrument the playback so the occasional
	// "track restarts at its start" can be diagnosed: track boundaries (new
	// BOS) and box (re)attaches are logged with byte/granule context.
	trackNum := 0
	var forwarded int64
	for {
		page, err := readOggPage(r)
		if err != nil {
			break
		}

		// Maintain the current track's header pages: a BOS page starts a
		// track (Vorbis identification header), the following granule<=0
		// pages carry comment/setup, the first audio page (granule>0) ends
		// the header sequence.
		htype := page[5]
		gran := int64(binary.LittleEndian.Uint64(page[6:14]))
		numSegs := int(page[26])
		bodyLen := int64(len(page) - 27 - numSegs)
		switch {
		case htype&0x02 != 0: // BOS
			// New logical stream = track boundary. Log it with the previous
			// track's size so a premature/duplicate BOS (the suspected cause of
			// a track restarting at its start) is visible in the log.
			m.logger.Info("spotify: track boundary (BOS)",
				"track", trackNum+1, "prevTrackKB", trackBody/1024,
				"prevMaxGran", maxGran, "forwardedKB", forwarded/1024)
			trackNum++
			hdr = append([]byte(nil), page...)
			capturing = true
			trackBody, maxGran = 0, 0
		case capturing && gran > 0: // first audio page
			m.mu.Lock()
			m.headerPages = hdr
			m.mu.Unlock()
			capturing = false
		case capturing:
			hdr = append(hdr, page...)
		}
		trackBody += bodyLen
		if gran > maxGran {
			maxGran = gran
		}
		if maxGran > vorbisRate { // at least one second streamed
			kbps := int(trackBody * 8 * vorbisRate / (maxGran * 1000))
			m.mu.Lock()
			m.actualKbps = kbps
			m.mu.Unlock()
		}

		m.mu.Lock()
		sink := m.sink
		haveHdr := len(m.headerPages) > 0
		m.mu.Unlock()

		if sink != nil {
			paused = false
			// Track-boundary flush: a new BOS is a new logical Vorbis stream
			// (the box must reload codebooks). If the BOS is buried mid-batch
			// behind the previous track's tail, the box re-inits on a partial
			// chunk and the new track audibly restarts (live-observed, ~1 in 3
			// tracks). Flushing the tail first makes the BOS begin on a clean
			// chunk boundary so the decoder re-inits cleanly.
			if htype&0x02 != 0 && len(pending) > 0 {
				m.forward(sink, pending)
				pending = pending[:0]
			}
			// Batch pages into large writes (see flushThreshold) so the box
			// gets large chunks, not a tiny chunk per page.
			pending = append(pending, page...)
			forwarded += int64(len(page))
			if len(pending) >= flushBytes {
				m.forward(sink, pending)
				pending = pending[:0]
			}
			continue
		}
		// No consumer: drop any half-filled batch so a freshly attaching box
		// starts clean, then once a track's headers are captured pause
		// go-librespot so it stops producing (no racing) until a box attaches
		// and ServeOgg resumes it.
		pending = pending[:0]
		if !paused && haveHdr {
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			_ = m.Pause(pctx)
			cancel()
			paused = true
		}
		if ctx.Err() != nil {
			break
		}
	}
	return cmd.Wait()
}

// forward writes p to the box sink; on write error it drops the sink.
func (m *Manager) forward(sink io.Writer, p []byte) {
	if _, err := sink.Write(p); err != nil {
		m.mu.Lock()
		if m.sink == sink {
			m.sink = nil
		}
		m.mu.Unlock()
		return
	}
	if f, ok := sink.(http.Flusher); ok {
		f.Flush()
	}
}

// readOggPage reads one complete Ogg page from r, syncing to the "OggS"
// capture pattern. The returned slice is a whole page (27-byte header +
// segment table + body).
func readOggPage(r *bufio.Reader) ([]byte, error) {
	for { // sync to "OggS"
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 'O' {
			continue
		}
		p, err := r.Peek(3)
		if err != nil {
			return nil, err
		}
		if p[0] == 'g' && p[1] == 'g' && p[2] == 'S' {
			if _, err := r.Discard(3); err != nil {
				return nil, err
			}
			break
		}
	}
	// 23 bytes after "OggS": version, header_type, granule(8), serial(4),
	// page_seq(4), crc(4), page_segments(1).
	rest := make([]byte, 23)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}
	numSegs := int(rest[22])
	segs := make([]byte, numSegs)
	if _, err := io.ReadFull(r, segs); err != nil {
		return nil, err
	}
	bodyLen := 0
	for _, s := range segs {
		bodyLen += int(s)
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	page := make([]byte, 0, 4+23+numSegs+bodyLen)
	page = append(page, 'O', 'g', 'g', 'S')
	page = append(page, rest...)
	page = append(page, segs...)
	page = append(page, body...)
	return page, nil
}

// Play asks go-librespot to start playing a Spotify URI on this device,
// using its own cached credential. This is the autonomous-recall path:
// the agent calls it on a Spotify preset press, with no app involved.
//
// The recall calls Play FIRST, then points the box at /spotify/stream.ogg.
// With the sink-gated drain (see runOnce) go-librespot blocks on its full
// output pipe until the box attaches, so the pipe holds the track's start
// incl. the Ogg headers; the box therefore receives the stream from the
// beginning and can decode it.
func (m *Manager) Play(ctx context.Context, uri string) error {
	// Enable shuffle BEFORE loading the context so go-librespot builds a
	// shuffled queue and starts on a random track. Setting it after play only
	// flips the flag without reshuffling the already-built queue (live-observed:
	// tracks still played in playlist order). Best-effort.
	if err := m.apiPost(ctx, "/player/shuffle_context", `{"shuffle_context":true}`); err != nil {
		m.logger.Debug("spotify: enable shuffle (pre-play) failed", "err", err)
	}
	if err := m.apiPostC(ctx, m.playClient, "/player/play", `{"uri":`+jsonString(uri)+`}`); err != nil {
		return err
	}
	// No post-play /player/next: with shuffle enabled before play, go-librespot
	// already starts on a random track at position 0. A skip here only made the
	// track begin mid-song (live-observed).
	return nil
}

// Pause and Resume mirror the obvious controls.
func (m *Manager) Pause(ctx context.Context) error {
	return m.apiPost(ctx, "/player/pause", "")
}

func (m *Manager) Resume(ctx context.Context) error {
	return m.apiPost(ctx, "/player/resume", "")
}

// SetVolume tells go-librespot the current volume as a percent (0..100) so the
// Spotify app's slider reflects the speaker's real level. With volume_steps
// 100 the API value is the percent directly. This is the box -> Spotify
// direction; the box -> go-librespot caller is the gabbo volumeUpdated hook.
func (m *Manager) SetVolume(ctx context.Context, pct int) error {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return m.apiPost(ctx, "/player/volume", fmt.Sprintf(`{"volume":%d}`, pct))
}

// Bitrate returns the bitrate measured from the live stream (kbit/s), or the
// configured nominal when nothing has streamed yet.
func (m *Manager) Bitrate() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.actualKbps > 0 {
		return m.actualKbps
	}
	return m.bitr
}

// Streaming reports whether a box is currently attached to the Ogg stream
// (i.e. Spotify is actively playing to the speaker). The memory guard uses
// this to avoid rebooting the box mid-playback.
func (m *Manager) Streaming() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sink != nil
}

// DeviceName returns the name currently advertised to Spotify.
func (m *Manager) DeviceName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.name
}

// ServeInfo answers GET /spotify/info with the live state the UI needs: whether
// Spotify is available, the measured bitrate, and the advertised device name.
func (m *Manager) ServeInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	m.mu.Lock()
	track, artist, cover := m.curName, m.curArtist, m.curCover
	m.mu.Unlock()
	resp := struct {
		Ready   bool   `json:"ready"`
		Bitrate int    `json:"bitrate"`
		Name    string `json:"name"`
		Track   string `json:"track"`
		Artist  string `json:"artist"`
		Cover   string `json:"cover"`
	}{
		Ready:   m.Ready(),
		Bitrate: m.Bitrate(),
		Name:    m.DeviceName(),
		Track:   track,
		Artist:  artist,
		Cover:   cover,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// watchVolume subscribes to go-librespot's /events WebSocket and mirrors every
// Spotify-app volume change onto the box. go-librespot runs with
// external_volume, so a Connect volume command does not touch its audio; it
// surfaces here as a "volume" event {value, max} which we scale to a percent
// and push to the box over the Bose REST API. Reconnects with a short backoff.
func (m *Manager) watchVolume(ctx context.Context) {
	url := "ws://" + m.apiAddr + "/events"
	for ctx.Err() == nil {
		if err := m.volumeStream(ctx, url); err != nil && ctx.Err() == nil {
			m.logger.Debug("spotify: events stream ended", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (m *Manager) volumeStream(ctx context.Context, url string) error {
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := d.DialContext(ctx, url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Closer goroutine is bound to a per-call context cancelled on return, so
	// it never outlives this stream. The earlier version waited on the
	// long-lived parent ctx and leaked one goroutine (holding conn) per
	// reconnect; with frequent go-librespot restarts that fed an OOM.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-sctx.Done(); conn.Close() }()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var ev struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "metadata":
			// Current track info for the desktop (and later box) display.
			var md struct {
				Name          string   `json:"name"`
				ArtistNames   []string `json:"artist_names"`
				AlbumCoverURL string   `json:"album_cover_url"`
			}
			if err := json.Unmarshal(ev.Data, &md); err != nil {
				continue
			}
			m.mu.Lock()
			m.curName = md.Name
			m.curArtist = strings.Join(md.ArtistNames, ", ")
			m.curCover = md.AlbumCoverURL
			m.mu.Unlock()
		case "volume":
			if m.box == nil {
				continue // no box client: metadata only, no volume mirror
			}
			var vd struct {
				Value int `json:"value"`
				Max   int `json:"max"`
			}
			if err := json.Unmarshal(ev.Data, &vd); err != nil {
				continue
			}
			pct := 100
			if vd.Max > 0 {
				pct = vd.Value * 100 / vd.Max
			}
			sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			if err := m.box.SetVolume(sctx, pct); err != nil {
				m.logger.Debug("spotify: box SetVolume from Spotify event failed", "err", err, "pct", pct)
			}
			cancel()
			m.logger.Info("spotify: volume mirrored to box", "pct", pct)
		}
	}
}

// jsonString quotes a string as a JSON value.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (m *Manager) apiPost(ctx context.Context, path string, body string) error {
	return m.apiPostC(ctx, m.client, path, body)
}

func (m *Manager) apiPostC(ctx context.Context, client *http.Client, path string, body string) error {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+m.apiAddr+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("go-librespot %s: %w", path, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("go-librespot %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// ServeOgg streams go-librespot's live Ogg/Vorbis passthrough output to the
// HTTP client (the box's UPnP fetch) until it disconnects. It registers as
// the single consumer; a new request replaces any previous one. No header is
// prepended: the box decodes the raw Ogg directly.
func (m *Manager) ServeOgg(w http.ResponseWriter, r *http.Request) {
	if !m.Ready() {
		http.Error(w, "spotify not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "audio/ogg")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)

	// Replay the current track's cached Ogg header pages first so a box that
	// joins mid-track has the identification/comment/setup headers it needs
	// to initialise the decoder; the live pages (forwarded by the drain)
	// then follow and are decodable even though they start mid-track.
	m.mu.Lock()
	hdr := append([]byte(nil), m.headerPages...)
	m.mu.Unlock()
	if len(hdr) > 0 {
		if _, err := w.Write(hdr); err != nil {
			return
		}
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	done := make(chan struct{})
	cw := &closeNotifyWriter{w: w, done: done}
	m.mu.Lock()
	reattach := m.sink != nil // a consumer was already attached = box re-fetched
	m.sink = cw
	m.mu.Unlock()
	// reattach=true means the box dropped and re-fetched the stream (the prime
	// suspect for a track appearing to restart): it then gets the cached
	// granule-0 headers again. Logged so the restart can be correlated.
	m.logger.Info("spotify: box attached to Ogg stream", "remote", r.RemoteAddr, "headerBytes", len(hdr), "reattach", reattach)

	// The drain pauses go-librespot while no box is attached; resume it so
	// the live stream flows to this box.
	rctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	_ = m.Resume(rctx)
	cancel()

	select {
	case <-r.Context().Done():
	case <-done:
	}
	m.mu.Lock()
	if m.sink == cw {
		m.sink = nil
	}
	m.mu.Unlock()
	m.logger.Info("spotify: box detached from Ogg stream")
}

// closeNotifyWriter signals done on the first failed write so ServeOgg
// returns when the box drops the connection.
type closeNotifyWriter struct {
	w    io.Writer
	done chan struct{}
	once sync.Once
}

func (c *closeNotifyWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err != nil {
		c.once.Do(func() { close(c.done) })
	}
	return n, err
}

func (c *closeNotifyWriter) Flush() {
	if f, ok := c.w.(http.Flusher); ok {
		f.Flush()
	}
}

func splitHostPort(addr string) (host, port string) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1", "3678"
	}
	return h, p
}

// logWriter forwards go-librespot stderr lines to the agent logger.
type logWriter struct{ logger *slog.Logger }

func newLogWriter(l *slog.Logger) *logWriter { return &logWriter{logger: l} }

func (w *logWriter) Write(p []byte) (int, error) {
	w.logger.Info("go-librespot", "line", trimEOL(string(p)))
	return len(p), nil
}

func trimEOL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

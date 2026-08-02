// config.go: the go-librespot config file the manager writes — YAML
// rendering, the advertised device name, zeroconf interface pinning, and
// the one-shot refresh from the box's real name and volume.

package spotify

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	fmt.Fprintf(&b, "device_name: %q\n", advertisedName(name))
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
	// Always honour initial_volume on start instead of the last saved volume:
	// go-librespot persists the volume and restores it next start, which made
	// the Spotify app slider start at the stale/100 value instead of the box's
	// real level. With this, initial_volume (seeded from the box) wins.
	b.WriteString("ignore_last_volume: true\n")
	b.WriteString("server:\n")
	b.WriteString("  enabled: true\n")
	fmt.Fprintf(&b, "  address: %s\n", host)
	fmt.Fprintf(&b, "  port: %s\n", port)
	// Pin upstream's disk audio cache OFF. Its default is already false, but
	// the default must never decide this: STR sets HOME to the NAND config
	// dir, so an enabled cache would resolve its XDG default directory onto
	// NAND with a 1 GB size limit and grind the box's flash. Older engine
	// builds without the key ignore it (non-strict koanf loader).
	b.WriteString("cache:\n")
	b.WriteString("  enabled: false\n")
	b.WriteString("credentials:\n")
	b.WriteString("  type: zeroconf\n")
	b.WriteString("  zeroconf:\n")
	b.WriteString("    persist_credentials: true\n")
	// Pin the Spotify Connect advert to one interface on multi-homed boxes so
	// the same speaker is not announced twice in the Spotify app (#331). sm2
	// boxes carry two MACs (SCM SoC + SMSC bridge) on one IP; with no pin,
	// go-librespot's zeroconf advertises on every interface. This is the
	// top-level zeroconf_interfaces_to_advertise key (not nested under
	// credentials). Older go-librespot builds without this koanf key ignore it
	// (the loader is non-strict), so it is a safe no-op there; an empty result
	// keeps the advertise-on-all default rather than suppressing the advert.
	if iface := primaryZeroconfIface(); iface != "" {
		fmt.Fprintf(&b, "zeroconf_interfaces_to_advertise:\n  - %q\n", iface)
	}
	return b.String()
}

// strDeviceNameSuffix marks STR's go-librespot Spotify Connect entry so it is
// distinguishable from the box's native Bose eSDK Connect entry. STR keeps the
// native advert alive on purpose (marge advertises SPOTIFY so free Spotify
// accounts, which go-librespot refuses, still work), so both announce
// _spotify-connect._tcp under the box's own name; without a marker the Spotify
// app shows two identically named entries per box (#331, "die Namen sind
// identisch").
const strDeviceNameSuffix = " (STR)"

// advertisedName is the device_name go-librespot registers for Spotify Connect:
// the box's friendly name plus the STR marker. Guarded so a config rewrite never
// stacks the marker, and an empty name is left unchanged (go-librespot then
// falls back to its own default).
func advertisedName(base string) string {
	base = strings.TrimSpace(base)
	if base == "" || strings.HasSuffix(base, strDeviceNameSuffix) {
		return base
	}
	return base + strDeviceNameSuffix
}

// primaryZeroconfIface returns the single network interface go-librespot should
// advertise Spotify Connect on, or "" to keep the advertise-on-all default. It
// picks the first UP, non-loopback, multicast interface bearing a routable IPv4
// - wlan0 on sm2 boxes, eth0 on the Portable (taigan presents its Wi-Fi as
// eth0), so it is model-agnostic and must not be hardcoded. Returning the first
// such interface collapses the two-MAC/one-IP box to a single advert.
func primaryZeroconfIface() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if v4 := n.IP.To4(); v4 != nil && v4.IsGlobalUnicast() && !v4.IsLinkLocalUnicast() {
				return ifc.Name
			}
		}
	}
	return ""
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
	m.configVol = vol
	m.mu.Unlock()
	// No audio cache handling needed: go-librespot does not cache audio to
	// disk (verified in its source; only the tiny config + credential files
	// land in configDir). The NAND-filling cache seen earlier was the old
	// librespot (Rust, --cache), not go-librespot.
	return os.WriteFile(filepath.Join(m.configDir, "config.yml"), []byte(m.configYAML(name, vol)), 0o644)
}

// refreshVolumeConfigOnce rewrites config.yml with the box's real name and
// volume once the box REST API first answers, then restarts go-librespot a
// single time. config.yml is first written at agent start, usually before the
// box is up, so device_name and initial_volume fall back (volume 100), which
// made the Spotify app slider start at 100% and jump on first touch. With
// ignore_last_volume true and a correct initial_volume, go-librespot then
// reports the box's real level. One shot only (no polling), so it cannot flap
// like the old name watcher; skips the rewrite when already correct and the
// restart when a box is streaming.
func (m *Manager) refreshVolumeConfigOnce(ctx context.Context) {
	if m.box == nil {
		return
	}
	t := time.NewTicker(8 * time.Second)
	defer t.Stop()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if time.Now().After(deadline) {
			return
		}
		st, err := m.box.LoadSettings(ctx)
		if err != nil {
			continue // box REST not up yet
		}
		name := strings.TrimSpace(st.Info.Name)
		if name == "" {
			name = m.fallback
		}
		vol := st.Volume.Actual
		if vol < 0 || vol > 100 {
			vol = 100
		}
		m.mu.Lock()
		unchanged := name == m.name && vol == m.configVol
		streaming := m.sink != nil
		restart := m.runCancel
		m.mu.Unlock()
		if unchanged {
			return // initial config was already correct
		}
		if err := os.WriteFile(filepath.Join(m.configDir, "config.yml"),
			[]byte(m.configYAML(name, vol)), 0o644); err != nil {
			m.logger.Warn("spotify: refresh config failed", "err", err)
			return
		}
		m.mu.Lock()
		m.name = name
		m.configVol = vol
		m.mu.Unlock()
		m.logger.Info("spotify: refreshed config from box", "name", name, "vol", vol, "restart", !streaming)
		if !streaming && restart != nil {
			restart()
		}
		return // one shot
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

// DeviceName returns the name currently advertised to Spotify - the box's
// friendly name plus the STR marker (see advertisedName), matching what the
// Spotify app shows so the STR UI and the Connect picker agree. m.name itself
// stays the bare friendly name so watchDeviceName's change detection is not
// tripped every cycle by the suffix.
func (m *Manager) DeviceName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return advertisedName(m.name)
}

func splitHostPort(addr string) (host, port string) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1", "3678"
	}
	return h, p
}

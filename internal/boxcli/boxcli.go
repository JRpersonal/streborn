// Package boxcli sends commands to Bose's local CLI server on port
// 17000. We use it to wake the box from standby before we send a UPnP
// play.
package boxcli

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Send sends a single command to port 17000 and collects up to 200 ms
// of output. The box typically answers immediately.
func Send(ctx context.Context, host, cmd string) (string, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", host+":17000")
	if err != nil {
		return "", fmt.Errorf("dial cli: %w", err)
	}
	defer conn.Close()

	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	var sb strings.Builder
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		sb.WriteString(line)
		if err != nil {
			break
		}
	}
	return sb.String(), nil
}

// PowerOn wakes the box from standby. Idempotent: if the box is already
// on, it returns OK and does nothing.
func PowerOn(ctx context.Context, host string) error {
	_, err := Send(ctx, host, "sys power")
	return err
}

// WakeAndWait makes sure the box is out of standby. Sends `sys power`
// and polls `/now_playing` until source != STANDBY or timeout. The box
// sometimes reacts with a delay or ignores sys power entirely when it is
// in deep standby; in that case it is sent multiple times.
//
// logger may be nil; when present, per-iteration phase markers are emitted
// so a diagnostic bundle shows the standby-exit timeline (#60).
func WakeAndWait(ctx context.Context, host string, maxWait time.Duration, logger *slog.Logger) error {
	if host == "" {
		host = "127.0.0.1"
	}
	if maxWait <= 0 {
		maxWait = 8 * time.Second
	}
	deadline := time.Now().Add(maxWait)
	client := &http.Client{Timeout: 2 * time.Second}
	infoURL := fmt.Sprintf("http://%s:8090/now_playing", host)
	start := time.Now()
	logPhase := func(msg string, kv ...any) {
		if logger == nil {
			return
		}
		kv = append(kv, "elapsed_ms", time.Since(start).Milliseconds())
		logger.Warn(msg, kv...)
	}

	logPhase("wake phase: start", "host", host, "max_wait", maxWait.String())

	for i := 0; ; i++ {
		// Check first: is the box maybe already awake?
		state, err := readSource(ctx, client, infoURL)
		if err == nil && state != "STANDBY" {
			logPhase("wake phase: already awake", "attempt", i, "source", state)
			return nil
		}
		if err != nil {
			logPhase("wake phase: pre-check read failed", "attempt", i, "err", err.Error())
		} else {
			logPhase("wake phase: STANDBY, sending sys power", "attempt", i, "source", state)
		}
		// Standby or unclear state -> send power on.
		if pwrErr := PowerOn(ctx, host); pwrErr != nil {
			logPhase("wake phase: sys power write failed", "attempt", i, "err", pwrErr.Error())
		}
		// Short pause so the box can process the command.
		select {
		case <-ctx.Done():
			logPhase("wake phase: ctx cancelled", "attempt", i, "err", ctx.Err().Error())
			return ctx.Err()
		case <-time.After(800 * time.Millisecond):
		}
		// Check again
		state, err = readSource(ctx, client, infoURL)
		if err == nil && state != "STANDBY" {
			logPhase("wake phase: woke", "attempt", i, "source", state)
			return nil
		}
		if time.Now().After(deadline) {
			logPhase("wake phase: timeout", "attempts", i+1, "last_source", state)
			return fmt.Errorf("box stays in STANDBY after %d attempts", i+1)
		}
	}
}

// readSource extracts the source attribute from /now_playing.
func readSource(ctx context.Context, c *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	s := string(body[:n])
	// First source="X" attribute
	if i := strings.Index(s, `source="`); i >= 0 {
		rest := s[i+8:]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			return rest[:j], nil
		}
	}
	return "", fmt.Errorf("source attribute not found")
}

// PresetKey simulates a physical preset key press.
//
//	slot 1..6
//	mode "p" = press&release, "ph" = press&hold
func PresetKey(ctx context.Context, host string, slot int, mode string) error {
	if mode == "" {
		mode = "p"
	}
	_, err := Send(ctx, host, fmt.Sprintf("sys presetkey %d %s", slot, mode))
	return err
}

// AddPreset stores a preset on the box so the hardware keys can trigger
// a `nowSelectionUpdated` event with the ContentItem. We set all presets
// as a UPNP source because that is what the box is most likely to accept
// without a running STS worker.
//
// CLI syntax (from BoseApp strings):
//
//	ws AddPreset <SOURCE> <TYPE> <LOCATION> <LABEL> <SOURCEACCOUNT> <PRESETID>
func AddPreset(ctx context.Context, host string, slot int, name, streamURL string) error {
	// LABEL must be in quotes, otherwise the box splits it at the space.
	// LOCATION should have no quotes.
	cmd := fmt.Sprintf(`ws AddPreset UPNP audio %s "%s" UPnPUserName %d`,
		streamURL, name, slot)
	_, err := Send(ctx, host, cmd)
	return err
}

// AddPresetRaw writes a preset for an arbitrary source/type/location/account,
// not just STR's UPnP streams. Used to restore an account-linked preset the box
// dropped (e.g. a Deezer playlist) back onto its original slot, so the box plays
// it again via its own cached account token. Inputs are sanitised for the TAP
// CLI (no quotes/newlines that would break tokenisation).
func AddPresetRaw(ctx context.Context, host string, slot int, source, typ, location, name, account string) error {
	clean := func(s string) string {
		return strings.NewReplacer("\"", "", "\n", " ", "\r", " ").Replace(strings.TrimSpace(s))
	}
	source = clean(source)
	typ = clean(typ)
	location = clean(location)
	account = clean(account)
	name = clean(name)
	if source == "" || location == "" || slot < 1 || slot > 6 {
		return fmt.Errorf("AddPresetRaw: source, location and slot 1..6 required")
	}
	if typ == "" {
		typ = "audio"
	}
	if account == "" {
		account = source + "UserName"
	}
	cmd := fmt.Sprintf(`ws AddPreset %s %s %s "%s" %s %d`, source, typ, location, name, account, slot)
	_, err := Send(ctx, host, cmd)
	return err
}

// RemovePreset deletes the box preset slot.
func RemovePreset(ctx context.Context, host string, slot int) error {
	_, err := Send(ctx, host, fmt.Sprintf("ws RemovePreset %d", slot))
	return err
}

// PresetSpec is a box preset specification for SyncAllPresets.
type PresetSpec struct {
	Slot      int    // 1..6
	Name      string // displayed name (quoted if it contains a space)
	StreamURL string // direct stream URL for UPnP
}

// SyncAllPresets sends all presets as UPNP source ContentItems to the
// box. Should run after a box boot (the box needs ~10s until the CLI
// server has come up) and whenever the stick preset store is updated.
//
// errs is a map of slot -> error for individual slots; continued after
// errors.
func SyncAllPresets(ctx context.Context, host string, presets []PresetSpec) map[int]error {
	errs := map[int]error{}
	for _, p := range presets {
		if p.StreamURL == "" || p.Slot < 1 || p.Slot > 6 {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 4*time.Second)
		if err := AddPreset(c, host, p.Slot, p.Name, p.StreamURL); err != nil {
			errs[p.Slot] = err
		}
		cancel()
	}
	return errs
}

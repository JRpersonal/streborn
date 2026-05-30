// Package boxcli sendet Kommandos an Bose's lokalen CLI Server auf Port
// 17000. Wir nutzen das um die Box aus dem Standby zu wecken bevor wir
// UPnP Play schicken.
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

// Send schickt einen einzelnen Command an Port 17000 und sammelt bis zu
// 200 ms Output. Box antwortet typischerweise sofort.
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

// PowerOn weckt die Box aus dem Standby. Idempotent: wenn Box schon an
// ist, gibt sie OK zurueck und macht nichts.
func PowerOn(ctx context.Context, host string) error {
	_, err := Send(ctx, host, "sys power")
	return err
}

// WakeAndWait stellt sicher dass die Box aus dem Standby ist. Sendet
// `sys power` und pollt `/now_playing` bis source != STANDBY oder Timeout.
// Box reagiert manchmal verzoegert oder ignoriert sys power komplett wenn
// sie in Deep Standby ist; in dem Fall wird mehrmals gesendet.
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
		// Erst pruefen: ist Box vielleicht schon wach?
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
		// Standby oder unklarer State -> power on senden.
		if pwrErr := PowerOn(ctx, host); pwrErr != nil {
			logPhase("wake phase: sys power write failed", "attempt", i, "err", pwrErr.Error())
		}
		// Kurze Pause damit Box den Befehl verarbeiten kann.
		select {
		case <-ctx.Done():
			logPhase("wake phase: ctx cancelled", "attempt", i, "err", ctx.Err().Error())
			return ctx.Err()
		case <-time.After(800 * time.Millisecond):
		}
		// Nochmal checken
		state, err = readSource(ctx, client, infoURL)
		if err == nil && state != "STANDBY" {
			logPhase("wake phase: woke", "attempt", i, "source", state)
			return nil
		}
		if time.Now().After(deadline) {
			logPhase("wake phase: timeout", "attempts", i+1, "last_source", state)
			return fmt.Errorf("box bleibt im STANDBY nach %d Versuchen", i+1)
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
	// Erstes source="X" Attribut
	if i := strings.Index(s, `source="`); i >= 0 {
		rest := s[i+8:]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			return rest[:j], nil
		}
	}
	return "", fmt.Errorf("source attribute not found")
}

// PresetKey simuliert einen physischen Preset Tastendruck.
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

// AddPreset speichert ein Preset auf der Box damit die Hardware Tasten
// einen `nowSelectionUpdated` Event mit dem ContentItem ausloesen koennen.
// Wir setzen alle Presets als UPNP Source weil das die Box am ehesten
// akzeptiert ohne ein laufender STS Worker.
//
// CLI Syntax (aus BoseApp Strings):
//
//	ws AddPreset <SOURCE> <TYPE> <LOCATION> <LABEL> <SOURCEACCOUNT> <PRESETID>
func AddPreset(ctx context.Context, host string, slot int, name, streamURL string) error {
	// LABEL muss in Quotes, sonst splittet die Box ihn beim Leerzeichen.
	// LOCATION sollte keine Quotes haben.
	cmd := fmt.Sprintf(`ws AddPreset UPNP audio %s "%s" UPnPUserName %d`,
		streamURL, name, slot)
	_, err := Send(ctx, host, cmd)
	return err
}

// RemovePreset loescht den Box Preset Slot.
func RemovePreset(ctx context.Context, host string, slot int) error {
	_, err := Send(ctx, host, fmt.Sprintf("ws RemovePreset %d", slot))
	return err
}

// PresetSpec ist eine Box Preset Spezifikation fuer SyncAllPresets.
type PresetSpec struct {
	Slot      int    // 1..6
	Name      string // angezeigter Name (mit Quotes versehen wenn Leerzeichen)
	StreamURL string // direkte Stream URL fuer UPnP
}

// SyncAllPresets schickt alle Presets als UPNP Source ContentItems an die
// Box. Sollte nach Box Boot ausgefuehrt werden (Box braucht ~10s bis CLI
// Server hochgefahren ist) und immer wenn der Stick Preset Store geupdated
// wird.
//
// errs ist ein Map von Slot -> Fehler fuer einzelne Slots; fortgesetzt
// nach Errors.
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

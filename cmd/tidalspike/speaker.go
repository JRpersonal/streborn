package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// testVolume is the only volume the spike ever sets. It is not a flag on
// purpose: a measurement run must never be loud.
const testVolume = 5

const avTransportURN = "urn:schemas-upnp-org:service:AVTransport:1"

// speaker addresses one SoundTouch: the UPnP MediaRenderer on :8091 and the
// Bose web API on :8090. The two bases are fields so the tests can point
// them at httptest servers.
type speaker struct {
	ControlURL string // http://<ip>:8091/AVTransport/Control
	WebBase    string // http://<ip>:8090
	HTTP       *http.Client
	// Sleep and PollEvery exist for the tests.
	Sleep     func(ctx context.Context, d time.Duration) error
	PollEvery time.Duration
}

func newSpeaker(ip string) *speaker {
	return &speaker{
		ControlURL: "http://" + ip + ":8091/AVTransport/Control",
		WebBase:    "http://" + ip + ":8090",
		HTTP:       &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *speaker) sleep(ctx context.Context, d time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// protocolInfo builds the res protocolInfo for a direct HTTP stream. The
// fourth field stays "*": a DLNA.ORG_PN guess that does not match the bytes
// is worse than none (see internal/upnp buildDIDLMime).
func protocolInfo(mime string) string {
	if mime == "" {
		mime = "audio/mpeg"
	}
	return "http-get:*:" + mime + ":*"
}

// didlMeta is what goes into CurrentURIMetaData.
type didlMeta struct {
	Title    string
	Artist   string
	Album    string
	Duration time.Duration
	MimeType string
	URL      string
}

// buildDIDL renders a single-item DIDL-Lite document for SetAVTransportURI.
func buildDIDL(m didlMeta) string {
	title := m.Title
	if title == "" {
		title = "Tidal track"
	}
	var b strings.Builder
	b.WriteString(`<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`)
	b.WriteString(`<item id="0" parentID="-1" restricted="1">`)
	b.WriteString(`<dc:title>` + xmlEscape(title) + `</dc:title>`)
	if m.Artist != "" {
		b.WriteString(`<dc:creator>` + xmlEscape(m.Artist) + `</dc:creator>`)
		b.WriteString(`<upnp:artist>` + xmlEscape(m.Artist) + `</upnp:artist>`)
	}
	if m.Album != "" {
		b.WriteString(`<upnp:album>` + xmlEscape(m.Album) + `</upnp:album>`)
	}
	b.WriteString(`<upnp:class>object.item.audioItem.musicTrack</upnp:class>`)
	b.WriteString(`<res protocolInfo="` + xmlEscape(protocolInfo(m.MimeType)) + `"`)
	if m.Duration > 0 {
		b.WriteString(` duration="` + clock(m.Duration) + `"`)
	}
	b.WriteString(`>` + xmlEscape(m.URL) + `</res>`)
	b.WriteString(`</item></DIDL-Lite>`)
	return b.String()
}

// clock renders H:MM:SS.mmm for the DIDL duration attribute.
func clock(d time.Duration) string {
	h := int(d / time.Hour)
	m := int(d/time.Minute) % 60
	s := int(d/time.Second) % 60
	ms := int(d/time.Millisecond) % 1000
	return fmt.Sprintf("%d:%02d:%02d.%03d", h, m, s, ms)
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// soapArg keeps argument order, which some renderers care about.
type soapArg struct{ Name, Value string }

// soapEnvelope builds the request body for an AVTransport action.
func soapEnvelope(action string, args []soapArg) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	b.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	b.WriteString(`<u:` + action + ` xmlns:u="` + avTransportURN + `">`)
	for _, a := range args {
		b.WriteString(`<` + a.Name + `>` + xmlEscape(a.Value) + `</` + a.Name + `>`)
	}
	b.WriteString(`</u:` + action + `></s:Body></s:Envelope>`)
	return b.String()
}

func (s *speaker) soap(ctx context.Context, action string, args []soapArg) (string, error) {
	body := soapEnvelope(action, args)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ControlURL, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"`+avTransportURN+`#`+action+`"`)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: %w", action, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return string(out), fmt.Errorf("%s: HTTP %d: %s", action, resp.StatusCode, snippet(out))
	}
	return string(out), nil
}

// setVolume posts <volume>N</volume> to the Bose web API.
func (s *speaker) setVolume(ctx context.Context, v int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.WebBase, "/")+"/volume",
		strings.NewReader("<volume>"+strconv.Itoa(v)+"</volume>"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("volume: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("volume: HTTP %d", resp.StatusCode)
	}
	return nil
}

// xmlField pulls <name>value</name> out of a SOAP response without a full
// schema; the AVTransport answers are flat.
func xmlField(doc, name string) string {
	open := "<" + name + ">"
	i := strings.Index(doc, open)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(open):]
	j := strings.Index(rest, "</"+name+">")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// playReport is what `play` prints.
type playReport struct {
	ReachedPlaying bool
	FinalState     string
	Status         string
	Positions      []string
}

// playDirect runs the measured sequence: volume 5, SetAVTransportURI with
// DIDL, Play, wait for PLAYING, volume 5 again, then sample RelTime twice to
// see whether the position advances.
func (s *speaker) playDirect(ctx context.Context, m didlMeta, waitPlaying, sampleGap time.Duration) (*playReport, error) {
	if err := s.setVolume(ctx, testVolume); err != nil {
		return nil, fmt.Errorf("pre-play volume: %w", err)
	}
	slog.Info("volume set before play", "volume", testVolume)
	didl := buildDIDL(m)
	if _, err := s.soap(ctx, "SetAVTransportURI", []soapArg{
		{"InstanceID", "0"},
		{"CurrentURI", m.URL},
		{"CurrentURIMetaData", didl},
	}); err != nil {
		return nil, err
	}
	slog.Info("SetAVTransportURI accepted", "protocolInfo", protocolInfo(m.MimeType))
	if _, err := s.soap(ctx, "Play", []soapArg{{"InstanceID", "0"}, {"Speed", "1"}}); err != nil {
		return nil, err
	}
	slog.Info("Play accepted")

	rep := &playReport{}
	every := s.PollEvery
	if every <= 0 {
		every = 500 * time.Millisecond
	}
	for waited := time.Duration(0); waited <= waitPlaying; waited += every {
		resp, err := s.soap(ctx, "GetTransportInfo", []soapArg{{"InstanceID", "0"}})
		if err == nil {
			rep.FinalState = xmlField(resp, "CurrentTransportState")
			rep.Status = xmlField(resp, "CurrentTransportStatus")
			if rep.FinalState == "PLAYING" {
				rep.ReachedPlaying = true
				break
			}
		} else {
			slog.Warn("GetTransportInfo failed", "err", err)
		}
		if err := s.sleep(ctx, every); err != nil {
			return rep, err
		}
	}
	// Re-assert the test volume whether or not PLAYING was seen: a source
	// switch can restore the last volume on this firmware.
	if err := s.setVolume(ctx, testVolume); err != nil {
		slog.Warn("re-assert volume failed", "err", err)
	} else {
		slog.Info("volume re-asserted after play", "volume", testVolume)
	}
	for i := 0; i < 2; i++ {
		if i > 0 {
			if err := s.sleep(ctx, sampleGap); err != nil {
				return rep, err
			}
		}
		resp, err := s.soap(ctx, "GetPositionInfo", []soapArg{{"InstanceID", "0"}})
		if err != nil {
			slog.Warn("GetPositionInfo failed", "err", err)
			continue
		}
		rep.Positions = append(rep.Positions, xmlField(resp, "RelTime"))
	}
	return rep, nil
}

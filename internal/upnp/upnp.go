// Package upnp ist ein minimaler UPnP AVTransport Control Point.
// Er reicht um einen Stream URL an einen DLNA Media Renderer (z.B. die
// Bose SoundTouch) zu schicken und Play/Pause/Stop zu steuern.
package upnp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// safeHTTPURL accepts a URL only if its scheme is http or https.
// This is the project's belt-and-braces filter at every outbound
// HTTP call site that takes a URL from a not-strictly-trusted
// source (preset store written by the local user, radio-browser
// search results, playlist auto-discovery). Defense-in-depth: in
// practice we never see other schemes, but a single rogue preset
// with file://, ftp:// or jar:// would otherwise reach Go's stdlib
// HTTP client and become an SSRF vector. CodeQL flagged exactly
// these call sites; the helper makes the policy explicit.
func safeHTTPURL(raw string) error {
	if raw == "" {
		return errors.New("url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("disallowed url scheme %q (only http/https accepted)", u.Scheme)
	}
}

// Renderer haelt die Adresse eines AVTransport Endpoints.
type Renderer struct {
	// ControlURL ist die volle URL des AVTransport Control Endpoints,
	// z.B. http://192.0.2.66:8091/AVTransport/Control
	ControlURL string

	// Client wird fuer alle SOAP Requests verwendet. Wenn nil, http.DefaultClient.
	Client *http.Client
}

// NewBoseRenderer liefert einen Renderer fuer die typische Bose SoundTouch
// UPnP Konfiguration auf Port 8091.
func NewBoseRenderer(host string) *Renderer {
	return &Renderer{
		ControlURL: fmt.Sprintf("http://%s:8091/AVTransport/Control", host),
		Client:     &http.Client{Timeout: 8 * time.Second},
	}
}

// SetURI laedt eine neue Stream URL in den Renderer.
// metaTitle wird im DIDL-Lite Metadata als Track Name eingebettet, iconURL
// wird als upnp:albumArtURI mitgegeben damit die SoundTouch Box das
// Sender Logo in ihrer Now Playing Anzeige zeigt.
//
// iconURL kann eine pipe-separierte Kette von Fallback URLs sein (so
// persistieren wir das im preset.art). Die Box bekommt nur den ersten
// Eintrag — der Rest ist Frontend Fallback Material.
func (r *Renderer) SetURI(ctx context.Context, streamURL, metaTitle, iconURL string) error {
	if idx := strings.Index(iconURL, "|"); idx >= 0 {
		iconURL = iconURL[:idx]
	}
	meta := buildDIDL(streamURL, metaTitle, iconURL)
	body := fmt.Sprintf(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:SetAVTransportURI xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID><CurrentURI>%s</CurrentURI><CurrentURIMetaData>%s</CurrentURIMetaData></u:SetAVTransportURI></s:Body></s:Envelope>`,
		xmlEscape(streamURL), xmlEscape(meta))
	return r.soapCall(ctx, "SetAVTransportURI", body)
}

// Play startet die Wiedergabe.
func (r *Renderer) Play(ctx context.Context) error {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Play xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID><Speed>1</Speed></u:Play></s:Body></s:Envelope>`
	return r.soapCall(ctx, "Play", body)
}

// Pause haelt die Wiedergabe an.
func (r *Renderer) Pause(ctx context.Context) error {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Pause xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID></u:Pause></s:Body></s:Envelope>`
	return r.soapCall(ctx, "Pause", body)
}

// Stop beendet die Wiedergabe.
func (r *Renderer) Stop(ctx context.Context) error {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Stop xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID></u:Stop></s:Body></s:Envelope>`
	return r.soapCall(ctx, "Stop", body)
}

// PlayURL ist eine Convenience Methode: setzt URI und drueckt Play.
// iconURL ist optional; wenn gesetzt wird es als albumArtURI an die Box
// gegeben damit sie das Sender Logo anzeigt.
func (r *Renderer) PlayURL(ctx context.Context, streamURL, title, iconURL string) error {
	resolved, _ := ResolveStreamURL(ctx, streamURL)
	if resolved != "" && resolved != streamURL {
		streamURL = resolved
	}
	if err := r.SetURI(ctx, streamURL, title, iconURL); err != nil {
		return fmt.Errorf("SetURI: %w", err)
	}
	if err := r.Play(ctx); err != nil {
		return fmt.Errorf("Play: %w", err)
	}
	return nil
}

// SetURIMime is SetURI with an explicit DIDL res protocolInfo mime.
func (r *Renderer) SetURIMime(ctx context.Context, streamURL, metaTitle, iconURL, mime string) error {
	if idx := strings.Index(iconURL, "|"); idx >= 0 {
		iconURL = iconURL[:idx]
	}
	meta := buildDIDLMime(streamURL, metaTitle, iconURL, mime)
	body := fmt.Sprintf(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:SetAVTransportURI xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID><CurrentURI>%s</CurrentURI><CurrentURIMetaData>%s</CurrentURIMetaData></u:SetAVTransportURI></s:Body></s:Envelope>`,
		xmlEscape(streamURL), xmlEscape(meta))
	return r.soapCall(ctx, "SetAVTransportURI", body)
}

// PlayURLMime is PlayURL for a stream whose codec mime is known (e.g. the
// Spotify loopback WAV). It advertises the given protocolInfo mime and
// skips ResolveStreamURL, which is for radio playlist/HTTPS quirks and
// would only add a needless reachability probe to a known loopback URL.
func (r *Renderer) PlayURLMime(ctx context.Context, streamURL, title, iconURL, mime string) error {
	if err := r.SetURIMime(ctx, streamURL, title, iconURL, mime); err != nil {
		return fmt.Errorf("SetURI: %w", err)
	}
	if err := r.Play(ctx); err != nil {
		return fmt.Errorf("Play: %w", err)
	}
	return nil
}

// ResolveStreamURL bereitet eine Sender URL fuer die Box vor:
//
//  1. Bei Playlist Endungen (.pls / .m3u) wird die enthaltene Stream
//     URL extrahiert (Box kann Playlist Dateien nicht abspielen).
//  2. Bei HTTPS URLs wird versucht die HTTP Variante zu nutzen wenn
//     sie antwortet. Bose UPnP Renderer hat oft Probleme mit HTTPS
//     Streams (Cert Chain, TLS Version) und liefert dann SOAP 402.
//  3. Sonst die Original URL.
func ResolveStreamURL(ctx context.Context, u string) (string, error) {
	if u == "" {
		return "", nil
	}
	lower := strings.ToLower(u)
	// 1) Playlist Endungen aufloesen
	if strings.Contains(lower, ".pls") || strings.Contains(lower, ".m3u") {
		if resolved := extractStreamFromPlaylist(ctx, u); resolved != "" {
			u = resolved
			lower = strings.ToLower(u)
		}
	}
	// 2) HTTPS → HTTP Fallback wenn der Host auch ueber HTTP antwortet
	if strings.HasPrefix(lower, "https://") {
		httpVar := "http://" + u[len("https://"):]
		if isStreamReachable(ctx, httpVar) {
			return httpVar, nil
		}
	}
	return u, nil
}

// extractStreamFromPlaylist laedt eine .pls / .m3u Datei und gibt die
// erste Stream URL zurueck. Leer wenn nichts gefunden / Fehler.
func extractStreamFromPlaylist(ctx context.Context, u string) string {
	if err := safeHTTPURL(u); err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	for _, line := range strings.Split(string(body[:n]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "file") {
			if i := strings.Index(line, "="); i >= 0 {
				val := strings.TrimSpace(line[i+1:])
				if strings.HasPrefix(val, "http") {
					return val
				}
			}
		}
		if strings.HasPrefix(line, "http") {
			return line
		}
	}
	return ""
}

// isStreamReachable prueft mit GET Range 0-0 ob der Server antwortet.
// HEAD wird von vielen Streaming Servern nicht unterstuetzt.
func isStreamReachable(ctx context.Context, u string) bool {
	if err := safeHTTPURL(u); err != nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", "SoundTouchReborn/1.0")
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 400 || resp.StatusCode == 416
}

func (r *Renderer) soapCall(ctx context.Context, action, body string) error {
	if r.ControlURL == "" {
		return errors.New("ControlURL nicht gesetzt")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.ControlURL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", `text/xml; charset=utf-8`)
	req.Header.Set("SOAPACTION", fmt.Sprintf(`"urn:schemas-upnp-org:service:AVTransport:1#%s"`, action))

	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("soap %s status %d: %s", action, resp.StatusCode, string(respBody))
	}
	return nil
}

// buildDIDL erzeugt das CurrentURIMetaData XML fuer einen Audio Stream.
// DLNA Renderer wollen oft DIDL-Lite mit upnp:class und res protocolInfo um
// den Stream Codec zu erkennen. Wenn iconURL gesetzt ist wird es als
// upnp:albumArtURI eingebettet damit die Box ein Sender Logo zeigt.
func buildDIDL(streamURL, title, iconURL string) string {
	return buildDIDLMime(streamURL, title, iconURL, "audio/mpeg")
}

// buildDIDLMime is buildDIDL with an explicit res protocolInfo mime. Radio
// streams use audio/mpeg; the Spotify loopback stream is a live WAV, so it
// passes audio/wav (the box was verified to play that, see the spotify
// spike).
func buildDIDLMime(streamURL, title, iconURL, mime string) string {
	if title == "" {
		title = "Stream"
	}
	if mime == "" {
		mime = "audio/mpeg"
	}
	art := ""
	if iconURL != "" {
		art = `<upnp:albumArtURI dlna:profileID="JPEG_TN" xmlns:dlna="urn:schemas-dlna-org:metadata-1-0/">` + xmlEscapeAttr(iconURL) + `</upnp:albumArtURI>`
	}
	return `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"><item id="0" parentID="-1" restricted="1"><dc:title>` + xmlEscapeAttr(title) + `</dc:title><upnp:class>object.item.audioItem.musicTrack</upnp:class>` + art + `<res protocolInfo="http-get:*:` + mime + `:*">` + xmlEscapeAttr(streamURL) + `</res></item></DIDL-Lite>`
}

// xmlEscape escaped Sonderzeichen fuer XML Element Content.
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// xmlEscapeAttr ist identisch zu xmlEscape plus Quotes.
func xmlEscapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", `'`, "&apos;")
	return r.Replace(s)
}

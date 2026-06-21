// Package upnp is a minimal UPnP AVTransport control point.
// It is enough to send a stream URL to a DLNA media renderer (e.g. the
// Bose SoundTouch) and to control play/pause/stop.
package upnp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
)

// Renderer holds the address of an AVTransport endpoint.
type Renderer struct {
	// ControlURL is the full URL of the AVTransport control endpoint,
	// e.g. http://192.0.2.66:8091/AVTransport/Control
	ControlURL string

	// Client is used for all SOAP requests. If nil, http.DefaultClient.
	Client *http.Client
}

// NewBoseRenderer returns a Renderer for the typical Bose SoundTouch
// UPnP configuration on port 8091.
func NewBoseRenderer(host string) *Renderer {
	return &Renderer{
		ControlURL: fmt.Sprintf("http://%s:8091/AVTransport/Control", host),
		Client:     &http.Client{Timeout: 8 * time.Second},
	}
}

// SetURI loads a new stream URL into the renderer.
// metaTitle is embedded in the DIDL-Lite metadata as the track name, iconURL
// is passed as upnp:albumArtURI so the SoundTouch box shows the station logo
// in its now-playing display.
//
// iconURL can be a pipe-separated chain of fallback URLs (that is how we
// persist it in preset.art). The box only gets the first entry — the rest is
// frontend fallback material.
func (r *Renderer) SetURI(ctx context.Context, streamURL, metaTitle, iconURL string) error {
	if idx := strings.Index(iconURL, "|"); idx >= 0 {
		iconURL = iconURL[:idx]
	}
	meta := buildDIDL(streamURL, metaTitle, iconURL)
	body := fmt.Sprintf(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:SetAVTransportURI xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID><CurrentURI>%s</CurrentURI><CurrentURIMetaData>%s</CurrentURIMetaData></u:SetAVTransportURI></s:Body></s:Envelope>`,
		xmlEscape(streamURL), xmlEscape(meta))
	return r.soapCall(ctx, "SetAVTransportURI", body)
}

// Play starts playback.
func (r *Renderer) Play(ctx context.Context) error {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Play xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID><Speed>1</Speed></u:Play></s:Body></s:Envelope>`
	return r.soapCall(ctx, "Play", body)
}

// Pause halts playback.
func (r *Renderer) Pause(ctx context.Context) error {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Pause xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID></u:Pause></s:Body></s:Envelope>`
	return r.soapCall(ctx, "Pause", body)
}

// Stop ends playback.
func (r *Renderer) Stop(ctx context.Context) error {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Stop xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID></u:Stop></s:Body></s:Envelope>`
	return r.soapCall(ctx, "Stop", body)
}

// PlayURL is a convenience method: sets the URI and presses Play.
// iconURL is optional; when set it is passed to the box as albumArtURI so
// it shows the station logo.
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

// ResolveStreamURL prepares a station URL for the box:
//
//  1. For playlist extensions (.pls / .m3u) the contained stream URL is
//     extracted (the box cannot play playlist files).
//  2. For HTTPS URLs it tries to use the HTTP variant if it answers. The
//     Bose UPnP renderer often has trouble with HTTPS streams (cert
//     chain, TLS version) and then returns SOAP 402.
//  3. Otherwise the original URL.
func ResolveStreamURL(ctx context.Context, u string) (string, error) {
	if u == "" {
		return "", nil
	}
	lower := strings.ToLower(u)
	// 1) Resolve playlist extensions
	if strings.Contains(lower, ".pls") || strings.Contains(lower, ".m3u") {
		if resolved := extractStreamFromPlaylist(ctx, u); resolved != "" {
			u = resolved
			lower = strings.ToLower(u)
		}
	}
	// 2) HTTPS -> HTTP fallback if the host also answers over HTTP
	if strings.HasPrefix(lower, "https://") {
		httpVar := "http://" + u[len("https://"):]
		if isStreamReachable(ctx, httpVar) {
			return httpVar, nil
		}
	}
	return u, nil
}

// extractStreamFromPlaylist loads a .pls / .m3u file and returns the
// first stream URL. Empty if nothing is found / on error.
func extractStreamFromPlaylist(ctx context.Context, u string) string {
	if err := netutil.SafeHTTPURL(u); err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	client := netutil.GuardedClient(5 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	// Read up to 8 KB with io.ReadAll over a LimitReader, not a single Read: a
	// bare resp.Body.Read can return a short chunk before the line carrying the
	// File1=/stream URL has arrived, which made playlist resolution flaky on
	// servers that flush the header and body in separate TCP segments.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	for _, line := range strings.Split(string(body), "\n") {
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

// isStreamReachable checks with GET Range 0-0 whether the server answers.
// HEAD is not supported by many streaming servers.
func isStreamReachable(ctx context.Context, u string) bool {
	if err := netutil.SafeHTTPURL(u); err != nil {
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
	client := netutil.GuardedClient(4 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 400 || resp.StatusCode == 416
}

func (r *Renderer) soapCall(ctx context.Context, action, body string) error {
	if r.ControlURL == "" {
		return errors.New("ControlURL not set")
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

// buildDIDL builds the CurrentURIMetaData XML for an audio stream.
// DLNA renderers often want DIDL-Lite with upnp:class and res protocolInfo
// to detect the stream codec. If iconURL is set it is embedded as
// upnp:albumArtURI so the box shows a station logo.
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

// xmlEscape escapes special characters for XML element content.
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// xmlEscapeAttr is identical to xmlEscape plus quotes.
func xmlEscapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", `'`, "&apos;")
	return r.Replace(s)
}

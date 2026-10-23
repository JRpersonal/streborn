// The one /now_playing reader.
//
// Before this file every package that wanted to know what the speaker plays
// fetched http://<box>:8090/now_playing by hand and read the answer its own
// way: substring checks over the body in one place, a regex in the next, an
// XML struct in the third. Three parsing styles meant three truth tables for
// the same question, and a body whose track title happened to contain
// "STANDBY" counted as a sleeping speaker in one of them. This file owns the
// fetch, the parse and the verdicts; callers keep their own timeout and their
// own policy for an unreadable speaker (the reader returns an error, the
// caller decides what that means for it).

package boxapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NowPlaying is one read of the speaker's /now_playing: the parsed document
// plus the body it was parsed from, so a caller that needs the firmware's own
// XML (the /api/status proxy, the station-name annotation) has it without a
// second request.
type NowPlaying struct {
	// Attributes of the <nowPlaying> root.
	DeviceID      string
	Source        string // "UPNP", "STANDBY", "INVALID_SOURCE", "SETUP", ...
	SourceAccount string

	ContentItem ContentItem

	Track       string
	Artist      string
	Album       string
	StationName string
	Art         string // the <art> URL
	ArtStatus   string // the artImageStatus attribute, e.g. "IMAGE_PRESENT"
	// PlayStatus is the <playStatus> element verbatim; empty when the document
	// carries none (a speaker that just left standby, a source teardown in
	// progress). Status() adds the legacy fallback scan.
	PlayStatus string
	// Position and Total come from <time total="236">25</time>; HasTime says
	// whether the element was there at all.
	Position, Total time.Duration
	HasTime         bool

	// Raw is the body exactly as the speaker sent it (no UTF-8 repair, no
	// entity decoding), capped at nowPlayingBodyLimit.
	Raw []byte
	// Parsed is false when the body was not well-formed XML (a truncated or
	// malformed document). The fields above are then salvaged by scanning Raw
	// the way the old substring readers did, so every verdict still answers.
	Parsed bool
}

// ContentItem is the <ContentItem> the speaker is tuned to.
type ContentItem struct {
	Source        string
	SourceAccount string
	Location      string // entity-decoded, so "&amp;" in the XML is "&" here
	Type          string
	IsPresetable  bool
	ItemName      string
	ContainerArt  string
}

// nowPlayingBodyLimit caps the body read. The largest documents seen in the
// field (a native station with a base64 descriptor, Spotify with artwork) are
// a few KB; the old readers cut at 8 KB and silently truncated anything
// longer, which is why the cap here matches getXML's generous one.
const nowPlayingBodyLimit = 256 * 1024

// NowPlaying reads and parses the speaker's /now_playing once. The deadline
// is the caller's (callCtx supplies the 6 s default for a context without
// one). A non-200 answer is a *BoxError, so a caller that must serve the
// firmware's own error body through can still reach it.
func (c *Client) NowPlaying(ctx context.Context) (*NowPlaying, error) {
	raw, err := c.GetRaw(ctx, "/now_playing")
	if err != nil {
		return nil, err
	}
	return ParseNowPlaying(raw), nil
}

// GetRaw reads a :8090 endpoint and returns the body verbatim, for the
// documents a caller parses itself (/sources and /presets feed
// presets.NativeSourceAccounts and the preset reconcile, which want the
// speaker's bytes, not a struct). The deadline is the caller's; a non-200
// answer is a *BoxError carrying the body.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := callCtx(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, nowPlayingBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, newBoxError(path, resp.StatusCode, body)
	}
	return body, nil
}

// nowPlayingXML is the wire shape; NowPlaying is the public one so the
// xml tags and the attribute string booleans stay out of callers' sight.
type nowPlayingXML struct {
	DeviceID      string `xml:"deviceID,attr"`
	Source        string `xml:"source,attr"`
	SourceAccount string `xml:"sourceAccount,attr"`
	ContentItem   struct {
		Source        string `xml:"source,attr"`
		SourceAccount string `xml:"sourceAccount,attr"`
		Location      string `xml:"location,attr"`
		Type          string `xml:"type,attr"`
		IsPresetable  string `xml:"isPresetable,attr"`
		ItemName      string `xml:"itemName"`
		ContainerArt  string `xml:"containerArt"`
	} `xml:"ContentItem"`
	Track       string `xml:"track"`
	Artist      string `xml:"artist"`
	Album       string `xml:"album"`
	StationName string `xml:"stationName"`
	Art         struct {
		Status string `xml:"artImageStatus,attr"`
		URL    string `xml:",chardata"`
	} `xml:"art"`
	Time *struct {
		Total    string `xml:"total,attr"`
		Position string `xml:",chardata"`
	} `xml:"time"`
	PlayStatus string `xml:"playStatus"`
}

// Legacy scans, kept for the salvage path and for the verdicts that read
// across the whole document on purpose (Locations).
var (
	reRootSource   = regexp.MustCompile(`<nowPlaying[^>]*\bsource="([^"]*)"`)
	reAnySource    = regexp.MustCompile(`\bsource="([^"]*)"`)
	reAnyLocation  = regexp.MustCompile(`\blocation="([^"]*)"`)
	rePlayStatusEl = regexp.MustCompile(`<playStatus>([^<]+)</playStatus>`)
	reItemNameEl   = regexp.MustCompile(`<itemName>([^<]*)</itemName>`)
)

// ParseNowPlaying turns a /now_playing body into a NowPlaying. It never
// fails: a body that is not well-formed XML gets its fields salvaged by the
// same substring scans the old readers used (Parsed reports which path ran),
// so a verdict over a truncated document is the verdict it always was.
func ParseNowPlaying(raw []byte) *NowPlaying {
	np := &NowPlaying{Raw: raw}
	var doc nowPlayingXML
	if err := xml.Unmarshal(ensureUTF8(raw), &doc); err != nil {
		np.salvage()
		return np
	}
	np.Parsed = true
	np.DeviceID = doc.DeviceID
	np.Source = doc.Source
	np.SourceAccount = doc.SourceAccount
	np.ContentItem = ContentItem{
		Source:        doc.ContentItem.Source,
		SourceAccount: doc.ContentItem.SourceAccount,
		Location:      doc.ContentItem.Location,
		Type:          doc.ContentItem.Type,
		IsPresetable:  strings.EqualFold(doc.ContentItem.IsPresetable, "true"),
		ItemName:      doc.ContentItem.ItemName,
		ContainerArt:  strings.TrimSpace(doc.ContentItem.ContainerArt),
	}
	np.Track = doc.Track
	np.Artist = doc.Artist
	np.Album = doc.Album
	np.StationName = doc.StationName
	np.Art = strings.TrimSpace(doc.Art.URL)
	np.ArtStatus = doc.Art.Status
	np.PlayStatus = strings.TrimSpace(doc.PlayStatus)
	if doc.Time != nil {
		np.HasTime = true
		if t, err := strconv.Atoi(strings.TrimSpace(doc.Time.Total)); err == nil {
			np.Total = time.Duration(t) * time.Second
		}
		if p, err := strconv.Atoi(strings.TrimSpace(doc.Time.Position)); err == nil {
			np.Position = time.Duration(p) * time.Second
		}
	}
	return np
}

// salvage fills the fields from a body xml.Unmarshal refused, with the scans
// the substring readers used: root source attribute, first location
// attribute, first itemName and playStatus element. Nothing is decoded.
func (np *NowPlaying) salvage() {
	body := string(np.Raw)
	if m := reRootSource.FindStringSubmatch(body); m != nil {
		np.Source = m[1]
	} else if m := reAnySource.FindStringSubmatch(body); m != nil {
		np.Source = m[1]
	}
	if m := reAnyLocation.FindStringSubmatch(body); m != nil {
		np.ContentItem.Location = m[1]
	}
	if m := reItemNameEl.FindStringSubmatch(body); m != nil {
		np.ContentItem.ItemName = m[1]
	}
	if m := rePlayStatusEl.FindStringSubmatch(body); m != nil {
		np.PlayStatus = m[1]
	}
}

// Body returns the raw document as a string, for the callers that log or
// regex it.
func (np *NowPlaying) Body() string { return string(np.Raw) }

// Status is the play status: the <playStatus> element when the document has
// one, otherwise a scan of the body for a state token in the order
// PLAY, BUFFERING, PAUSE, STOP (some firmwares put the status on an
// attribute instead of the element). Empty when the speaker reported none.
func (np *NowPlaying) Status() string {
	if np.PlayStatus != "" {
		return np.PlayStatus
	}
	body := string(np.Raw)
	switch {
	case strings.Contains(body, "PLAY_STATE"):
		return "PLAY_STATE"
	case strings.Contains(body, "BUFFERING_STATE"):
		return "BUFFERING_STATE"
	case strings.Contains(body, "PAUSE_STATE"):
		return "PAUSE_STATE"
	case strings.Contains(body, "STOP_STATE"):
		return "STOP_STATE"
	}
	return ""
}

// IsStandby reports whether the speaker says it is off: the root source is
// STANDBY (some firmwares NETWORK_STANDBY). Read off the source attribute,
// not the whole body, so a station or track literally named "STANDBY" cannot
// be mistaken for a sleeping speaker.
func (np *NowPlaying) IsStandby() bool { return strings.Contains(np.Source, "STANDBY") }

// SourceTornDown reports whether the firmware dropped the source it was
// playing (INVALID_SOURCE): its answer to a starved or rejected stream. Such
// a document usually carries no <playStatus> at all.
func (np *NowPlaying) SourceTornDown() bool { return strings.Contains(np.Source, "INVALID_SOURCE") }

// IsPlaying reports a play or buffering state on any source: audio is
// flowing or about to.
func (np *NowPlaying) IsPlaying() bool {
	switch np.Status() {
	case "PLAY_STATE", "BUFFERING_STATE":
		return true
	}
	return false
}

// IsBuffering reports BUFFERING_STATE specifically. A speaker retrying a
// file it cannot read buffers forever, which is not recovery.
func (np *NowPlaying) IsBuffering() bool { return np.Status() == "BUFFERING_STATE" }

// IsPaused reports PAUSE_STATE.
func (np *NowPlaying) IsPaused() bool { return np.Status() == "PAUSE_STATE" }

// IsBusy reports whether the speaker is playing, buffering or paused: the
// states in which a push would interrupt somebody.
func (np *NowPlaying) IsBusy() bool { return np.IsPlaying() || np.IsPaused() }

// Location is the ContentItem location the speaker is tuned to, entity
// decoded. Empty when the document carries none.
func (np *NowPlaying) Location() string { return np.ContentItem.Location }

// Locations returns every location attribute in the document, in document
// order and undecoded. A foreign controller's selection can sit in a second
// element; the foreign-content guard wants all of them.
func (np *NowPlaying) Locations() []string {
	ms := reAnyLocation.FindAllStringSubmatch(string(np.Raw), -1)
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m[1])
	}
	return out
}

// IsURL reports whether the speaker is in a play or buffering state AND its
// location points at wantURL, the stream a recall pushed. The location check
// is what a bare play-state check misses: a speaker that rejects the recall
// (1036 wrong-state, chronic on the Wave) keeps reporting the PREVIOUS
// stream's play state while never fetching the new one.
//
// Compared on the stream path ("/stream/5"), not the whole URL: the speaker
// echoes the location it was given, but host spellings differ across the
// paths that build these URLs (loopback vs the speaker's LAN address).
// Deliberately forgiving in one direction: a document with no location at
// all, or a wantURL that is not an STR stream, falls back to the plain
// play-state verdict so a model that does not report a location never ends
// up in an endless re-push loop.
func (np *NowPlaying) IsURL(wantURL string) bool {
	if !np.IsPlaying() {
		return false
	}
	want := StreamPath(wantURL)
	if want == "" || !strings.Contains(string(np.Raw), `location="`) {
		return true
	}
	return strings.Contains(np.ContentItem.Location, want)
}

// OnSpotifyStream reports whether the speaker is pointed at STR's Spotify
// Ogg stream, whatever its play state: a paused Spotify playlist still wants
// its Next/Prev keys routed to the engine.
func (np *NowPlaying) OnSpotifyStream() bool {
	return strings.Contains(np.ContentItem.Location, "spotify/stream")
}

// IsSpotify reports STR's Spotify stream in a play or buffering state: the
// recall verify's success signal, where a bare play-state check is fooled by
// the speaker bouncing off to the previous radio preset.
func (np *NowPlaying) IsSpotify() bool { return np.OnSpotifyStream() && np.IsPlaying() }

// ReallySpotify is the strict form of IsSpotify: the Spotify stream in
// PLAY_STATE proper, not merely BUFFERING. Re-pointing a speaker that is
// only buffering restarts the track.
func (np *NowPlaying) ReallySpotify() bool {
	return np.OnSpotifyStream() && np.Status() == "PLAY_STATE"
}

// Summary is the compact evidence the recall settle logs want: the source,
// the speaker's own itemName (what a display model shows) and the
// <playStatus> element verbatim (empty when there is none, no fallback scan).
func (np *NowPlaying) Summary() (source, itemName, playStatus string) {
	return np.Source, np.ContentItem.ItemName, np.PlayStatus
}

// StreamPath reduces an STR stream URL to the path+query the speaker echoes
// back ("http://127.0.0.1:8888/stream/5" -> "/stream/5"), so a location
// comparison does not depend on which host spelling built the URL. Returns
// "" for anything that is not an STR stream URL, which disables the
// comparison.
func StreamPath(u string) string {
	i := strings.Index(u, "/stream/")
	if i < 0 {
		return ""
	}
	return u[i:]
}

// String is a one-line description for logs: source, status, location.
func (np *NowPlaying) String() string {
	return fmt.Sprintf("source=%s status=%s location=%s", np.Source, np.Status(), np.ContentItem.Location)
}

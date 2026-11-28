package main

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Manifest MIME types playbackinfo answers with.
const (
	manifestBTS  = "application/vnd.tidal.bts"
	manifestDASH = "application/dash+xml"
)

// playbackInfo is the playbackinfopostpaywall response. The manifest is
// base64; its MIME type says whether it is Tidal's BTS JSON or a DASH MPD.
type playbackInfo struct {
	TrackID           int64  `json:"trackId"`
	AssetPresentation string `json:"assetPresentation"`
	AudioMode         string `json:"audioMode"`
	AudioQuality      string `json:"audioQuality"`
	ManifestMimeType  string `json:"manifestMimeType"`
	ManifestHash      string `json:"manifestHash"`
	Manifest          string `json:"manifest"`
	BitDepth          int    `json:"bitDepth"`
	SampleRate        int    `json:"sampleRate"`
}

// streamInfo is the decoded, renderer-relevant view of a manifest.
type streamInfo struct {
	Kind       string // "BTS" or "DASH"
	MimeType   string // the media MIME type the manifest declares
	Codecs     string // as declared, e.g. "flac", "mp4a.40.2"
	Container  string // what the bytes on the wire are
	Encrypted  bool
	Encryption string // encryptionType (BTS) or ContentProtection scheme (DASH)
	// URLs holds the BTS direct URL(s), or for DASH the init segment followed
	// by every media segment.
	URLs         []string
	SegmentCount int // DASH media segments, without the init segment
	Bandwidth    int
	SampleRate   int
	Duration     time.Duration
	Raw          string // decoded manifest text, for --dump
}

// directURL is the one URL a UPnP renderer could be handed as is: a BTS
// manifest with a single URL. DASH never qualifies.
func (s *streamInfo) directURL() (string, bool) {
	if s.Kind == "BTS" && len(s.URLs) == 1 {
		return s.URLs[0], true
	}
	return "", false
}

// verdict is the one-line answer the spike exists for.
func (s *streamInfo) verdict() string {
	switch {
	case s.Encrypted:
		return "ENCRYPTED: no plain URL, would need a DRM path (out of scope)"
	case s.Kind == "BTS" && len(s.URLs) == 1:
		return "single plain " + s.Container + " URL: hand it straight to the speaker (UPnP)"
	case s.Kind == "BTS":
		return fmt.Sprintf("%d plain URLs: would need concatenation in a stream proxy", len(s.URLs))
	default:
		return fmt.Sprintf("DASH, %s in %d segments: needs a segment joiner (and a remux if the speaker rejects fMP4) in a stream proxy", s.Container, s.SegmentCount)
	}
}

// decodeManifest base64-decodes pi.Manifest and parses it per its MIME type.
func decodeManifest(pi *playbackInfo) (*streamInfo, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pi.Manifest))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(pi.Manifest))
		if err != nil {
			return nil, fmt.Errorf("manifest is not base64: %w", err)
		}
	}
	var si *streamInfo
	switch {
	case strings.Contains(pi.ManifestMimeType, manifestBTS):
		si, err = decodeBTS(raw)
	case strings.Contains(pi.ManifestMimeType, manifestDASH):
		si, err = decodeDASH(raw)
	default:
		return nil, fmt.Errorf("unknown manifestMimeType %q", pi.ManifestMimeType)
	}
	if err != nil {
		return nil, err
	}
	if si.SampleRate == 0 {
		si.SampleRate = pi.SampleRate
	}
	si.Raw = string(raw)
	return si, nil
}

// btsManifest is Tidal's own JSON manifest for progressive streams.
type btsManifest struct {
	MimeType       string   `json:"mimeType"`
	Codecs         string   `json:"codecs"`
	EncryptionType string   `json:"encryptionType"`
	KeyID          string   `json:"keyId"`
	URLs           []string `json:"urls"`
}

func decodeBTS(raw []byte) (*streamInfo, error) {
	var m btsManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("BTS manifest: %w", err)
	}
	if len(m.URLs) == 0 {
		return nil, errors.New("BTS manifest without urls")
	}
	enc := m.EncryptionType
	if enc == "" {
		enc = "NONE"
	}
	return &streamInfo{
		Kind:       "BTS",
		MimeType:   m.MimeType,
		Codecs:     m.Codecs,
		Container:  containerFor(m.MimeType, m.Codecs, false),
		Encrypted:  !strings.EqualFold(enc, "NONE") || m.KeyID != "",
		Encryption: enc,
		URLs:       m.URLs,
	}, nil
}

// MPD subset. encoding/xml matches element names in any namespace when the
// tag carries none, so the urn:mpeg:dash:schema:mpd:2011 default namespace
// needs no special handling.
type mpdDoc struct {
	XMLName  xml.Name    `xml:"MPD"`
	Duration string      `xml:"mediaPresentationDuration,attr"`
	Periods  []mpdPeriod `xml:"Period"`
}

type mpdPeriod struct {
	AdaptationSets []mpdAdaptation `xml:"AdaptationSet"`
}

type mpdAdaptation struct {
	MimeType          string           `xml:"mimeType,attr"`
	ContentType       string           `xml:"contentType,attr"`
	Codecs            string           `xml:"codecs,attr"`
	ContentProtection []mpdProtection  `xml:"ContentProtection"`
	SegmentTemplate   *mpdTemplate     `xml:"SegmentTemplate"`
	Representations   []mpdRepresentat `xml:"Representation"`
}

type mpdRepresentat struct {
	ID                string          `xml:"id,attr"`
	MimeType          string          `xml:"mimeType,attr"`
	Codecs            string          `xml:"codecs,attr"`
	Bandwidth         int             `xml:"bandwidth,attr"`
	AudioSamplingRate string          `xml:"audioSamplingRate,attr"`
	ContentProtection []mpdProtection `xml:"ContentProtection"`
	SegmentTemplate   *mpdTemplate    `xml:"SegmentTemplate"`
}

type mpdProtection struct {
	SchemeIDURI string `xml:"schemeIdUri,attr"`
	Value       string `xml:"value,attr"`
}

type mpdTemplate struct {
	Timescale      int    `xml:"timescale,attr"`
	Initialization string `xml:"initialization,attr"`
	Media          string `xml:"media,attr"`
	StartNumber    *int   `xml:"startNumber,attr"`
	Timeline       *struct {
		S []struct {
			T *int64 `xml:"t,attr"`
			D int64  `xml:"d,attr"`
			R int    `xml:"r,attr"`
		} `xml:"S"`
	} `xml:"SegmentTimeline"`
}

func decodeDASH(raw []byte) (*streamInfo, error) {
	var doc mpdDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("DASH manifest: %w", err)
	}
	if len(doc.Periods) == 0 || len(doc.Periods[0].AdaptationSets) == 0 {
		return nil, errors.New("DASH manifest without Period/AdaptationSet")
	}
	as := doc.Periods[0].AdaptationSets[0]
	if len(as.Representations) == 0 {
		return nil, errors.New("DASH manifest without Representation")
	}
	// Highest bandwidth wins; that is the quality that was asked for.
	rep := as.Representations[0]
	for _, r := range as.Representations[1:] {
		if r.Bandwidth > rep.Bandwidth {
			rep = r
		}
	}
	tmpl := rep.SegmentTemplate
	if tmpl == nil {
		tmpl = as.SegmentTemplate
	}
	if tmpl == nil {
		return nil, errors.New("DASH manifest without SegmentTemplate")
	}
	mime := firstNonEmpty(rep.MimeType, as.MimeType)
	codecs := firstNonEmpty(rep.Codecs, as.Codecs)
	dur := parseISODuration(doc.Duration)

	count := 0
	if tmpl.Timeline != nil {
		var total int64
		for _, s := range tmpl.Timeline.S {
			r := s.R
			if r < 0 && s.D > 0 && tmpl.Timescale > 0 && dur > 0 {
				// r=-1: repeat until the end of the period.
				left := dur.Seconds()*float64(tmpl.Timescale) - float64(total)
				r = int(math.Ceil(left/float64(s.D))) - 1
			}
			if r < 0 {
				r = 0
			}
			count += 1 + r
			total += int64(1+r) * s.D
		}
	}
	start := 1
	if tmpl.StartNumber != nil {
		start = *tmpl.StartNumber
	}
	fill := func(t string, n int) string {
		t = strings.ReplaceAll(t, "$RepresentationID$", rep.ID)
		t = strings.ReplaceAll(t, "$Bandwidth$", strconv.Itoa(rep.Bandwidth))
		return strings.ReplaceAll(t, "$Number$", strconv.Itoa(n))
	}
	var urls []string
	if tmpl.Initialization != "" {
		urls = append(urls, fill(tmpl.Initialization, 0))
	}
	for i := 0; i < count; i++ {
		urls = append(urls, fill(tmpl.Media, start+i))
	}

	var schemes []string
	for _, p := range append(append([]mpdProtection{}, as.ContentProtection...), rep.ContentProtection...) {
		schemes = append(schemes, firstNonEmpty(p.Value, p.SchemeIDURI))
	}
	enc := "NONE"
	if len(schemes) > 0 {
		enc = strings.Join(schemes, ", ")
	}
	sr, _ := strconv.Atoi(rep.AudioSamplingRate)
	return &streamInfo{
		Kind:         "DASH",
		MimeType:     mime,
		Codecs:       codecs,
		Container:    containerFor(mime, codecs, true),
		Encrypted:    len(schemes) > 0,
		Encryption:   enc,
		URLs:         urls,
		SegmentCount: count,
		Bandwidth:    rep.Bandwidth,
		SampleRate:   sr,
		Duration:     dur,
	}, nil
}

// containerFor names what is on the wire. The question the spike answers is
// exactly this: does LOSSLESS arrive as raw FLAC or as FLAC inside MP4.
func containerFor(mime, codecs string, dash bool) string {
	m := strings.ToLower(mime)
	c := strings.ToLower(codecs)
	inner := ""
	switch {
	case strings.Contains(c, "flac"):
		inner = "FLAC"
	case strings.HasPrefix(c, "mp4a"), c == "aac":
		inner = "AAC"
	case strings.Contains(c, "mp3"):
		inner = "MP3"
	case strings.Contains(c, "eac3"), strings.Contains(c, "ec-3"):
		inner = "E-AC-3"
	case strings.Contains(c, "ac-4"), strings.Contains(c, "ac4"):
		inner = "AC-4"
	}
	switch m {
	case "audio/flac", "audio/x-flac":
		return "raw FLAC"
	case "audio/mpeg", "audio/mp3":
		return "MP3"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		if dash {
			return inner + "-in-fMP4"
		}
		return inner + "-in-MP4"
	case "audio/aac":
		return "ADTS AAC"
	}
	if inner != "" {
		return inner + " in " + mime
	}
	return mime
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// isoDuration matches the PT#H#M#S subset MPDs use.
var isoDuration = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:([\d.]+)S)?)?$`)

func parseISODuration(s string) time.Duration {
	m := isoDuration.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	var d time.Duration
	if v, err := strconv.Atoi(m[1]); err == nil {
		d += time.Duration(v) * 24 * time.Hour
	}
	if v, err := strconv.Atoi(m[2]); err == nil {
		d += time.Duration(v) * time.Hour
	}
	if v, err := strconv.Atoi(m[3]); err == nil {
		d += time.Duration(v) * time.Minute
	}
	if v, err := strconv.ParseFloat(m[4], 64); err == nil {
		d += time.Duration(v * float64(time.Second))
	}
	return d
}

// unixInToken finds an "exp=<unix>" or "Expires=<unix>"-style value inside a
// CDN token string.
var unixInToken = regexp.MustCompile(`(?i)(?:^|[~&;,_])(?:exp|expires)=(\d{9,11})`)

// leadingUnix matches tokens of the form "<unix>~<signature>".
var leadingUnix = regexp.MustCompile(`^(\d{10})~`)

// urlExpiry looks for an expiry timestamp in a signed media URL. It knows the
// usual CDN shapes (Expires=, exp= inside an Akamai-style token, a
// "<unix>~sig" token). ok is false when nothing recognisable is present,
// which is itself a finding.
func urlExpiry(raw string) (t time.Time, where string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return time.Time{}, "", false
	}
	plausible := func(n int64) bool { return n > 1_000_000_000 && n < 100_000_000_000 }
	for key, vals := range u.Query() {
		for _, v := range vals {
			k := strings.ToLower(key)
			if k == "expires" || k == "exp" || k == "e" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && plausible(n) {
					return time.Unix(n, 0).UTC(), "query " + key, true
				}
			}
			if m := unixInToken.FindStringSubmatch(v); m != nil {
				if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && plausible(n) {
					return time.Unix(n, 0).UTC(), "token in query " + key, true
				}
			}
			if m := leadingUnix.FindStringSubmatch(v); m != nil {
				if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && plausible(n) {
					return time.Unix(n, 0).UTC(), "leading timestamp in query " + key, true
				}
			}
		}
	}
	if m := unixInToken.FindStringSubmatch(u.Path); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && plausible(n) {
			return time.Unix(n, 0).UTC(), "path token", true
		}
	}
	return time.Time{}, "", false
}

// redactURL keeps scheme, host and the first path segment, and drops the
// signed query, so terminal output can be pasted into an issue.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable URL>"
	}
	path := u.Path
	if i := strings.Index(strings.TrimPrefix(path, "/"), "/"); i >= 0 {
		path = path[:i+1] + "/..."
	}
	q := ""
	if u.RawQuery != "" {
		q = "?<" + strconv.Itoa(len(u.Query())) + " params redacted>"
	}
	return u.Scheme + "://" + u.Host + path + q
}

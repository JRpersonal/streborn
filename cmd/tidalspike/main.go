// Command tidalspike is a throwaway measurement tool for deciding on a Tidal
// source in STR. It is not part of the agent or the desktop app and is never
// shipped. It answers: does the device login work with the unofficial API,
// which container does each quality deliver today, are the URLs plain and
// how long do they live, and does a SoundTouch play one over UPnP.
//
// See README.md next to this file for setup and the command sequence.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"
)

var qualities = []string{"LOW", "HIGH", "LOSSLESS", "HI_RES_LOSSLESS"}

func main() {
	level := slog.LevelInfo
	if os.Getenv("TIDALSPIKE_DEBUG") != "" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout))
}

const usage = `tidalspike: Tidal measurement spike for STR (not shipped)

usage:
  tidalspike login                      device-code login, saves tokens
  tidalspike refresh                    force a token refresh
  tidalspike whoami                     user id, country, subscription
  tidalspike playlists                  list your playlists
  tidalspike track <id> [--quality Q|all] [--probe] [--show-urls] [--dump FILE]
  tidalspike play <id> --speaker <ip> [--quality Q] [--dry-run]

Q is LOW, HIGH, LOSSLESS or HI_RES_LOSSLESS.
env: TIDAL_CLIENT_ID, TIDAL_CLIENT_SECRET (required), TIDALSPIKE_TEST_SPEAKER
(play interlock), TIDALSPIKE_TOKEN_FILE, TIDAL_CLIENT_VERSION, TIDALSPIKE_DEBUG
`

func run(ctx context.Context, args []string, out io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "login":
		err = cmdLogin(ctx, out)
	case "refresh":
		err = cmdRefresh(ctx, out)
	case "whoami":
		err = cmdWhoami(ctx, out)
	case "playlists":
		err = cmdPlaylists(ctx, out)
	case "track":
		err = cmdTrack(ctx, args[1:], out)
	case "play":
		err = cmdPlay(ctx, args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return 0
	default:
		fmt.Fprintf(out, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		slog.Error(args[0]+" failed", "err", err)
		return 1
	}
	return 0
}

func oauthFromEnv() *oauth {
	return &oauth{
		Base:         envOr("TIDALSPIKE_AUTH_BASE", defaultAuthBase),
		ClientID:     os.Getenv("TIDAL_CLIENT_ID"),
		ClientSecret: os.Getenv("TIDAL_CLIENT_SECRET"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loggedInClient loads the stored tokens and builds an API client that
// refreshes and re-saves them on demand.
func loggedInClient() (*apiClient, error) {
	path, err := defaultTokenPath()
	if err != nil {
		return nil, err
	}
	ts, err := loadTokens(path)
	if err != nil {
		return nil, err
	}
	return &apiClient{
		Base:          envOr("TIDALSPIKE_API_BASE", defaultAPIBase),
		OAuth:         oauthFromEnv(),
		Tokens:        ts,
		TokenPath:     path,
		ClientVersion: os.Getenv("TIDAL_CLIENT_VERSION"),
	}, nil
}

func cmdLogin(ctx context.Context, out io.Writer) error {
	o := oauthFromEnv()
	if o.ClientID == "" {
		return errors.New("set TIDAL_CLIENT_ID (and TIDAL_CLIENT_SECRET), see README.md")
	}
	path, err := defaultTokenPath()
	if err != nil {
		return err
	}
	da, err := o.startDevice(ctx)
	if err != nil {
		return err
	}
	link := da.VerificationURIComplete
	if link == "" {
		link = da.VerificationURI
	}
	if link != "" && !strings.Contains(link, "://") {
		link = "https://" + link
	}
	fmt.Fprintf(out, "Open %s and enter code %s (valid %.0f s). Waiting...\n", link, da.UserCode, da.ExpiresIn)
	ts, err := o.pollDevice(ctx, da)
	if err != nil {
		return err
	}
	c := &apiClient{Base: envOr("TIDALSPIKE_API_BASE", defaultAPIBase), OAuth: o, Tokens: ts, ClientVersion: os.Getenv("TIDAL_CLIENT_VERSION")}
	if s, err := c.session(ctx); err == nil {
		ts.UserID = s.UserID
		ts.CountryCode = s.CountryCode
	} else {
		slog.Warn("GET sessions after login failed, keeping token-response values", "err", err)
	}
	if err := saveTokens(path, ts); err != nil {
		return err
	}
	fmt.Fprintf(out, "Logged in: user %d, country %s, token valid until %s\nTokens saved to %s\n",
		ts.UserID, ts.CountryCode, ts.ExpiresAt.Local().Format(time.RFC3339), path)
	return nil
}

func cmdRefresh(ctx context.Context, out io.Writer) error {
	c, err := loggedInClient()
	if err != nil {
		return err
	}
	if err := c.forceRefresh(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "Refreshed, access token valid until %s\n", c.Tokens.ExpiresAt.Local().Format(time.RFC3339))
	return nil
}

func cmdWhoami(ctx context.Context, out io.Writer) error {
	c, err := loggedInClient()
	if err != nil {
		return err
	}
	s, err := c.session(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "user id:      %d\ncountry:      %s\ntoken until:  %s\n",
		s.UserID, s.CountryCode, c.Tokens.ExpiresAt.Local().Format(time.RFC3339))
	sub, err := c.subscription(ctx)
	if err != nil {
		fmt.Fprintf(out, "subscription: unavailable (%v)\n", err)
		return nil
	}
	fmt.Fprintf(out, "subscription: %s (%s), highest quality %s\n", sub.Subscription.Type, sub.Status, sub.HighestSoundQuality)
	return nil
}

func cmdPlaylists(ctx context.Context, out io.Writer) error {
	c, err := loggedInClient()
	if err != nil {
		return err
	}
	pls, err := c.playlists(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTRACKS\tNAME")
	for _, p := range pls {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", p.UUID, p.NumberOfTracks, p.Title)
	}
	_ = tw.Flush()
	fmt.Fprintf(out, "%d playlists\n", len(pls))
	return nil
}

// parseInterleaved lets positionals and flags mix (`track 123 --quality X`
// and `track --quality X 123`), which the flag package alone does not.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func normQuality(q string) (string, error) {
	q = strings.ToUpper(strings.TrimSpace(q))
	for _, k := range qualities {
		if q == k {
			return q, nil
		}
	}
	return "", fmt.Errorf("quality %q: want one of %s", q, strings.Join(qualities, ", "))
}

func cmdTrack(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("track", flag.ContinueOnError)
	quality := fs.String("quality", "LOSSLESS", "LOW|HIGH|LOSSLESS|HI_RES_LOSSLESS|all")
	probe := fs.Bool("probe", false, "range-fetch the first 64 bytes and sniff the real container")
	showURLs := fs.Bool("show-urls", false, "print full signed URLs (do not paste them anywhere)")
	dump := fs.String("dump", "", "write the decoded manifest(s) to this file")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: track <id> [--quality Q|all] [--probe] [--show-urls] [--dump FILE]")
	}
	id := pos[0]
	var want []string
	if strings.EqualFold(*quality, "all") {
		want = qualities
	} else {
		q, err := normQuality(*quality)
		if err != nil {
			return err
		}
		want = []string{q}
	}
	c, err := loggedInClient()
	if err != nil {
		return err
	}
	if meta, err := c.track(ctx, id); err == nil {
		fmt.Fprintf(out, "track %s: %s - %s (%s), %ds, catalogue quality %s\n",
			id, meta.Artist.Name, meta.Title, meta.Album.Title, meta.Duration, meta.AudioQuality)
	} else {
		slog.Warn("track metadata failed", "err", err)
	}
	var dumps strings.Builder
	for _, q := range want {
		fmt.Fprintf(out, "\n== requested %s\n", q)
		pi, err := c.playbackInfo(ctx, id, q)
		if err != nil {
			fmt.Fprintf(out, "  playbackinfo error: %v\n", err)
			continue
		}
		si, err := decodeManifest(pi)
		if err != nil {
			fmt.Fprintf(out, "  delivered %s, manifest %s: decode error: %v\n", pi.AudioQuality, pi.ManifestMimeType, err)
			continue
		}
		printStream(out, pi, si, *showURLs)
		if *probe && len(si.URLs) > 0 {
			hc := &http.Client{Timeout: 20 * time.Second}
			pr, err := probeURL(ctx, hc, si.URLs[0])
			if pr != nil {
				fmt.Fprintf(out, "  probe:        HTTP %d, Content-Type %q, Accept-Ranges %q, size %d, magic %s\n",
					pr.Status, pr.ContentType, pr.AcceptRanges, pr.TotalSize, pr.Magic)
			}
			if err != nil {
				fmt.Fprintf(out, "  probe error:  %v\n", err)
			}
		}
		if *dump != "" {
			fmt.Fprintf(&dumps, "=== %s (%s, %s)\n%s\n\n", q, pi.AudioQuality, pi.ManifestMimeType, si.Raw)
		}
	}
	if *dump != "" {
		if err := os.WriteFile(*dump, []byte(dumps.String()), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nmanifests written to %s (contains signed URLs, keep it local)\n", *dump)
	}
	return nil
}

func printStream(out io.Writer, pi *playbackInfo, si *streamInfo, showURLs bool) {
	fmt.Fprintf(out, "  delivered:    %s (%d bit / %d Hz), mode %s, presentation %s\n",
		pi.AudioQuality, pi.BitDepth, pi.SampleRate, pi.AudioMode, pi.AssetPresentation)
	fmt.Fprintf(out, "  manifest:     %s (%s)\n", si.Kind, pi.ManifestMimeType)
	fmt.Fprintf(out, "  mimeType:     %s\n  codecs:       %s\n  container:    %s\n", si.MimeType, si.Codecs, si.Container)
	fmt.Fprintf(out, "  encryption:   %s (encrypted=%t)\n", si.Encryption, si.Encrypted)
	if si.Kind == "DASH" {
		fmt.Fprintf(out, "  dash:         %d media segments + init, bandwidth %d, duration %s\n", si.SegmentCount, si.Bandwidth, si.Duration)
	}
	fmt.Fprintf(out, "  urls:         %d\n", len(si.URLs))
	if len(si.URLs) > 0 {
		first := si.URLs[0]
		shown := redactURL(first)
		if showURLs {
			shown = first
		}
		fmt.Fprintf(out, "  first url:    %s\n", shown)
		if t, where, ok := urlExpiry(first); ok {
			fmt.Fprintf(out, "  url expiry:   %s (in %s, from %s)\n", t.Local().Format(time.RFC3339), time.Until(t).Round(time.Second), where)
		} else {
			fmt.Fprintf(out, "  url expiry:   not visible in the URL\n")
		}
	}
	fmt.Fprintf(out, "  verdict:      %s\n", si.verdict())
}

func cmdPlay(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("play", flag.ContinueOnError)
	ip := fs.String("speaker", "", "speaker IP (must equal TIDALSPIKE_TEST_SPEAKER)")
	quality := fs.String("quality", "LOSSLESS", "LOW|HIGH|LOSSLESS|HI_RES_LOSSLESS")
	dry := fs.Bool("dry-run", false, "resolve and print the DIDL, do not touch the speaker")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: play <trackId> --speaker <ip> [--quality Q] [--dry-run]")
	}
	q, err := normQuality(*quality)
	if err != nil {
		return err
	}
	if !*dry {
		if err := checkSpeaker(*ip, os.Getenv("TIDALSPIKE_TEST_SPEAKER")); err != nil {
			return err
		}
	}
	c, err := loggedInClient()
	if err != nil {
		return err
	}
	m, err := resolveForPlay(ctx, c, pos[0], q)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "protocolInfo: %s\nDIDL: %s\n", protocolInfo(m.MimeType), redactDIDL(buildDIDL(*m), m.URL))
	if *dry {
		return nil
	}
	rep, err := newSpeaker(*ip).playDirect(ctx, *m, 15*time.Second, 8*time.Second)
	if rep != nil {
		fmt.Fprintf(out, "reached PLAYING: %t, last state %s (status %s), RelTime samples %v\n",
			rep.ReachedPlaying, rep.FinalState, rep.Status, rep.Positions)
	}
	return err
}

// checkSpeaker is the interlock that keeps `play` on the one test speaker:
// the IP must be given, be an IP, and match TIDALSPIKE_TEST_SPEAKER.
func checkSpeaker(ip, allowed string) error {
	if ip == "" {
		return errors.New("--speaker is required")
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("--speaker %q is not an IP address", ip)
	}
	if allowed == "" {
		return errors.New("set TIDALSPIKE_TEST_SPEAKER to the test speaker's IP; play refuses any other box")
	}
	if ip != allowed {
		return fmt.Errorf("--speaker %s is not TIDALSPIKE_TEST_SPEAKER; refusing", ip)
	}
	return nil
}

// resolveForPlay gets a single direct URL plus metadata for the DIDL. DASH
// and encrypted streams are refused: they are not playable without a proxy,
// and saying so is the measurement.
func resolveForPlay(ctx context.Context, c *apiClient, id, quality string) (*didlMeta, error) {
	pi, err := c.playbackInfo(ctx, id, quality)
	if err != nil {
		return nil, err
	}
	si, err := decodeManifest(pi)
	if err != nil {
		return nil, err
	}
	slog.Info("stream resolved", "requested", quality, "delivered", pi.AudioQuality, "manifest", si.Kind, "mime", si.MimeType, "codecs", si.Codecs)
	if si.Encrypted {
		return nil, fmt.Errorf("stream is encrypted (%s), nothing to hand the speaker", si.Encryption)
	}
	u, ok := si.directURL()
	if !ok {
		return nil, fmt.Errorf("no single direct URL (%s): %s", si.Kind, si.verdict())
	}
	m := &didlMeta{MimeType: si.MimeType, URL: u}
	if t, err := c.track(ctx, id); err == nil {
		m.Title = t.Title
		m.Artist = t.Artist.Name
		m.Album = t.Album.Title
		m.Duration = time.Duration(t.Duration) * time.Second
	} else {
		slog.Warn("track metadata failed, DIDL gets a placeholder title", "err", err)
	}
	return m, nil
}

// redactDIDL hides the signed URL in printed DIDL.
func redactDIDL(didl, u string) string {
	return strings.ReplaceAll(didl, xmlEscape(u), xmlEscape(redactURL(u)))
}

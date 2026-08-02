package main

// This file was split out of app.go (wave-1 move-only refactor):
// app version/info and the app update check.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"streborn-app/agentbin"
)

// AppVersion returns the semver version of the running app.
func (a *App) AppVersion() string { return appVersion }

// AppInfo returns app metadata (version, build, author, URLs) for
// the About dialog, footer and auto-update check.
//
// UpdateManifestURL points to a small JSON file of the form
//
//	{"version":"1.1.0","build":"2026-06-01-0900","downloadUrl":"https://.../app-windows-amd64.exe","notes":"..."}
//
// On startup the app checks whether the remote version is greater than its
// own and then shows an update banner. Empty = auto-update off.
type AppInfo struct {
	Version           string `json:"version"`
	Build             string `json:"build"`
	Author            string `json:"author"`
	GitHubURL         string `json:"githubUrl"`
	WebsiteURL        string `json:"websiteUrl"`
	DonateURL         string `json:"donateUrl"`
	DonateSlogan      string `json:"donateSlogan"`
	UpdateManifestURL string `json:"updateManifestUrl"`
	// AgentBinBytes is the size of the embedded agent binary this app pushes
	// on a speaker update (0 on dev builds with the empty stub). The frontend
	// uses it for the storage pre-flight before an OTA.
	AgentBinBytes int64 `json:"agentBinBytes"`
}

// Versions are set via -ldflags X in the build; defaults are for
// development only.
var (
	appVersion = "1.0.0"
	appBuild   = "dev"
)

func (a *App) AppInfo() AppInfo {
	return AppInfo{
		Version:    appVersion,
		Build:      appBuild,
		Author:     "Jens Roggenfelder (JRpersonal)",
		GitHubURL:  "https://github.com/JRpersonal/streborn",
		WebsiteURL: "https://st-reborn.de",
		DonateURL:  "", // populated once the PayPal link on the website is live
		// DonateSlogan is left empty so the frontend renders the
		// locale-aware fallback from the i18n bundle. Hardcoding
		// German here would shadow the bundle for every locale.
		DonateSlogan: "",
		// Update endpoint on the website (separate repo). CheckAppUpdate
		// appends the running client's context (?v=&b=&os=&arch=&lang=) so
		// the server can pick the right OS download and localized notes,
		// then returns a small JSON manifest. See CheckAppUpdate for the
		// request/response contract.
		UpdateManifestURL: "https://st-reborn.de/api/update-check.php",
		AgentBinBytes:     int64(len(agentbin.Bytes())),
	}
}

// versionLess reports whether dotted numeric version a is strictly less
// than b. Both may carry a leading "v" and a git-describe suffix
// ("-3-gabc123-dirty"); only the leading numeric segments are compared,
// so a dev build off tag v0.6.5 compares equal to the v0.6.5 release.
func versionLess(a, b string) bool {
	pa, pb := parseVersionParts(a), parseVersionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var parts []int
	for _, seg := range strings.Split(v, ".") {
		n, ok := 0, false
		for _, r := range seg { // stop at the first non-numeric rune (git suffix)
			if r < '0' || r > '9' {
				break
			}
			n, ok = n*10+int(r-'0'), true
		}
		if !ok {
			break
		}
		parts = append(parts, n)
	}
	return parts
}

// CheckAppUpdate fetches the UpdateManifestURL and returns the manifest
// when the remote version is strictly newer than the running one.
//
// Request: GET UpdateManifestURL with the running client's context as
// query parameters (all non-identifying, no device/network data):
//
//	v     running app version    e.g. v0.6.5
//	b     build stamp            e.g. 2026-06-01-1150
//	os    runtime GOOS           windows | darwin | linux
//	arch  runtime GOARCH         amd64 | arm64
//	lang  active UI locale       e.g. de | en | uk (omitted if unset)
//
// Response: a small JSON object with string fields. version is required;
// downloadUrl and notes are optional. The server may either always return
// the latest release (the client filters with versionLess below) or only
// respond with a body when v is older. Example:
//
//	{"version":"v0.6.6","build":"...","downloadUrl":"https://st-reborn.de/download/windows","notes":"..."}
func (a *App) CheckAppUpdate() (result map[string]string, err error) {
	// The update check is best-effort and must never take the app down.
	// Any unforeseen panic (a malformed response that trips a code path,
	// a nil deref, etc.) is recovered here and reported as a plain error,
	// so an unreachable or garbage endpoint can only ever mean "no banner".
	defer func() {
		if r := recover(); r != nil {
			if a.logger != nil {
				a.logger.Warn("CheckAppUpdate recovered from panic", "panic", r)
			}
			result, err = nil, fmt.Errorf("update check failed")
		}
	}()
	// Kill switch to A/B test whether the startup update check is behind a
	// report (e.g. a macOS start crash). With STR_NO_UPDATE_CHECK set
	// the check is a no-op, so a user can run with it fully off and see if
	// the crash persists.
	if strings.TrimSpace(os.Getenv("STR_NO_UPDATE_CHECK")) != "" {
		return map[string]string{}, nil
	}
	info := a.AppInfo()
	manifestURL := info.UpdateManifestURL
	// Dev/staging override: point the update check at a different
	// manifest (a local mock or the staging endpoint) without
	// rebuilding the baked-in production URL. Empty/unset uses the
	// shipped URL, so this is inert in normal operation.
	if override := strings.TrimSpace(os.Getenv("STR_UPDATE_MANIFEST_URL")); override != "" {
		manifestURL = override
	}
	if manifestURL == "" {
		return map[string]string{}, nil
	}
	reqURL := manifestURL
	if u, perr := url.Parse(reqURL); perr == nil {
		q := u.Query()
		q.Set("v", info.Version)
		q.Set("b", info.Build)
		q.Set("os", runtime.GOOS)
		q.Set("arch", runtime.GOARCH)
		if loc := a.appLocale(); loc != "" {
			q.Set("lang", loc)
		}
		u.RawQuery = q.Encode()
		reqURL = u.String()
	}
	ctx, cancel := context.WithTimeout(a.appCtx(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	// Stable, identifiable agent string so the server can filter bots and
	// keep meaningful update-check stats.
	req.Header.Set("User-Agent", "STReborn-Desktop/"+info.Version+" ("+runtime.GOOS+"; "+runtime.GOARCH+")")
	// Use the pure-Go update client (embedded RootCAs + PreferGo), NOT the
	// shared httpClient. The shared one leaves TLS verification to the
	// platform, which on macOS runs through cgo (Security.framework) and
	// crashed an old Mac on this very call (#102). See updateHTTPClient.
	resp, err := updateHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest status %d", resp.StatusCode)
	}
	var m map[string]string
	// Read cap is generous on purpose: the server caps notes at 1500
	// *characters*, which in heavy multi-byte text (emoji/CJK) can be
	// several KB. 4 KB risked truncating the JSON mid-notes and failing
	// the decode (no banner); 16 KB leaves comfortable headroom.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&m); err != nil {
		return nil, err
	}
	rv := m["version"]
	// Only surface the banner when the remote version is strictly newer
	// than the running one; equal or older (e.g. a dev build ahead of the
	// published tag) stays silent.
	if rv == "" || !versionLess(info.Version, rv) {
		return map[string]string{}, nil
	}
	return m, nil
}

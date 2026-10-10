// Package bosefw finds the Bose firmware file that belongs to a SoundTouch
// speaker, so the app can send the owner of an outdated speaker straight to
// Bose's own download.
//
// Bose still publishes its model catalogue (index.xml) and the update images
// beside it on downloads.bose.com, long after the cloud shutdown. STR reads
// that catalogue at runtime and links the matching file on Bose's host. STR
// never downloads, stores, mirrors or redistributes a firmware image: the user's
// browser fetches it from Bose. When Bose cannot be reached, a small built-in
// table of the final (27.0.6) file names stands in, so the link still works on
// a flaky connection.
package bosefw

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// IndexURL is Bose's SoundTouch model catalogue. worldwide.bose.com/updates/
// soundtouch answers with a 301 to it.
const IndexURL = "https://downloads.bose.com/ced/soundtouch/downloads_stockholm/index.xml"

// DownloadBase is where every file in the catalogue lives.
const DownloadBase = "https://downloads.bose.com/ced/soundtouch/downloads_stockholm/"

// LatestRelease is the final firmware Bose shipped for every SoundTouch.
const LatestRelease = "27.0.6.46330.5043500"

// Entry is one speaker image in the catalogue.
type Entry struct {
	DeviceID    string // e.g. "0x093B"
	ProductName string // e.g. "SoundTouch 20"
	Release     string // e.g. "27.0.6.46330.5043500"
	FileName    string // e.g. "Update_ti_27.0.6.46330.5043500.sm2.stu"
	URL         string // full download URL on Bose's host
}

// File is a download offered to the user.
type File struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Generation string `json:"generation"` // "scm" (older hardware), "sm2" (newer), or ""
	DeviceID   string `json:"deviceId"`
}

// Builtin is the fallback when Bose's catalogue cannot be fetched. It is the
// speaker part of the catalogue as Bose published it (checked 2026-10-10), file
// NAMES only. The files themselves stay on Bose's host.
var Builtin = []Entry{
	builtin("0x0923", "SoundTouch 20", "Update_ti_27.0.6.46330.5043500.scm.stu"),
	builtin("0x0924", "SoundTouch 30", "Update_ti_27.0.6.46330.5043500.scm.stu"),
	builtin("0x0925", "SoundTouch Portable", "Update_ti_27.0.6.46330.5043500.scm.stu"),
	builtin("0x0932", "Wave SoundTouch", "Update_ti_27.0.6.46330.5043500.nelson.scm.stu"),
	builtin("0x0936", "SoundTouch SA-4", "Update_ti_27.0.6.46330.5043500.lisa.scm.stu"),
	builtin("0x0938", "Cinemate", "Update_ti_27.0.6.46330.5043500.triode.scm.stu"),
	builtin("0x0939", "SoundTouch 10", "Update_ti_27.0.6.46330.5043500.rhino.sm2.stu"),
	builtin("0x093A", "SoundTouch SA-5", "Update_ti_27.0.6.46330.5043500.burns.sm2.stu"),
	builtin("0x093B", "SoundTouch 20", "Update_ti_27.0.6.46330.5043500.sm2.stu"),
	builtin("0x093C", "SoundTouch 30", "Update_ti_27.0.6.46330.5043500.sm2.stu"),
	builtin("0x093D", "Wave SoundTouch", "Update_ti_27.0.6.46330.5043500.nelson.sm2.stu"),
	builtin("0x0941", "SoundTouch SA-4", "Update_ti_27.0.6.46330.5043500.lisa.sm2.stu"),
	builtin("0x0942", "Cinemate", "Update_ti_27.0.6.46330.5043500.triode.sm2.stu"),
	builtin("0x0948", "Lifestyle", "Update_ti_27.0.6.46330.5043500.bardeen.sm2.stu"),
	builtin("0x0949", "SoundTouch 300", "Update_ti_27.0.6.46330.5043500.ginger.sm2.stu"),
	builtin("0x094A", "SoundTouch Wireless Link adapter", "Update_ti_27.0.6.46330.5043500.sm2.stu"),
}

func builtin(id, name, file string) Entry {
	return Entry{DeviceID: id, ProductName: name, Release: LatestRelease, FileName: file, URL: DownloadBase + file}
}

type xmlIndex struct {
	Devices []struct {
		ID          string `xml:"ID,attr"`
		ProductName string `xml:"PRODUCTNAME,attr"`
		Hardware    []struct {
			Releases []struct {
				Revision string `xml:"REVISION,attr"`
				HTTPHost string `xml:"HTTPHOST,attr"`
				URLPath  string `xml:"URLPATH,attr"`
				Images   []struct {
					FileName string `xml:"FILENAME,attr"`
				} `xml:"IMAGE"`
			} `xml:"RELEASE"`
		} `xml:"HARDWARE"`
	} `xml:"DEVICE"`
}

// ParseIndex reads Bose's index.xml and returns the speaker update images
// (.stu files) it lists. App installers and other files are skipped, as is any
// entry whose download host is not Bose's own.
func ParseIndex(data []byte) ([]Entry, error) {
	var idx xmlIndex
	if err := xml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse Bose index: %w", err)
	}
	var out []Entry
	for _, d := range idx.Devices {
		for _, h := range d.Hardware {
			for _, r := range h.Releases {
				base, ok := downloadBase(r.HTTPHost, r.URLPath)
				if !ok {
					continue
				}
				for _, img := range r.Images {
					name := strings.TrimSpace(img.FileName)
					if !strings.HasSuffix(strings.ToLower(name), ".stu") || strings.ContainsAny(name, "/\\?#") {
						continue
					}
					out = append(out, Entry{
						DeviceID:    strings.TrimSpace(d.ID),
						ProductName: strings.TrimSpace(d.ProductName),
						Release:     strings.TrimSpace(r.Revision),
						FileName:    name,
						URL:         base + name,
					})
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("parse Bose index: no speaker images listed")
	}
	return out, nil
}

// downloadBase builds the folder URL from a RELEASE's HTTPHOST and URLPATH and
// accepts only Bose's own download host, so a tampered or changed catalogue can
// never turn the app's button into a link somewhere else.
func downloadBase(host, path string) (string, bool) {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	host = strings.TrimSuffix(host, "/")
	if !strings.EqualFold(host, "downloads.bose.com") {
		return "", false
	}
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" || strings.Contains(path, "..") {
		return "", false
	}
	return "https://downloads.bose.com/" + path + "/", true
}

// generation reads the hardware generation out of a file name: the catalogue
// names every image ...scm.stu or ...sm2.stu.
func generation(file string) string {
	f := strings.ToLower(file)
	switch {
	case strings.HasSuffix(f, ".scm.stu"):
		return "scm"
	case strings.HasSuffix(f, ".sm2.stu"):
		return "sm2"
	}
	return ""
}

// prefixModels are catalogue names a speaker may report with a suffix, e.g. a
// "Wave SoundTouch music system IV". Kept to an explicit list: a general prefix
// match would hand a SoundTouch 300 the SoundTouch 30 file.
var prefixModels = []string{"wave soundtouch"}

func modelMatches(product, model string) bool {
	p := strings.ToLower(strings.TrimSpace(product))
	m := strings.ToLower(strings.TrimSpace(model))
	if p == "" || m == "" {
		return false
	}
	if p == m {
		return true
	}
	for _, pm := range prefixModels {
		if p == pm && strings.HasPrefix(m, pm+" ") {
			return true
		}
	}
	return false
}

// Match returns the files that fit a speaker, as read from its :8090/info:
// model is <type>, moduleType is scm or sm2, variant is the chassis code name.
//
// One file is the normal answer. Two files means the model exists on both
// hardware generations and the speaker did not say which one it is: the caller
// shows both, older hardware first, and the speaker's own update page refuses
// the one that does not fit. More than two candidates is a guess the app does
// not make, so it returns nothing and the user follows Bose's article.
func Match(entries []Entry, model, moduleType, variant string) []File {
	var cands []Entry
	seen := map[string]bool{}
	for _, e := range entries {
		if !modelMatches(e.ProductName, model) || seen[e.FileName] {
			continue
		}
		seen[e.FileName] = true
		cands = append(cands, e)
	}
	mt := strings.ToLower(strings.TrimSpace(moduleType))
	if mt == "scm" || mt == "sm2" {
		var same []Entry
		for _, e := range cands {
			if generation(e.FileName) == mt {
				same = append(same, e)
			}
		}
		cands = same
	}
	if len(cands) > 2 {
		// A Lifestyle reports e.g. variant "bardeen", and that name is in the
		// file name. Narrow on it when it settles the choice.
		if v := strings.ToLower(strings.TrimSpace(variant)); v != "" {
			var byVariant []Entry
			for _, e := range cands {
				if strings.Contains(strings.ToLower(e.FileName), "."+v+".") {
					byVariant = append(byVariant, e)
				}
			}
			if len(byVariant) > 0 {
				cands = byVariant
			}
		}
	}
	if len(cands) == 0 || len(cands) > 2 {
		return nil
	}
	if len(cands) == 2 && generation(cands[0].FileName) == "sm2" && generation(cands[1].FileName) == "scm" {
		cands[0], cands[1] = cands[1], cands[0]
	}
	out := make([]File, 0, len(cands))
	for _, e := range cands {
		out = append(out, File{Name: e.FileName, URL: e.URL, Generation: generation(e.FileName), DeviceID: e.DeviceID})
	}
	return out
}

// Catalogue fetches Bose's index once per session and keeps it in memory.
// Nothing is written to disk.
type Catalogue struct {
	Client  *http.Client
	URL     string
	Timeout time.Duration

	mu      sync.Mutex
	entries []Entry
	failed  time.Time
}

// retryAfter keeps a failed fetch from being repeated on every screen render.
const retryAfter = 2 * time.Minute

// Entries returns Bose's live catalogue, or the built-in table when Bose cannot
// be reached. live reports which one it is.
func (c *Catalogue) Entries(ctx context.Context) (entries []Entry, live bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries != nil {
		return c.entries, true
	}
	if !c.failed.IsZero() && time.Since(c.failed) < retryAfter {
		return Builtin, false
	}
	got, err := c.fetch(ctx)
	if err != nil {
		c.failed = time.Now()
		return Builtin, false
	}
	c.entries = got
	return got, true
}

func (c *Catalogue) fetch(ctx context.Context) ([]Entry, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := c.URL
	if url == "" {
		url = IndexURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch index: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return ParseIndex(body)
}

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// probeResult is what the first bytes of a media URL say about it. The
// manifest declares a MIME type; the magic bytes prove it, and the range
// headers tell whether the speaker could seek.
type probeResult struct {
	Status       int
	ContentType  string
	AcceptRanges string
	TotalSize    int64 // from Content-Range, -1 when unknown
	Magic        string
}

// probeURL fetches the first 64 bytes of u with a Range request.
func probeURL(ctx context.Context, hc *http.Client, u string) (*probeResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes=0-63")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	head, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return nil, err
	}
	pr := &probeResult{
		Status:       resp.StatusCode,
		ContentType:  resp.Header.Get("Content-Type"),
		AcceptRanges: resp.Header.Get("Accept-Ranges"),
		TotalSize:    -1,
		Magic:        sniffMagic(head),
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i >= 0 {
			if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
				pr.TotalSize = n
			}
		}
	} else if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
		pr.TotalSize = resp.ContentLength
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return pr, fmt.Errorf("probe: HTTP %d", resp.StatusCode)
	}
	return pr, nil
}

// sniffMagic names the container from its first bytes.
func sniffMagic(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("fLaC")):
		return "FLAC (fLaC)"
	case len(b) >= 8 && string(b[4:8]) == "ftyp":
		brand := ""
		if len(b) >= 12 {
			brand = string(b[8:12])
		}
		return "MP4 (ftyp " + strings.TrimSpace(brand) + ")"
	case len(b) >= 8 && (string(b[4:8]) == "moof" || string(b[4:8]) == "styp" || string(b[4:8]) == "sidx"):
		return "fragmented MP4 segment (" + string(b[4:8]) + ")"
	case bytes.HasPrefix(b, []byte("ID3")):
		return "MP3 (ID3)"
	case len(b) >= 2 && b[0] == 0xFF && (b[1]&0xF6) == 0xF0:
		return "ADTS AAC"
	case len(b) >= 2 && b[0] == 0xFF && (b[1]&0xE0) == 0xE0:
		return "MPEG audio frame"
	case len(b) == 0:
		return "empty"
	}
	n := len(b)
	if n > 8 {
		n = 8
	}
	return fmt.Sprintf("unknown (% x)", b[:n])
}

package webui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Starting a station the way the speaker starts it itself.
//
// A hardware preset key activates a native LOCAL_INTERNET_RADIO station and the
// speaker fetches the stream on its own. Starting the same preset from the app
// used to take a different route: it pushed the stream over UPnP. That has two
// costs. It re-exposes the app path to the refusal the native form exists to
// avoid, and when the speaker is already playing that station natively it tears
// a working stream down and rebuilds it as UPnP, which is audible as a gap and
// visible as the source flipping LOCAL_INTERNET_RADIO -> UPNP -> ... (measured
// on a Portable, 2026-08-04).
//
// The speaker accepts the native item over its own /select endpoint once the
// radio source is registered, which is exactly the state the agent now
// establishes. Verified live on a Portable: /select with the station item
// switches the source to LOCAL_INTERNET_RADIO and plays, with no 1005 and no
// 1036. (The same call answers 1005 UNKNOWN_SOURCE_ERROR when the source is
// NOT registered, which is what makes the readiness gate below load-bearing
// rather than decorative.)

// selectNativeStation asks the speaker to activate a native radio station,
// the same item its own preset key would activate.
//
// Returns an error when the speaker refuses, so the caller can fall back to the
// UPnP push rather than leaving the user with silence.
func (s *Server) selectNativeStation(ctx context.Context, location, name string) error {
	if s.boxHost == "" || location == "" {
		return fmt.Errorf("selectNativeStation: box host and location required")
	}
	body := `<ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="` +
		escapeXMLAttr(location) + `" sourceAccount="" isPresetable="true"><itemName>` +
		escapeXMLText(name) + `</itemName></ContentItem>`

	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodPost,
		"http://"+s.boxHost+":8090/select", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("select: status %d: %s", resp.StatusCode, strings.TrimSpace(string(answer)))
	}
	// The speaker answers 200 with an <error .../> body when it does not know
	// the source, so the status code alone is not the verdict.
	if strings.Contains(string(answer), "<error") {
		return fmt.Errorf("select: box refused: %s", strings.TrimSpace(string(answer)))
	}
	return nil
}

func escapeXMLAttr(s string) string {
	return strings.NewReplacer(`&`, "&amp;", `<`, "&lt;", `>`, "&gt;", `"`, "&quot;").Replace(s)
}

func escapeXMLText(s string) string {
	return strings.NewReplacer(`&`, "&amp;", `<`, "&lt;", `>`, "&gt;").Replace(s)
}

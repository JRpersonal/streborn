// App-side text translation for the announcement composer, using Google's
// keyless translate endpoint (the same no-API-key approach as the radio-browser
// client). Source language is auto-detected; the target is the TTS voice the
// user picked. Done app-side so the box stays minimal (app-first architecture).

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Translate returns text translated into targetLang (a 2-letter code like "de").
// Keyless: it calls translate.googleapis.com/translate_a/single?client=gtx,
// which needs no API key or billing. Source language is auto-detected. The
// response is Google's nested-array shape: [ [ ["translated","orig",...], ... ],
// ... ]; the translated segments are result[0][i][0], concatenated.
func (a *App) Translate(text, targetLang string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("no text to translate")
	}
	targetLang = strings.TrimSpace(targetLang)
	if targetLang == "" {
		targetLang = "en"
	}
	q := url.Values{}
	q.Set("client", "gtx")
	q.Set("sl", "auto")
	q.Set("tl", targetLang)
	q.Set("dt", "t")
	q.Set("q", text)
	u := "https://translate.googleapis.com/translate_a/single?" + q.Encode()
	req, err := http.NewRequestWithContext(a.appCtx(), http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	// A browser-ish UA avoids the occasional bot rejection on this endpoint.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; STReborn)")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("translation service returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", err
	}
	var outer []json.RawMessage
	if err := json.Unmarshal(body, &outer); err != nil || len(outer) == 0 {
		return "", fmt.Errorf("could not read the translation response")
	}
	var segs [][]any
	if err := json.Unmarshal(outer[0], &segs); err != nil {
		return "", fmt.Errorf("could not read the translation segments")
	}
	var b strings.Builder
	for _, s := range segs {
		if len(s) > 0 {
			if str, ok := s[0].(string); ok {
				b.WriteString(str)
			}
		}
	}
	res := strings.TrimSpace(b.String())
	if res == "" {
		return "", fmt.Errorf("the translation came back empty")
	}
	return res, nil
}

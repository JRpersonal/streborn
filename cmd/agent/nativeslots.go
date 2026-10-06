package main

// Keys that hold a station of a music service the speaker plays by itself
// (Pandora, iHeartRadio; presets.TypeNative).
//
// STR has nothing to proxy for these: the firmware's own client plays them,
// with the account the speaker holds. So the reconcile treats them unlike
// every other preset. It never writes STR's stream form over them, it leaves
// the key alone whenever the speaker already holds that exact item, and when
// the key is missing or holds something else it writes the stored ContentItem
// back, verbatim, only while the speaker offers the service. A service that is
// not registered (the US services are off, the account was removed) gets a log
// line and nothing else: the preset stays in the store, the key is never
// deleted, and it is written back once the service is there again.

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/presets"
)

var (
	// addPresetContentItemFn writes one key as a native ContentItem.
	// A variable so tests can record the writes instead of dialling TAP.
	addPresetContentItemFn = boxcli.AddPresetContentItem
	// boxSourceStatusFn reads the speaker's own source list.
	boxSourceStatusFn = boxSourceStatus
	// boxSourceAccountsFn reads the accounts the speaker lists per source.
	boxSourceAccountsFn = boxSourceAccounts
)

// nativeSlotSkip is a native key the reconcile left alone, and why.
type nativeSlotSkip struct {
	Slot   int
	Source string
	Reason string
}

// planNativeSlots decides which native keys need writing. A key the speaker
// already holds with the stored item is never written (that is what makes the
// reconcile idempotent, and why a forced full pass leaves it alone too); a key
// that is missing or holds something else is written while sourceReady says
// the speaker offers the service, and skipped otherwise. sourceReady is only
// called when a write is actually wanted, so a healthy pass costs no request.
func planNativeSlots(stick []presets.Preset, entries []boxPresetEntry, sourceReady func(source string) bool) (writes []presets.Preset, skips []nativeSlotSkip) {
	onBox := make(map[int]boxPresetEntry, len(entries))
	for _, e := range entries {
		onBox[e.Slot] = e
	}
	for _, p := range stick {
		if !p.IsNative() {
			continue
		}
		if e, ok := onBox[p.Slot]; ok && boxHoldsNativeItem(e, p.Native) {
			continue
		}
		if !sourceReady(p.Native.Source) {
			skips = append(skips, nativeSlotSkip{Slot: p.Slot, Source: p.Native.Source,
				Reason: "the speaker does not offer this service right now"})
			continue
		}
		writes = append(writes, p)
	}
	return writes, skips
}

// boxHoldsNativeItem reports whether the speaker's own entry for a key is the
// stored native item. The location is compared unescaped (the speaker's
// /presets list escapes attribute values) and the source case-insensitively.
func boxHoldsNativeItem(e boxPresetEntry, item *presets.NativeItem) bool {
	if item == nil {
		return false
	}
	return xmlEntityUnescape(e.Location) == item.Location &&
		presets.NormalizeNativeSource(e.Source) == presets.NormalizeNativeSource(item.Source)
}

// nativeWriteBackoff is how long a native write the speaker refused is left
// alone before the reconcile tries it again. A refusal is not a timing hiccup
// the way a boot-window UPnP miss is: the speaker either takes the item or it
// does not, and asking every maintenance pass would wake it for nothing.
const nativeWriteBackoff = time.Hour

// nativeSlotState remembers refusals and the last skip reason per key, so a
// refused write backs off and a skip is logged once rather than every pass.
var nativeSlotState = struct {
	sync.Mutex
	refusedAt map[string]time.Time
	skipped   map[int]string
}{refusedAt: map[string]time.Time{}, skipped: map[int]string{}}

func nativeWriteKey(p presets.Preset) string {
	return fmt.Sprintf("%d|%s|%s", p.Slot, p.Native.Source, p.Native.Location)
}

// logNativeSkips logs each skipped key once per reason.
func logNativeSkips(skips []nativeSlotSkip, logger *slog.Logger) {
	nativeSlotState.Lock()
	defer nativeSlotState.Unlock()
	seen := map[int]bool{}
	for _, sk := range skips {
		seen[sk.Slot] = true
		if nativeSlotState.skipped[sk.Slot] == sk.Reason {
			continue
		}
		nativeSlotState.skipped[sk.Slot] = sk.Reason
		logger.Info("preset reconcile: leaving a native service key as it is, it is kept in the store and written back once the speaker offers the service",
			"slot", sk.Slot, "source", sk.Source, "reason", sk.Reason)
	}
	for slot := range nativeSlotState.skipped {
		if !seen[slot] {
			delete(nativeSlotState.skipped, slot)
		}
	}
}

// writeNativeSlots writes the planned native keys and returns the slots the
// speaker took. A refusal is logged and backs off; it never fails the pass,
// because the fast retry cadence a failed pass triggers would hammer a speaker
// that will keep refusing.
func writeNativeSlots(boxHost string, writes []presets.Preset, logger *slog.Logger) (written []int) {
	now := time.Now()
	var accounts map[string][]string
	for _, p := range writes {
		key := nativeWriteKey(p)
		nativeSlotState.Lock()
		at, refused := nativeSlotState.refusedAt[key]
		nativeSlotState.Unlock()
		if refused && now.Sub(at) < nativeWriteBackoff {
			continue
		}
		// The account the speaker itself lists for the service, read once per
		// pass and only when a write is due. The stored one can be missing or
		// the station's name (#1101), which the speaker's command line cannot
		// carry and the speaker would not recognise.
		if accounts == nil {
			accounts = boxSourceAccountsFn(boxHost)
		}
		account := presets.ResolveNativeAccount(*p.Native, accounts)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		err := addPresetContentItemFn(ctx, boxHost, p.Slot, p.Native.Source, p.Native.ItemType,
			p.Native.Location, p.Name, account)
		cancel()
		nativeSlotState.Lock()
		if err != nil {
			nativeSlotState.refusedAt[key] = now
		} else {
			delete(nativeSlotState.refusedAt, key)
		}
		nativeSlotState.Unlock()
		if err != nil {
			logger.Warn("preset reconcile: the speaker did not take a native service key, trying again in an hour",
				"slot", p.Slot, "source", p.Native.Source, "type", p.Native.ItemType, "err", err)
			continue
		}
		logger.Info("preset reconcile: wrote a native service key back as the speaker's own item",
			"slot", p.Slot, "source", p.Native.Source, "name", p.Name)
		written = append(written, p.Slot)
	}
	return written
}

// lazySourceReady returns a sourceReady func that reads the speaker's source
// list at most once, on first use.
func lazySourceReady(boxHost string) func(string) bool {
	var once sync.Once
	var status map[string]string
	return func(source string) bool {
		once.Do(func() { status = boxSourceStatusFn(boxHost) })
		return strings.EqualFold(status[presets.NormalizeNativeSource(source)], "READY")
	}
}

// boxSourceAccounts reads :8090/sources into source -> listed accounts
// (presets.NativeSourceAccounts). Unreadable is an empty, non-nil map.
func boxSourceAccounts(boxHost string) map[string][]string {
	body := readBoxSources(boxHost)
	if body == nil {
		return map[string][]string{}
	}
	return presets.NativeSourceAccounts(body)
}

// readBoxSources fetches the speaker's /sources document, nil on any failure.
func readBoxSources(boxHost string) []byte {
	if boxHost == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+boxHost+":8090/sources", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil
	}
	return body
}

// boxSourceStatus reads :8090/sources into source -> status. A source listed
// several times (one per account) counts as READY when any entry is. An
// unreadable answer is an empty map, which reads as "not offered".
func boxSourceStatus(boxHost string) map[string]string {
	body := readBoxSources(boxHost)
	if body == nil {
		return map[string]string{}
	}
	return parseSourceStatus(body)
}

// parseSourceStatus is boxSourceStatus's parser.
func parseSourceStatus(body []byte) map[string]string {
	out := map[string]string{}
	var doc struct {
		Items []struct {
			Source string `xml:"source,attr"`
			Status string `xml:"status,attr"`
		} `xml:"sourceItem"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return out
	}
	for _, it := range doc.Items {
		src := presets.NormalizeNativeSource(it.Source)
		if strings.EqualFold(out[src], "READY") {
			continue
		}
		out[src] = strings.ToUpper(strings.TrimSpace(it.Status))
	}
	return out
}

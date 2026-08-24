// Dispatch coverage for the frames the official doc names that STR previously
// let fall through as unrecognized: the wrapped sourcesUpdated form (a real
// missed signal), the documented no-op frames, and acctModeUpdated.

package boxws

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// newLogClient builds a Client with a buffer-backed logger at the given level
// and no handler, so a test can assert exactly what reaches the log (the NAND
// ring keeps Info and above, so "nothing at INFO" is a real requirement here,
// not cosmetics).
func newLogClient(buf *bytes.Buffer, level slog.Level) *Client {
	return New(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})),
		"ws://127.0.0.1:8080/", nil)
}

func waitFired(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: the sources-changed callback never fired", what)
	}
}

// The box sends the sources-changed signal in BOTH wire forms: bare-root
// <sourcesUpdated/> and wrapped <updates><sourcesUpdated/></updates> (the
// Portable emits the wrapped one on every source change). Until the wrapped
// form was parsed, wrapped-form chassis NEVER fired the callback, so the agent
// kept a stale "native radio not ready" verdict and wrote UPnP presets during
// the registration window. Both forms must fire the same callback.
func TestHandleMessage_SourcesUpdatedFiresCallbackBothWireForms(t *testing.T) {
	frames := map[string]string{
		"bare root": `<sourcesUpdated deviceID="AABBCCDDEEFF" />`,
		"wrapped":   `<updates deviceID="AABBCCDDEEFF"><sourcesUpdated /></updates>`,
	}
	for form, frame := range frames {
		var buf bytes.Buffer
		c := newLogClient(&buf, slog.LevelDebug)
		fired := make(chan struct{}, 1)
		c.SetOnSourcesChanged(func() {
			select {
			case fired <- struct{}{}:
			default:
			}
		})
		c.handleMessage(context.Background(), []byte(frame))
		waitFired(t, fired, form)
		if strings.Contains(buf.String(), "unrecognized frame") {
			t.Errorf("%s: a handled sources signal must not also count as unrecognized:\n%s", form, buf.String())
		}
	}
}

// The wrapped form arrives once per source change on the Portable, so it must
// stay OFF the INFO level entirely: the 32 KB NAND ring keeps Info and above,
// and churning it is a defect (the bare form keeps its INFO line because it is
// the rare startup-registration signal).
func TestHandleMessage_WrappedSourcesUpdatedLogsAtDebugOnly(t *testing.T) {
	frame := []byte(`<updates deviceID="AABBCCDDEEFF"><sourcesUpdated /></updates>`)

	var info bytes.Buffer
	newLogClient(&info, slog.LevelInfo).handleMessage(context.Background(), frame)
	if info.Len() != 0 {
		t.Errorf("the wrapped sourcesUpdated form must produce nothing at INFO, got:\n%s", info.String())
	}

	var debug bytes.Buffer
	newLogClient(&debug, slog.LevelDebug).handleMessage(context.Background(), frame)
	if !strings.Contains(debug.String(), "registered sources") {
		t.Errorf("the wrapped hit must still be visible at DEBUG, got:\n%s", debug.String())
	}
}

// swUpdateStatusUpdated / siteSurveyResultsUpdated are documented as requiring
// no client action, and recentsUpdated / infoUpdated carry nothing STR
// consumes. Each must be known-and-ignored in BOTH wire forms: no INFO line
// (the old path logged the first of each shape in full at INFO), and no
// noteExplainedActivity stamp (swUpdateStatusUpdated fires on source changes,
// not user presses, so an "explained" mark here would swallow real thumb
// presses).
func TestHandleMessage_DocumentedNoOpFramesAreKnownAndSilent(t *testing.T) {
	frames := []string{
		`<updates deviceID="AABBCCDDEEFF"><swUpdateStatusUpdated /></updates>`,
		`<swUpdateStatusUpdated deviceID="AABBCCDDEEFF" />`,
		`<updates deviceID="AABBCCDDEEFF"><siteSurveyResultsUpdated /></updates>`,
		`<siteSurveyResultsUpdated deviceID="AABBCCDDEEFF" />`,
		// recentsUpdated with its documented value-carrying body (lowercase
		// contentItem): the body is deliberately discarded, and it must not
		// confuse the presence-only parse.
		`<updates deviceID="AABBCCDDEEFF"><recentsUpdated><recents>` +
			`<recent deviceID="AABBCCDDEEFF" utcTime="1">` +
			`<contentItem source="TUNEIN" location="stationid" sourceAccount="" isPresetable="true">` +
			`<itemName>x</itemName></contentItem></recent></recents></recentsUpdated></updates>`,
		`<recentsUpdated deviceID="AABBCCDDEEFF" />`,
		`<updates deviceID="AABBCCDDEEFF"><infoUpdated /></updates>`,
		`<infoUpdated deviceID="AABBCCDDEEFF" />`,
	}
	for _, frame := range frames {
		var buf bytes.Buffer
		c := newLogClient(&buf, slog.LevelInfo)
		c.handleMessage(context.Background(), []byte(frame))
		if strings.Contains(buf.String(), "unrecognized frame") {
			t.Errorf("frame %q still hits the unrecognized path:\n%s", frame, buf.String())
		}
		if buf.Len() != 0 {
			t.Errorf("frame %q must produce nothing at INFO, got:\n%s", frame, buf.String())
		}
		c.thumbMu.Lock()
		explained := !c.thumbExplained.IsZero()
		c.thumbMu.Unlock()
		if explained {
			t.Errorf("frame %q must not count as explained activity (it is not a user press)", frame)
		}
	}
}

// acctModeUpdated has never been seen in a field bundle on 27.0.6 and may
// never fire; it is parsed so that IF it does, the account-association flip
// behind a 1036 storm gets a timestamp. One INFO line, deduped so a flapping
// box cannot spam the NAND ring, shared across both wire forms.
func TestHandleMessage_AcctModeUpdatedLogsOnceDeduped(t *testing.T) {
	const marker = "box account association changed"
	wrapped := []byte(`<updates deviceID="AABBCCDDEEFF"><acctModeUpdated></acctModeUpdated></updates>`)
	bare := []byte(`<acctModeUpdated deviceID="AABBCCDDEEFF" />`)

	var buf bytes.Buffer
	c := newLogClient(&buf, slog.LevelInfo)

	// First frame: exactly one INFO line, and not the unrecognized path.
	c.handleMessage(context.Background(), wrapped)
	if n := strings.Count(buf.String(), marker); n != 1 {
		t.Fatalf("first acctModeUpdated must log exactly one INFO line, got %d:\n%s", n, buf.String())
	}
	if strings.Contains(buf.String(), "unrecognized frame") {
		t.Fatalf("acctModeUpdated must not count as unrecognized:\n%s", buf.String())
	}

	// A flap right after (in either wire form) is swallowed by the gate.
	c.handleMessage(context.Background(), wrapped)
	c.handleMessage(context.Background(), bare)
	if n := strings.Count(buf.String(), marker); n != 1 {
		t.Fatalf("a flapping box must not add INFO lines inside the dedupe window, got %d:\n%s", n, buf.String())
	}

	// Once the gate ages out, the next frame is logged again.
	c.mu.Lock()
	c.lastAcctModeLogAt = time.Now().Add(-acctModeLogEvery - time.Second)
	c.mu.Unlock()
	c.handleMessage(context.Background(), bare)
	if n := strings.Count(buf.String(), marker); n != 2 {
		t.Fatalf("an aged gate must let the next association change through, got %d lines:\n%s", n, buf.String())
	}
}

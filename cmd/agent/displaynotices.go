// The agent's side of STR's display notices (internal/oled notice.go): which
// of the agent's own events put a short message on the speaker display.
//
//   - a held key stored what STR plays (holdsave.go) or what the speaker
//     plays itself (holdstore.go): SAVED N and the name; refused because the
//     item is on another key: ON KEY M; refused because STR has no preset
//     form for it: NOT SAVED.
//   - a physical press on a key that holds nothing at all (holdsave.go,
//     checkEmpty): KEY N, EMPTY.
//   - the reconcile wrote keys back that the box lost over a reboot: N KEYS
//     RESTORED, once in the life of the speaker. Some speakers (the Portable)
//     drop every key registration on every boot, so a notice per boot would
//     be routine noise; once is enough to show STR does this.
//
// The saves from the app and the phone page are noticed in internal/webui,
// where those requests land.

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/oled"
)

// showNotice is oled.Show; tests swap it.
var showNotice = oled.Show

// restoreNoticeWindow bounds "after a reboot": a key the reconcile writes back
// later than this after a box boot is a heal like any other, not a restore.
const restoreNoticeWindow = 15 * time.Minute

// restoreNoticeArmedAt is when a box boot armed the one restored-keys notice
// of this agent start (unix nanos, 0 = not armed or already used).
var restoreNoticeArmedAt atomic.Int64

// restoreNoticeShownPath marks that this speaker has shown the restored-keys
// notice once. Written a single time, never again, so it costs no NAND wear.
var restoreNoticeShownPath = "/mnt/nv/streborn/display-notice-restored-shown"

// armRestoreNotice is called once at start-up when the box itself just booted.
// It arms nothing once the speaker has shown the notice before.
func armRestoreNotice(now time.Time) {
	if _, err := os.Stat(restoreNoticeShownPath); err == nil {
		return
	}
	restoreNoticeArmedAt.Store(now.UnixNano())
}

// noteKeysRestored shows "N KEYS RESTORED AFTER REBOOT" the first time a
// reconcile pass after a box boot put keys back, and never again on this
// speaker.
func noteKeysRestored(n int) {
	if n <= 0 {
		return
	}
	at := restoreNoticeArmedAt.Load()
	if at == 0 || time.Since(time.Unix(0, at)) > restoreNoticeWindow {
		return
	}
	if restoreNoticeArmedAt.CompareAndSwap(at, 0) {
		showNotice(oled.KeysRestored(n))
		_ = os.WriteFile(restoreNoticeShownPath, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
	}
}

// heldGestureSaver is the running hold saver, so the marge keeper can tell
// the firmware's store gesture from its boot-time sync: only a store that
// follows a physical press of that key within heldNoticeWindow is a gesture.
var heldGestureSaver atomic.Pointer[holdSaver]

// heldNoticeWindow is how long after a physical press of a key the
// firmware's PUT for that key still counts as the user's hold. The PUT
// follows the hold verdict (about 1.6 s) within a second or two.
const heldNoticeWindow = 15 * time.Second

// alreadyOnKeyError is the keeper's refusal of an item that already sits on
// another key (#836), with that key.
type alreadyOnKeyError struct {
	slot int
	name string
}

func (e *alreadyOnKeyError) Error() string {
	return fmt.Sprintf("%v: %q is already on key %d", errNotKeepable, e.name, e.slot)
}

func (e *alreadyOnKeyError) Unwrap() error { return errNotKeepable }

// noticeHeldStore puts the outcome of the firmware's own store gesture on the
// display. The boot-time sync and a retried PUT reach the keeper too, so
// nothing is shown unless a person pressed that key moments ago. A UPnP item
// is left to holdsave.go, which saves what STR plays on the same hold.
func noticeHeldStore(item marge.HeldItem, name string, err error) {
	hs := heldGestureSaver.Load()
	if hs == nil || !hs.recentPhysicalPress(item.Slot, heldNoticeWindow) {
		return
	}
	var dup *alreadyOnKeyError
	switch {
	case err == nil:
		// a re-hold of what the key already holds is confirmed too
		showNotice(oled.SavedByKey(item.Slot, name))
	case errors.As(err, &dup):
		showNotice(oled.AlreadyOnKey(dup.slot))
	case errors.Is(err, errNotKeepable) && !strings.EqualFold(strings.TrimSpace(item.Source), "UPNP"):
		showNotice(oled.NotSaved(item.Slot))
	}
}

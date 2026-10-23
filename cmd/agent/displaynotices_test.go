package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/marge"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/webui"
)

type noticeLog struct {
	mu sync.Mutex
	ns []oled.Notice
}

func (l *noticeLog) add(n oled.Notice) { l.mu.Lock(); l.ns = append(l.ns, n); l.mu.Unlock() }

func (l *noticeLog) get() []oled.Notice {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]oled.Notice(nil), l.ns...)
}

func (l *noticeLog) only(t *testing.T, kind oled.NoticeKind, key int) oled.Notice {
	t.Helper()
	ns := l.get()
	if len(ns) != 1 || ns[0].Kind != kind || ns[0].Key != key {
		t.Fatalf("notices = %+v, want one %s on key %d", ns, kind, key)
	}
	return ns[0]
}

func (l *noticeLog) none(t *testing.T) {
	t.Helper()
	if ns := l.get(); len(ns) != 0 {
		t.Fatalf("unexpected notices %+v", ns)
	}
}

func swapShowNotice(t *testing.T) *noticeLog {
	l := &noticeLog{}
	old := showNotice
	showNotice = l.add
	t.Cleanup(func() { showNotice = old })
	return l
}

func TestHoldSaveNotices(t *testing.T) {
	cases := []struct {
		name   string
		result webui.HoldSaved
		err    error
		kind   oled.NoticeKind
		key    int
	}{
		{"saved", webui.HoldSaved{Kind: "spotify", Name: "Jens Chill"}, nil, oled.NoticeSavedByKey, 1},
		{"unchanged", webui.HoldSaved{Kind: "queue", Name: "Album", Unchanged: true}, nil, oled.NoticeSavedByKey, 1},
		{"swapped", webui.HoldSaved{Kind: "spotify", Name: "Jens Chill", SwappedWith: 4, SwappedName: "Rock"}, nil, oled.NoticeSwapped, 1},
		{"on another key", webui.HoldSaved{}, &webui.HoldSaveError{Code: "already-on-slot", OtherSlot: 4, OtherName: "X"}, oled.NoticeAlreadyOnKey, 4},
		{"refused", webui.HoldSaved{}, &webui.HoldSaveError{Code: "spotify-context-unknown"}, oled.NoticeNotSaved, 1},
		{"unknown stream", webui.HoldSaved{}, webui.ErrHoldUnknownStream, oled.NoticeNotSaved, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newHoldRig(t)
			l := &noticeLog{}
			r.hs.notify = l.add
			r.result, r.err = c.result, c.err
			r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
			r.gesture(true, 1600)
			n := l.only(t, c.kind, c.key)
			if c.kind == oled.NoticeSavedByKey && n.Line2 == "" {
				t.Fatal("saved notice without the item name")
			}
		})
	}
	// The speaker's own store gesture covers a native source: nothing here.
	r := newHoldRig(t)
	l := &noticeLog{}
	r.hs.notify = l.add
	r.err = fmt.Errorf("%w (source TUNEIN)", webui.ErrHoldNotUPnP)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 1600)
	l.none(t)
}

func TestEmptyKeyNotice(t *testing.T) {
	setup := func(empty, known bool) (*holdRig, *noticeLog) {
		r := newHoldRig(t)
		l := &noticeLog{}
		r.hs.notify = l.add
		r.hs.emptyKey = func(int) (bool, bool) { return empty, known }
		return r, l
	}

	r, l := setup(true, true)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(false, 150)
	r.fireChecks()
	l.only(t, oled.NoticeEmptyKey, 2)

	// The speaker activated something after the press: not empty after all.
	r, l = setup(true, true)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.now = r.now.Add(300 * time.Millisecond)
	r.hs.NoteSelection()
	r.fireChecks()
	l.none(t)

	// The box's own list was never reported: unknown, so silent. Every tap
	// brings its tap verdict (live ST10, 2026-10-16); without one a stats
	// press would read as a hold (holdSaver.statsHold).
	r, l = setup(true, false)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(false, 150)
	r.fireChecks()
	l.none(t)

	// A key that holds something.
	r, l = setup(false, true)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(false, 150)
	r.fireChecks()
	l.none(t)

	// A hold on an empty key is a store gesture, not a press of an empty key.
	r, l = setup(true, true)
	r.err = webui.ErrHoldNotUPnP
	r.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 1600)
	r.fireChecks()
	l.none(t)

	// A key sent over the network is nobody pressing the speaker.
	r, l = setup(true, true)
	r.press("PRESET_2", boxlog.ProducerGabbo, boxlog.OriginWeb, 0)
	r.fireChecks()
	l.none(t)
}

func TestHeldStoreNotices(t *testing.T) {
	l := swapShowNotice(t)
	r := newHoldRig(t)
	r.hs.now = func() time.Time { return r.now }
	heldGestureSaver.Store(r.hs)
	t.Cleanup(func() { heldGestureSaver.Store(nil) })

	item := marge.HeldItem{Slot: 3, Source: "LOCAL_INTERNET_RADIO"}
	// No press: the boot-time sync, silent whatever the outcome.
	noticeHeldStore(item, "Radio Bob", nil)
	l.none(t)

	r.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.now = r.now.Add(2 * time.Second)
	noticeHeldStore(item, "Radio Bob", nil)
	l.only(t, oled.NoticeSavedByKey, 3)

	l.ns = nil
	noticeHeldStore(item, "", &alreadyOnKeyError{slot: 5, name: "Radio Bob"})
	l.only(t, oled.NoticeAlreadyOnKey, 5)

	l.ns = nil
	noticeHeldStore(marge.HeldItem{Slot: 3, Source: "DEEZER"}, "", fmt.Errorf("%w: source DEEZER", errNotKeepable))
	l.only(t, oled.NoticeNotSaved, 3)

	// A UPnP push is holdsave.go's to answer.
	l.ns = nil
	noticeHeldStore(marge.HeldItem{Slot: 3, Source: "UPNP"}, "", fmt.Errorf("%w: source UPNP", errNotKeepable))
	l.none(t)

	// A press of another key, or one long ago, does not count.
	noticeHeldStore(marge.HeldItem{Slot: 4, Source: "LOCAL_INTERNET_RADIO"}, "X", nil)
	r.now = r.now.Add(heldNoticeWindow)
	noticeHeldStore(item, "Radio Bob", nil)
	l.none(t)

	if !errors.Is(&alreadyOnKeyError{slot: 1}, errNotKeepable) {
		t.Fatal("alreadyOnKeyError must still be errNotKeepable")
	}
}

// TestHeldStoreNoticeNeedsAHold pins the 2026-10-22 SoundTouch 20 bundle: a
// tap on (dead) key 6, the firmware's tap verdict 3 ms later, and STR's own
// key re-sync PUT for slot 6 twelve seconds after that. The display said
// "SAVED 6" for a store nobody made. Only a hold may confirm a store.
func TestHeldStoreNoticeNeedsAHold(t *testing.T) {
	l := swapShowNotice(t)
	t.Cleanup(func() { heldGestureSaver.Store(nil) })
	item := marge.HeldItem{Slot: 6, Source: "LOCAL_INTERNET_RADIO"}

	// The field case: tap verdict, then the re-sync PUT inside the window.
	r := newHoldRig(t)
	r.hs.now = func() time.Time { return r.now }
	heldGestureSaver.Store(r.hs)
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.gesture(false, 3)
	r.now = r.now.Add(12 * time.Second)
	noticeHeldStore(item, "Adroit Jazz Underground", nil)
	l.none(t)

	// A preset activation after the press is a tap too (no verdict line).
	r = newHoldRig(t)
	r.hs.now = func() time.Time { return r.now }
	heldGestureSaver.Store(r.hs)
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.now = r.now.Add(300 * time.Millisecond)
	r.hs.NoteSelection()
	r.now = r.now.Add(2 * time.Second)
	noticeHeldStore(item, "Adroit Jazz Underground", nil)
	l.none(t)

	// A real hold: verdict paired with the press, the firmware's PUT follows.
	r = newHoldRig(t)
	r.err = webui.ErrHoldNotUPnP
	r.hs.now = func() time.Time { return r.now }
	heldGestureSaver.Store(r.hs)
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.gesture(true, 1600)
	r.now = r.now.Add(3 * time.Second)
	noticeHeldStore(item, "Adroit Jazz Underground", nil)
	l.only(t, oled.NoticeSavedByKey, 6)
}

func TestKeysRestoredNotice(t *testing.T) {
	l := swapShowNotice(t)
	t.Cleanup(func() { restoreNoticeArmedAt.Store(0) })
	oldPath := restoreNoticeShownPath
	restoreNoticeShownPath = filepath.Join(t.TempDir(), "shown")
	t.Cleanup(func() { restoreNoticeShownPath = oldPath })

	noteKeysRestored(3) // not armed: the agent restarted on a running box
	l.none(t)

	armRestoreNotice(time.Now())
	noteKeysRestored(0)
	l.none(t)
	noteKeysRestored(2)
	l.only(t, oled.NoticeRestored, 2)
	noteKeysRestored(4)
	if len(l.get()) != 1 {
		t.Fatal("restored notice shown twice")
	}

	// the next box boot: the speaker has shown it once, never again
	l.ns = nil
	armRestoreNotice(time.Now())
	noteKeysRestored(5)
	l.none(t)

	// a boot long ago does not count as "after a reboot"
	_ = os.Remove(restoreNoticeShownPath)
	armRestoreNotice(time.Now().Add(-restoreNoticeWindow - time.Minute))
	noteKeysRestored(2)
	l.none(t)
}

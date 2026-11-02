package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// keyBox is a copy target with a real store and the agent's duplicate guard:
// a station may sit on one key only, judged on the finished store for a bulk
// write (#882) and on the current store for a per-slot write (#836).
type keyBox struct {
	mu    sync.Mutex
	srv   *httptest.Server
	host  string
	store map[int]Preset
	// bulk: the agent knows PUT /api/presets. payloadSlot: its 409 names the
	// key of the set that collided (agents from #1551 on).
	bulk, payloadSlot bool
	presetRows        string
	bulkPuts          int
	slotPuts          int
	syncs             int
}

func newKeyBox(t *testing.T, host string, store []Preset) *keyBox {
	t.Helper()
	b := &keyBox{host: host, store: map[int]Preset{}, bulk: true, payloadSlot: true}
	for _, p := range store {
		b.store[p.Slot] = p
	}
	conflict := func(w http.ResponseWriter, name string, slot, payload int) {
		body := map[string]any{"code": "already-on-slot", "name": name, "slot": slot}
		if b.payloadSlot && payload > 0 {
			body["payloadSlot"] = payload
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(body)
	}
	b.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agent/version":
			ver := map[string]string{"version": "v1.3.2"}
			if b.presetRows != "" {
				ver["presetRows"] = b.presetRows
			}
			_ = json.NewEncoder(w).Encode(ver)
		case r.Method == http.MethodPost && r.URL.Path == "/api/box/sync-presets":
			b.syncs++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodPut && r.URL.Path == presetAPIPath:
			b.bulkPuts++
			if !b.bulk {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var set []Preset
			_ = json.NewDecoder(r.Body).Decode(&set)
			in := map[int]bool{}
			for _, p := range set {
				in[p.Slot] = true
			}
			for _, other := range b.store {
				if in[other.Slot] {
					continue
				}
				for _, p := range set {
					if p.StreamURL == other.StreamURL {
						conflict(w, other.Name, other.Slot, p.Slot)
						return
					}
				}
			}
			for _, p := range set {
				b.store[p.Slot] = p
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, presetAPIPath+"/"):
			b.slotPuts++
			slot, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, presetAPIPath+"/"))
			var p Preset
			_ = json.NewDecoder(r.Body).Decode(&p)
			for _, other := range b.store {
				if other.Slot != slot && other.StreamURL == p.StreamURL {
					conflict(w, other.Name, other.Slot, 0)
					return
				}
			}
			b.store[slot] = p
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	l, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Fatalf("listen on %s: %v", host, err)
	}
	b.srv.Listener.Close()
	b.srv.Listener = l
	b.srv.Start()
	t.Cleanup(b.srv.Close)
	return b
}

func (b *keyBox) port(t *testing.T) int { return listenPort(t, b.srv) }

func (b *keyBox) names() map[int]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[int]string{}
	for s, p := range b.store {
		out[s] = p.Name
	}
	return out
}

func radio(slot int, name string) Preset {
	return Preset{Slot: slot, Name: name, Type: "radio", StreamURL: "http://example.com/" + name}
}

// TestCopyOneKeyLeavesTheOtherKeysAlone is #1551: copying key 2 puts that
// station on key 2 of the target and touches nothing else there.
func TestCopyOneKeyLeavesTheOtherKeysAlone(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{radio(1, "A"), radio(2, "B"), radio(3, "C")})
	dst := newKeyBox(t, "127.0.0.3", []Preset{radio(1, "X"), radio(2, "Y"), radio(4, "Z")})
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	res, err := a.CopyPresetKeysAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t), []int{2})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if !reflect.DeepEqual(res.Copied, []int{2}) || len(res.Skipped) != 0 {
		t.Errorf("result = %+v, want key 2 copied and nothing skipped", res)
	}
	want := map[int]string{1: "X", 2: "B", 4: "Z"}
	if got := dst.names(); !reflect.DeepEqual(got, want) {
		t.Errorf("target holds %v, want %v", got, want)
	}
	if dst.syncs != 0 {
		t.Errorf("the copy re-synced all hardware keys (%d), it must touch only the chosen one", dst.syncs)
	}
}

// TestCopyKeysSkipsADuplicateAndCopiesTheRest: the station of key 1 already
// sits on key 5 of the target. Key 1 is left out and named, key 3 still goes.
func TestCopyKeysSkipsADuplicateAndCopiesTheRest(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{radio(1, "A"), radio(2, "B"), radio(3, "C")})
	dst := newKeyBox(t, "127.0.0.3", []Preset{radio(5, "A")})
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	res, err := a.CopyPresetKeysAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t), []int{1, 3})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if !reflect.DeepEqual(res.Copied, []int{3}) {
		t.Errorf("copied = %v, want [3]", res.Copied)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Slot != 1 || res.Skipped[0].OnSlot != 5 || res.Skipped[0].Name != "A" {
		t.Errorf("skipped = %+v, want key 1 (A) skipped because it is on key 5", res.Skipped)
	}
	want := map[int]string{3: "C", 5: "A"}
	if got := dst.names(); !reflect.DeepEqual(got, want) {
		t.Errorf("target holds %v, want %v (nothing deleted to make room)", got, want)
	}
}

// TestCopyKeysFindsTheDuplicateWithoutPayloadSlot: an agent that predates
// payloadSlot names only the station; the key is found by its name.
func TestCopyKeysFindsTheDuplicateWithoutPayloadSlot(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{radio(1, "A"), radio(2, "B")})
	dst := newKeyBox(t, "127.0.0.3", []Preset{radio(6, "B")})
	dst.payloadSlot = false
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	res, err := a.CopyPresetKeysAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t), []int{1, 2})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if !reflect.DeepEqual(res.Copied, []int{1}) || len(res.Skipped) != 1 || res.Skipped[0].Slot != 2 || res.Skipped[0].OnSlot != 6 {
		t.Errorf("result = %+v, want key 1 copied and key 2 skipped (on key 6)", res)
	}
}

// TestCopyKeysSwapIsNoCollision: two chosen keys that swap places on the target
// are judged on the finished result, as in the whole-set copy (#882).
func TestCopyKeysSwapIsNoCollision(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{radio(1, "A"), radio(2, "B")})
	dst := newKeyBox(t, "127.0.0.3", []Preset{radio(1, "B"), radio(2, "A"), radio(3, "C")})
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	res, err := a.CopyPresetKeysAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t), []int{1, 2})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if !reflect.DeepEqual(res.Copied, []int{1, 2}) || len(res.Skipped) != 0 {
		t.Errorf("result = %+v, want both keys copied", res)
	}
	if dst.bulkPuts != 1 {
		t.Errorf("bulk writes = %d, want one", dst.bulkPuts)
	}
}

// TestCopyKeysPerSlotOnAnOlderAgent: an agent without the bulk endpoint gets
// one write per chosen key, each with its own answer.
func TestCopyKeysPerSlotOnAnOlderAgent(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{radio(1, "A"), radio(2, "B"), radio(3, "C")})
	dst := newKeyBox(t, "127.0.0.3", []Preset{radio(4, "B")})
	dst.bulk = false
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	res, err := a.CopyPresetKeysAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t), []int{1, 2})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if !reflect.DeepEqual(res.Copied, []int{1}) || len(res.Skipped) != 1 || res.Skipped[0].OnSlot != 4 {
		t.Errorf("result = %+v, want key 1 copied and key 2 skipped (on key 4)", res)
	}
	if dst.slotPuts != 2 {
		t.Errorf("per-slot writes = %d, want 2 (key 3 was not chosen)", dst.slotPuts)
	}
	if _, ok := dst.names()[3]; ok {
		t.Error("an unchosen key was written")
	}
}

// TestCopyKeysPressedTwiceToAOneRowTarget: a chosen key of the second row does
// not fit a target that keeps one row; it is reported, the rest goes.
func TestCopyKeysPressedTwiceToAOneRowTarget(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{radio(1, "A"), radio(8, "Row2")})
	dst := newKeyBox(t, "127.0.0.3", nil)
	installProbe(t, func() bool { return true })

	a := copyApp(t)
	res, err := a.CopyPresetKeysAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t), []int{1, 8})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if !reflect.DeepEqual(res.Copied, []int{1}) || !reflect.DeepEqual(res.NotKept, []int{8}) {
		t.Errorf("result = %+v, want key 1 copied and key 8 not kept", res)
	}

	dst2 := newKeyBox(t, "127.0.0.4", nil)
	dst2.presetRows = "2"
	res, err = a.CopyPresetKeysAcrossBoxes(src.host, src.port(t), dst2.host, dst2.port(t), []int{8})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if !reflect.DeepEqual(res.Copied, []int{8}) || dst2.names()[8] != "Row2" {
		t.Errorf("result = %+v, target %v, want key 8 copied onto the second row", res, dst2.names())
	}
}

// TestCopyKeysRefusesNoChoice: an empty choice copies nothing and says so.
func TestCopyKeysRefusesNoChoice(t *testing.T) {
	a := copyApp(t)
	if _, err := a.CopyPresetKeysAcrossBoxes("127.0.0.2", 1, "127.0.0.3", 1, nil); err == nil {
		t.Fatal("an empty key choice was accepted")
	}
}

// TestConflictIndex pins how a bulk refusal is matched to a chosen key.
func TestConflictIndex(t *testing.T) {
	pending := []Preset{radio(1, "A"), radio(2, "B"), radio(3, "b ")}
	cases := []struct {
		c    presetConflictError
		want int
	}{
		{presetConflictError{Name: "whatever", Slot: 5, PayloadSlot: 2}, 1},
		{presetConflictError{Name: "A", Slot: 5}, 0},
		{presetConflictError{Name: "B", Slot: 5}, -1}, // two keys match: do not guess
		{presetConflictError{Name: "nope", Slot: 5}, -1},
	}
	for _, c := range cases {
		if got := conflictIndex(pending, &c.c); got != c.want {
			t.Errorf("conflictIndex(%+v) = %d, want %d", c.c, got, c.want)
		}
	}
	if got := conflictIndex(pending[:1], &presetConflictError{Name: "other name", Slot: 4}); got != 0 {
		t.Errorf("a single pending key = %d, want 0", got)
	}
}

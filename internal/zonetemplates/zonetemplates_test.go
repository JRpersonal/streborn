package zonetemplates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// newStore returns a store persisting into a fresh temp dir plus its path.
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zone-templates.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q): %v", path, err)
	}
	return s, path
}

// tpl builds a valid template with one member per device id.
func tpl(name string, deviceIDs ...string) Template {
	if len(deviceIDs) == 0 {
		deviceIDs = []string{"device-id-here"}
	}
	members := make([]Member, 0, len(deviceIDs))
	for i, id := range deviceIDs {
		members = append(members, Member{DeviceID: id, IP: "192.0.2." + string(rune('1'+i))})
	}
	return Template{Name: name, Master: members[0], Members: members}
}

// mustUpsert creates or replaces a template, failing the test on error.
func mustUpsert(t *testing.T, s *Store, in Template) Template {
	t.Helper()
	got, err := s.Upsert(in)
	if err != nil {
		t.Fatalf("Upsert(%q): %v", in.Name, err)
	}
	return got
}

// removeFile deletes the store file so a later write is detectable: a no-op
// call must not resurrect the file, a real save recreates it. This detects
// even a rewrite of identical bytes, which a content comparison cannot.
func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove store file: %v", err)
	}
}

func assertNoWrite(t *testing.T, path, op string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s wrote the store file, want no write (stat err: %v)", op, err)
	}
}

func assertWrote(t *testing.T, path, op string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s did not write the store file: %v", op, err)
	}
}

func TestLoadLenient(t *testing.T) {
	tests := []struct {
		name    string
		content *string // nil = do not create the file
		wantErr bool
	}{
		{name: "missing file", content: nil, wantErr: false},
		{name: "empty file", content: strPtr(""), wantErr: false},
		{name: "whitespace file", content: strPtr(" \n\t"), wantErr: false},
		{name: "garbage file", content: strPtr("{not json"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "zone-templates.json")
			if tc.content != nil {
				if err := os.WriteFile(path, []byte(*tc.content), 0o644); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}
			s, err := Load(path)
			if tc.wantErr && err == nil {
				t.Fatalf("Load: want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Load: %v", err)
			}
			if s == nil {
				t.Fatalf("Load: store is nil")
			}
			if got := s.List(); len(got) != 0 {
				t.Fatalf("List on empty store = %v, want empty", got)
			}
			if got := s.OutList(); len(got) != 0 {
				t.Fatalf("OutList on empty store = %v, want empty", got)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func TestUpsertCreate(t *testing.T) {
	s, _ := newStore(t)
	in := tpl("  Kitchen  ", "dev-a", "dev-b")
	in.Permanent = true // must be ignored: SetPermanent is the only way
	got := mustUpsert(t, s, in)

	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(got.ID) {
		t.Errorf("ID = %q, want 8 hex chars", got.ID)
	}
	if got.Name != "Kitchen" {
		t.Errorf("Name = %q, want trimmed %q", got.Name, "Kitchen")
	}
	if got.Permanent {
		t.Errorf("create must force Permanent=false")
	}
	for _, ts := range []string{got.CreatedAt, got.UpdatedAt} {
		when, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			t.Errorf("timestamp %q not RFC3339: %v", ts, err)
		} else if when.Location() != time.UTC {
			t.Errorf("timestamp %q not UTC", ts)
		}
	}
	stored, ok := s.Get(got.ID)
	if !ok {
		t.Fatalf("Get(%q): not found after create", got.ID)
	}
	if stored.Name != "Kitchen" || len(stored.Members) != 2 {
		t.Errorf("stored = %+v, want name Kitchen with 2 members", stored)
	}
}

func TestUpsertValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Template)
		wantErr bool
	}{
		{"valid", func(*Template) {}, false},
		{"empty name", func(t *Template) { t.Name = "" }, true},
		{"whitespace name", func(t *Template) { t.Name = "   " }, true},
		{"48-char name ok", func(t *Template) { t.Name = strings.Repeat("a", 48) }, false},
		{"49-char name", func(t *Template) { t.Name = strings.Repeat("a", 49) }, true},
		{"zero members", func(t *Template) { t.Members = nil }, true},
		{"nine members", func(t *Template) {
			t.Members = nil
			for i := 0; i < 9; i++ {
				t.Members = append(t.Members, Member{DeviceID: "dev", IP: "192.0.2.1"})
			}
		}, true},
		{"member without deviceID", func(t *Template) {
			t.Members = []Member{{DeviceID: " ", IP: "192.0.2.1"}}
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			in := tpl("Valid Name", "dev-a")
			tc.mutate(&in)
			_, err := s.Upsert(in)
			if tc.wantErr && err == nil {
				t.Fatalf("Upsert: want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Upsert: %v", err)
			}
		})
	}
}

func TestUpsertMaxTemplates(t *testing.T) {
	s, _ := newStore(t)
	for i := 0; i < 12; i++ {
		mustUpsert(t, s, tpl("Group "+string(rune('A'+i)), "dev"))
	}
	if _, err := s.Upsert(tpl("The Thirteenth", "dev")); err == nil {
		t.Fatalf("13th template accepted, want error")
	}
	// Replacing an existing one must still work at the cap.
	if _, err := s.Upsert(tpl("group a", "dev-other")); err != nil {
		t.Fatalf("replace at cap: %v", err)
	}
}

func TestUpsertByNameReplaces(t *testing.T) {
	s, _ := newStore(t)
	orig := mustUpsert(t, s, tpl("Living Room", "dev-a"))
	if err := s.SetPermanent(orig.ID, true); err != nil {
		t.Fatalf("SetPermanent: %v", err)
	}

	// Empty ID + case-insensitive name match replaces, keeping identity.
	got := mustUpsert(t, s, tpl("living room", "dev-b", "dev-c"))
	if got.ID != orig.ID {
		t.Errorf("ID = %q, want kept %q", got.ID, orig.ID)
	}
	if got.CreatedAt != orig.CreatedAt {
		t.Errorf("CreatedAt = %q, want kept %q", got.CreatedAt, orig.CreatedAt)
	}
	if !got.Permanent {
		t.Errorf("replace dropped the Permanent flag")
	}
	if len(got.Members) != 2 || got.Members[0].DeviceID != "dev-b" {
		t.Errorf("Members = %+v, want the replacement members", got.Members)
	}
	if all := s.List(); len(all) != 1 {
		t.Errorf("List has %d templates after replace, want 1", len(all))
	}
}

func TestUpsertByIDUnknown(t *testing.T) {
	s, _ := newStore(t)
	in := tpl("Anything", "dev")
	in.ID = "deadbeef"
	if _, err := s.Upsert(in); err == nil {
		t.Fatalf("Upsert with unknown ID accepted, want error")
	}
}

func TestUpsertIdenticalSkipsWrite(t *testing.T) {
	s, path := newStore(t)
	got := mustUpsert(t, s, tpl("Kitchen", "dev-a"))

	removeFile(t, path)
	same := tpl("Kitchen", "dev-a")
	res, err := s.Upsert(same)
	if err != nil {
		t.Fatalf("identical Upsert: %v", err)
	}
	assertNoWrite(t, path, "identical Upsert")
	if res.UpdatedAt != got.UpdatedAt {
		t.Errorf("identical Upsert bumped UpdatedAt %q -> %q", got.UpdatedAt, res.UpdatedAt)
	}

	mustUpsert(t, s, tpl("Kitchen", "dev-a", "dev-b"))
	assertWrote(t, path, "changed Upsert")
}

func TestSetPermanentSingleInvariant(t *testing.T) {
	s, _ := newStore(t)
	a := mustUpsert(t, s, tpl("A", "dev"))
	b := mustUpsert(t, s, tpl("B", "dev"))
	c := mustUpsert(t, s, tpl("C", "dev"))

	if _, ok := s.Permanent(); ok {
		t.Fatalf("fresh store already has a permanent template")
	}
	if err := s.SetPermanent("nope", true); err == nil {
		t.Fatalf("SetPermanent(unknown) accepted, want error")
	}

	for _, id := range []string{a.ID, b.ID, c.ID, a.ID} {
		if err := s.SetPermanent(id, true); err != nil {
			t.Fatalf("SetPermanent(%q, true): %v", id, err)
		}
		perm, ok := s.Permanent()
		if !ok || perm.ID != id {
			t.Fatalf("Permanent() = (%+v, %v), want id %q", perm, ok, id)
		}
		count := 0
		for _, tp := range s.List() {
			if tp.Permanent {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%d templates permanent after SetPermanent(%q, true), want exactly 1", count, id)
		}
	}

	if err := s.SetPermanent(a.ID, false); err != nil {
		t.Fatalf("SetPermanent(false): %v", err)
	}
	if _, ok := s.Permanent(); ok {
		t.Fatalf("permanent template still present after switching it off")
	}
}

func TestSetPermanentClearsOutBothDirections(t *testing.T) {
	s, _ := newStore(t)
	a := mustUpsert(t, s, tpl("A", "dev"))

	// Direction on -> off.
	if err := s.SetPermanent(a.ID, true); err != nil {
		t.Fatalf("SetPermanent(true): %v", err)
	}
	if err := s.AddOut("dev-x", "192.0.2.9", "user-removed"); err != nil {
		t.Fatalf("AddOut: %v", err)
	}
	if err := s.SetPermanent(a.ID, false); err != nil {
		t.Fatalf("SetPermanent(false): %v", err)
	}
	if got := s.OutList(); len(got) != 0 {
		t.Fatalf("out list after SetPermanent(false) = %v, want empty", got)
	}

	// Direction off -> on.
	if err := s.AddOut("dev-y", "192.0.2.10", "self-play"); err != nil {
		t.Fatalf("AddOut: %v", err)
	}
	if err := s.SetPermanent(a.ID, true); err != nil {
		t.Fatalf("SetPermanent(true): %v", err)
	}
	if got := s.OutList(); len(got) != 0 {
		t.Fatalf("out list after SetPermanent(true) = %v, want empty", got)
	}
}

func TestSetPermanentNoChangeNoWrite(t *testing.T) {
	s, path := newStore(t)
	a := mustUpsert(t, s, tpl("A", "dev"))
	b := mustUpsert(t, s, tpl("B", "dev"))
	if err := s.SetPermanent(a.ID, true); err != nil {
		t.Fatalf("SetPermanent: %v", err)
	}

	removeFile(t, path)
	if err := s.SetPermanent(a.ID, true); err != nil { // already the sole permanent
		t.Fatalf("repeat SetPermanent(true): %v", err)
	}
	assertNoWrite(t, path, "repeat SetPermanent(true)")
	if err := s.SetPermanent(b.ID, false); err != nil { // already off
		t.Fatalf("repeat SetPermanent(false): %v", err)
	}
	assertNoWrite(t, path, "repeat SetPermanent(false)")
}

func TestDeleteClearsOutForPermanent(t *testing.T) {
	s, _ := newStore(t)
	perm := mustUpsert(t, s, tpl("Permanent", "dev"))
	other := mustUpsert(t, s, tpl("Other", "dev"))
	if err := s.SetPermanent(perm.ID, true); err != nil {
		t.Fatalf("SetPermanent: %v", err)
	}
	if err := s.AddOut("dev-x", "192.0.2.9", "user-removed"); err != nil {
		t.Fatalf("AddOut: %v", err)
	}

	if _, err := s.Delete("nope"); err == nil {
		t.Fatalf("Delete(unknown) accepted, want error")
	}

	wasPerm, err := s.Delete(other.ID)
	if err != nil {
		t.Fatalf("Delete(other): %v", err)
	}
	if wasPerm {
		t.Errorf("Delete(other) reported permanent")
	}
	if got := s.OutList(); len(got) != 1 {
		t.Fatalf("out list after deleting a non-permanent template = %v, want kept", got)
	}

	wasPerm, err = s.Delete(perm.ID)
	if err != nil {
		t.Fatalf("Delete(permanent): %v", err)
	}
	if !wasPerm {
		t.Errorf("Delete(permanent) did not report permanent")
	}
	if got := s.OutList(); len(got) != 0 {
		t.Fatalf("out list after deleting the permanent template = %v, want empty", got)
	}
	if _, ok := s.Get(perm.ID); ok {
		t.Errorf("deleted template still present")
	}
}

func TestOutAddRemoveIdempotentNoWrite(t *testing.T) {
	s, path := newStore(t)
	if err := s.AddOut("Dev-A", "192.0.2.5", "user-removed"); err != nil {
		t.Fatalf("AddOut: %v", err)
	}
	assertWrote(t, path, "first AddOut")

	removeFile(t, path)
	// Same deviceID in a different case, different IP: matches, so no write.
	if err := s.AddOut("dev-a", "192.0.2.99", "self-play"); err != nil {
		t.Fatalf("repeat AddOut by deviceID: %v", err)
	}
	assertNoWrite(t, path, "repeat AddOut by deviceID")
	// Same IP, different deviceID: matches, so no write.
	if err := s.AddOut("dev-other", "192.0.2.5", "self-play"); err != nil {
		t.Fatalf("repeat AddOut by IP: %v", err)
	}
	assertNoWrite(t, path, "repeat AddOut by IP")
	if got := s.OutList(); len(got) != 1 {
		t.Fatalf("out list = %v, want the single original entry", got)
	}

	// RemoveOut with no match: no write.
	if err := s.RemoveOut("dev-unknown", "192.0.2.200"); err != nil {
		t.Fatalf("RemoveOut(no match): %v", err)
	}
	assertNoWrite(t, path, "RemoveOut without a match")

	// A real removal writes and drops the entry.
	if err := s.RemoveOut("DEV-A", ""); err != nil {
		t.Fatalf("RemoveOut: %v", err)
	}
	assertWrote(t, path, "matching RemoveOut")
	if got := s.OutList(); len(got) != 0 {
		t.Fatalf("out list after RemoveOut = %v, want empty", got)
	}

	// ClearOut on an already-empty list: no write.
	removeFile(t, path)
	if err := s.ClearOut(); err != nil {
		t.Fatalf("ClearOut(empty): %v", err)
	}
	assertNoWrite(t, path, "ClearOut on empty list")
}

func TestIsOutMatching(t *testing.T) {
	s, _ := newStore(t)
	if err := s.AddOut("AABBCC", "192.0.2.5", "user-removed"); err != nil {
		t.Fatalf("AddOut: %v", err)
	}
	if err := s.AddOut("no-ip-entry", "", "self-play"); err != nil {
		t.Fatalf("AddOut: %v", err)
	}

	tests := []struct {
		name     string
		deviceID string
		ip       string
		want     bool
	}{
		{"same IP, different deviceID", "totally-else", "192.0.2.5", true},
		{"same deviceID lower-case, different IP", "aabbcc", "192.0.2.77", true},
		{"same deviceID upper-case, no IP", "AABBCC", "", true},
		{"both different", "other", "192.0.2.77", false},
		{"both empty", "", "", false},
		{"empty IP does not match entry with empty IP", "other", "", false},
		{"entry without IP still matches by deviceID", "NO-IP-ENTRY", "192.0.2.200", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.IsOut(tc.deviceID, tc.ip); got != tc.want {
				t.Errorf("IsOut(%q, %q) = %v, want %v", tc.deviceID, tc.ip, got, tc.want)
			}
		})
	}
}

func TestListSortedAndDetached(t *testing.T) {
	s, _ := newStore(t)
	mustUpsert(t, s, tpl("banana", "dev"))
	apple := mustUpsert(t, s, tpl("Apple", "dev"))
	mustUpsert(t, s, tpl("cherry", "dev"))

	list := s.List()
	var names []string
	for _, tp := range list {
		names = append(names, tp.Name)
	}
	want := []string{"Apple", "banana", "cherry"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("List order = %v, want %v (case-insensitive by name)", names, want)
		}
	}

	// Mutating the returned slice and its members must not affect the store.
	list[0].Name = "Zebra"
	list[0].Members[0].DeviceID = "hacked"
	got, ok := s.Get(apple.ID)
	if !ok {
		t.Fatalf("Get(%q): not found", apple.ID)
	}
	if got.Name != "Apple" || got.Members[0].DeviceID != "dev" {
		t.Errorf("store changed through the List copy: %+v", got)
	}

	// Get must hand out a deep copy too.
	got.Members[0].DeviceID = "hacked-again"
	again, _ := s.Get(apple.ID)
	if again.Members[0].DeviceID != "dev" {
		t.Errorf("store changed through the Get copy: %+v", again)
	}
}

func TestPersistRoundtrip(t *testing.T) {
	s, path := newStore(t)
	a := mustUpsert(t, s, tpl("A", "dev-a"))
	mustUpsert(t, s, tpl("B", "dev-b"))
	if err := s.SetPermanent(a.ID, true); err != nil {
		t.Fatalf("SetPermanent: %v", err)
	}
	if err := s.AddOut("dev-x", "192.0.2.9", "self-play"); err != nil {
		t.Fatalf("AddOut: %v", err)
	}

	re, err := Load(path)
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	if got := re.List(); len(got) != 2 {
		t.Fatalf("re-loaded List has %d templates, want 2", len(got))
	}
	perm, ok := re.Permanent()
	if !ok || perm.ID != a.ID {
		t.Fatalf("re-loaded Permanent() = (%+v, %v), want id %q", perm, ok, a.ID)
	}
	out := re.OutList()
	if len(out) != 1 || out[0].DeviceID != "dev-x" || out[0].Reason != "self-play" {
		t.Fatalf("re-loaded OutList = %+v, want the persisted entry", out)
	}
}

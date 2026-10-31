package spotify

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// truncatedState is the shape a power cut leaves: go-librespot's state.json cut
// off part way through its write.
const truncatedState = `{"device_id":"device-id-here","last_volume":42,"credentials":{"username":"user-id-here","da`

// The field case (ST300, v1.1.3): state.json cut off mid-write, a copy of the
// login in the account store. The engine must get a parseable file back that
// still logs in, and the damaged bytes must be kept.
func TestDamagedStateIsRepairedFromTheLoginCopy(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	cred := storedCredential{Username: "user-id-here", Data: []byte("good-blob")}
	writeJSON(t, filepath.Join(m.credStore, sanitizeUser(cred.Username)+".json"), cred)
	statePath := filepath.Join(m.configDir, "state.json")
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(truncatedState), 0o600); err != nil {
		t.Fatal(err)
	}

	if !m.repairCorruptState() {
		t.Fatal("a damaged state file was not repaired")
	}
	got, ok := m.readStateCredential()
	if !ok || got.Username != cred.Username || string(got.Data) != string(cred.Data) {
		t.Fatalf("the login was not restored into state.json: %+v ok=%v", got, ok)
	}
	backup, err := os.ReadFile(filepath.Join(m.configDir, corruptStateFile))
	if err != nil || string(backup) != truncatedState {
		t.Fatalf("the damaged bytes were not kept: %q err=%v", backup, err)
	}
	if snap := m.CredentialSnapshot(); snap["stateLoginRestored"] != true || snap["stateRepairedAt"] == "" {
		t.Fatalf("the repair is not visible in spotify_auth: %v", snap)
	}
}

// No login copy to restore from: the file goes, so the engine starts fresh and
// waits for a tap instead of dying on every start.
func TestDamagedStateWithoutACopyIsRemoved(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	statePath := filepath.Join(m.configDir, "state.json")
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if !m.repairCorruptState() {
		t.Fatal("an empty state file was not treated as damaged")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the damaged state file is still in the engine's way: %v", err)
	}
	if snap := m.CredentialSnapshot(); snap["stateLoginRestored"] != false {
		t.Fatalf("a repair without a copy claims a restored login: %v", snap)
	}
}

// A healthy or missing state file is never touched.
func TestHealthyOrMissingStateIsLeftAlone(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	if m.repairCorruptState() {
		t.Fatal("a missing state file was repaired")
	}
	seedDeadLogin(t, m)
	before, _ := os.ReadFile(filepath.Join(m.configDir, "state.json"))
	if m.repairCorruptState() {
		t.Fatal("a healthy state file was repaired")
	}
	after, _ := os.ReadFile(filepath.Join(m.configDir, "state.json"))
	if string(before) != string(after) {
		t.Fatal("a healthy state file was rewritten")
	}
	if _, err := os.Stat(filepath.Join(m.configDir, corruptStateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a backup was written for a healthy file")
	}
}

// With several accounts, the newest copy wins, and the login Spotify already
// refused is never put back even when it is the newest.
func TestRepairPicksTheNewestUsableCopy(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	old := storedCredential{Username: "older-user", Data: []byte("old-blob")}
	newer := storedCredential{Username: "newer-user", Data: []byte("new-blob")}
	refused := storedCredential{Username: "refused-user", Data: []byte("dead-blob")}
	for i, c := range []storedCredential{old, newer, refused} {
		p := filepath.Join(m.credStore, sanitizeUser(c.Username)+".json")
		writeJSON(t, p, c)
		at := time.Now().Add(time.Duration(i-3) * time.Hour)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(refused)
	if err := os.MkdirAll(m.configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.configDir, rejectedCredentialFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.configDir, "state.json"), []byte(truncatedState), 0o600); err != nil {
		t.Fatal(err)
	}

	m.repairCorruptState()
	got, ok := m.readStateCredential()
	if !ok || got.Username != newer.Username {
		t.Fatalf("restored %q, want the newest usable copy %q", got.Username, newer.Username)
	}
}

package spotify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Remembering what a Spotify account is CALLED, so the apps never have to print
// what it is numbered.
//
// A modern Spotify account has no username a person chose. What the engine
// reports, and what a preset is therefore saved under, is the canonical user id:
// an opaque run of letters and digits that means nothing to the owner and that
// resolves to a public profile page. Printed on a preset tile it told the owner
// nothing and told a stranger something.
//
// The display name is already within reach and was being thrown away. STR makes
// exactly one Spotify Web API call, /v1/me, to find out whether the account is
// Premium, and that same answer carries display_name. So this costs no request,
// no timer and no extra permission: wherever the Premium check works, the name
// works.
//
// It is remembered rather than looked up, which is the whole design and came
// from the maintainer: once the engine has played as an account, the name is
// known, so keep it. After one successful moment the name survives the engine
// being logged in as somebody else, Spotify being unreachable or rate-limiting,
// and a reboot. Without that, a preset saved by the household's other account
// could never show a name, because its token is not the one in hand.
//
// The id stays the identity. It is what go-librespot logs in with, what the
// credential copies in credStore are named by, and what a preset must carry to
// be recalled under the right account. The name is a label hanging off it, never
// a key.

// accountNamesFile sits beside the per-account credential copies, because it is
// the same fact about the same accounts. Its own file, not inside them: those
// are secrets at 0600 and this is a label.
const accountNamesFile = "names.json"

// maxAccountNameLen is a sanity bound on something that arrives from the
// network and ends up on screen. Spotify display names are short; anything
// longer is not a name and has no business in the store.
const maxAccountNameLen = 64

func (m *Manager) accountNamesPath() string {
	return filepath.Join(m.credStore, accountNamesFile)
}

// loadAccountNames reads the store once. A missing or unreadable file is the
// normal state on a speaker that has never seen Spotify answer, and it means
// "no names known", never an error worth surfacing.
func (m *Manager) loadAccountNames() {
	if m.accountNames != nil {
		return
	}
	m.accountNames = map[string]string{}
	data, err := os.ReadFile(m.accountNamesPath())
	if err != nil {
		return
	}
	var got map[string]string
	if json.Unmarshal(data, &got) != nil {
		return
	}
	for id, name := range got {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if name = cleanAccountName(name); name != "" {
			m.accountNames[id] = name
		}
	}
}

// cleanAccountName makes a display name fit to store and to draw: one line, no
// surrounding space, bounded. Returns "" for anything that is not a name.
func cleanAccountName(v string) string {
	v = strings.TrimSpace(v)
	// A newline in a label would break a log line and a tile alike, and nothing
	// legitimate carries one.
	if i := strings.IndexAny(v, "\r\n"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if len([]rune(v)) > maxAccountNameLen {
		v = string([]rune(v)[:maxAccountNameLen])
	}
	return v
}

// rememberAccountName records what an account is called, and writes to the NAND
// only when that is news.
//
// The speaker writes this file at most once per account, ever: the guard below
// is not an optimisation but the reason this is allowed to exist on hardware
// whose flash is the part that wears out.
func (m *Manager) rememberAccountName(id, name string) {
	id = strings.TrimSpace(id)
	name = cleanAccountName(name)
	if id == "" || name == "" {
		return
	}
	m.mu.Lock()
	m.loadAccountNames()
	if m.accountNames[id] == name {
		m.mu.Unlock()
		return
	}
	m.accountNames[id] = name
	snapshot := make(map[string]string, len(m.accountNames))
	for k, v := range m.accountNames {
		snapshot[k] = v
	}
	m.mu.Unlock()

	if err := os.MkdirAll(m.credStore, 0o755); err != nil {
		return
	}
	blob, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	// Written through a temporary file: a half-written names.json would be
	// discarded by loadAccountNames on the next boot, and with it every name
	// ever learned, for a label that was only ever an improvement.
	tmp := m.accountNamesPath() + ".tmp"
	if os.WriteFile(tmp, blob, 0o644) != nil {
		return
	}
	if os.Rename(tmp, m.accountNamesPath()) != nil {
		_ = os.Remove(tmp)
		return
	}
	// The NAME is not logged. It is the personal part, and this file exists so
	// that it has exactly one place to be. The id is enough to read the line by.
	m.logger.Info("spotify: learned what an account is called", "user", id)
}

// AccountNames returns a copy of what is known, id to display name, for the apps
// to draw with. Empty on a speaker that has never had an answer from Spotify,
// and the apps then print nothing at all rather than an id.
func (m *Manager) AccountNames() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadAccountNames()
	out := make(map[string]string, len(m.accountNames))
	for k, v := range m.accountNames {
		out[k] = v
	}
	return out
}

// AccountNamesSorted is AccountNames with a stable order, for tests and for a
// log line that has to be the same twice.
func (m *Manager) AccountNamesSorted() []string {
	names := m.AccountNames()
	ids := make([]string, 0, len(names))
	for id := range names {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

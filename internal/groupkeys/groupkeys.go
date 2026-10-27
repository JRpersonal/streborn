// Package groupkeys puts a saved multiroom group on a thumbs key of the
// remote (#863): one press forms the group, the next press dissolves it.
//
// A template names the group (main speaker, members, permanent flag). The
// bindings live on the speaker whose remote is pressed, because a remote press
// is only seen by the speaker it points at, so the document is per speaker and
// written only when the app saves it. A press writes nothing to NAND: it reads
// the main speaker's live zone and reuses the form and dissolve endpoints the
// desktop app calls, over HTTP, on the main speaker's agent (or on this agent
// itself, over loopback, when this speaker is the main speaker).
//
// First cut, deliberately without a feedback layer: no display text, no spoken
// announcement. The members joining or going quiet is the feedback.
package groupkeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
)

// Key ids a template can be bound to. They are the webhooks trigger ids of the
// same keys, so one id names the key across both features.
const (
	KeyThumbsUp   = "thumbsUp"
	KeyThumbsDown = "thumbsDown"
)

// BindableKey reports whether id is a key a template may be bound to.
func BindableKey(id string) bool {
	return id == KeyThumbsUp || id == KeyThumbsDown
}

// Limits on the document, so a malformed save cannot grow the NAND file.
const (
	MaxTemplates = 12
	MaxMembers   = 16
	MaxNameLen   = 60
)

// Member is one speaker of a template: the deviceID the firmware knows it by
// and its last known LAN address. Name is a display label for the app only.
type Member struct {
	DeviceID string `json:"deviceID"`
	IP       string `json:"ip,omitempty"`
	Name     string `json:"name,omitempty"`
}

// Template is one saved group.
type Template struct {
	Name      string   `json:"name"`
	Master    Member   `json:"master"`
	Members   []Member `json:"members"`
	Permanent bool     `json:"permanent,omitempty"`
}

// Document is the whole per-speaker file: the templates and which key forms
// which of them (key id -> template name).
type Document struct {
	Templates []Template        `json:"templates"`
	Bindings  map[string]string `json:"bindings,omitempty"`
}

// Validate checks the document the app saved and normalises it (trimmed
// names, no nil slices). It returns the first problem as an error the app can
// show verbatim.
func (d *Document) Validate() error {
	if len(d.Templates) > MaxTemplates {
		return fmt.Errorf("at most %d templates", MaxTemplates)
	}
	seen := map[string]bool{}
	for i := range d.Templates {
		t := &d.Templates[i]
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			return fmt.Errorf("template %d has no name", i+1)
		}
		if len(t.Name) > MaxNameLen {
			return fmt.Errorf("template name %q is too long", t.Name)
		}
		if seen[strings.ToLower(t.Name)] {
			return fmt.Errorf("template name %q is used twice", t.Name)
		}
		seen[strings.ToLower(t.Name)] = true
		t.Master.DeviceID = strings.TrimSpace(t.Master.DeviceID)
		t.Master.IP = strings.TrimSpace(t.Master.IP)
		if t.Master.DeviceID == "" && t.Master.IP == "" {
			return fmt.Errorf("template %q has no main speaker", t.Name)
		}
		if len(t.Members) == 0 {
			return fmt.Errorf("template %q has no members", t.Name)
		}
		if len(t.Members) > MaxMembers {
			return fmt.Errorf("template %q has too many members", t.Name)
		}
		for j := range t.Members {
			m := &t.Members[j]
			m.DeviceID = strings.TrimSpace(m.DeviceID)
			m.IP = strings.TrimSpace(m.IP)
			if m.DeviceID == "" && m.IP == "" {
				return fmt.Errorf("template %q has an empty member", t.Name)
			}
		}
	}
	for key, name := range d.Bindings {
		if !BindableKey(key) {
			return fmt.Errorf("key %q cannot carry a group", key)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			delete(d.Bindings, key)
			continue
		}
		if !seen[strings.ToLower(name)] {
			return fmt.Errorf("key %q is bound to an unknown template %q", key, name)
		}
		d.Bindings[key] = name
	}
	if d.Templates == nil {
		d.Templates = []Template{}
	}
	return nil
}

// Template returns the template called name, case-insensitively.
func (d Document) Template(name string) (Template, bool) {
	for _, t := range d.Templates {
		if strings.EqualFold(t.Name, strings.TrimSpace(name)) {
			return t, true
		}
	}
	return Template{}, false
}

// LiveZone is what the main speaker's agent reports about its firmware zone.
// Master is the deviceID leading the zone ("" when standalone); Members are
// the followers. SelfID is the deviceID the main speaker's own firmware
// reports for itself, when the client could learn it: a two-chip chassis
// announces a different MAC over discovery than the one its firmware keys the
// zone on, so a template written from discovery may name the main speaker by
// the "wrong" id, and SelfID is what tells us it leads the zone anyway.
type LiveZone struct {
	Master  string
	SelfID  string
	Members []Member
}

// MasterClient is how a toggle reaches the main speaker's agent. The HTTP
// implementation lives in client.go; tests inject a fake.
type MasterClient interface {
	// LiveZone reads the main speaker's live zone (GET /api/box/zone).
	LiveZone(ctx context.Context, master Member) (LiveZone, error)
	// Form forms the template's group on its main speaker (POST /api/box/zone).
	// deferred is true when the answer says the group was stored for the next
	// play instead of wired now (a permanent template).
	Form(ctx context.Context, tpl Template) (deferred bool, err error)
	// Dissolve takes the group led by master apart (DELETE /api/box/zone).
	Dissolve(ctx context.Context, master Member) error
	// Idle reports whether the main speaker is NOT playing right now.
	Idle(ctx context.Context, master Member) (bool, error)
	// PlayLast brings the main speaker's last station back (POST
	// /api/box/power {"on":true}), waking it when needed.
	PlayLast(ctx context.Context, master Member) error
}

// Outcome names what a press did, for the log and the debug section.
type Outcome string

const (
	OutcomeUnbound   Outcome = "unbound"
	OutcomeBusy      Outcome = "ignored-busy"
	OutcomeRateLimit Outcome = "ignored-rate-limit"
	OutcomeFormed    Outcome = "formed"
	// OutcomeDeferred is a PERMANENT template whose form was stored rather
	// than wired: the agent defers a permanent group to the next play so
	// creating one never starts music. Reported apart from "formed" because
	// the two look identical in a log and completely different in the room.
	OutcomeDeferred  Outcome = "formed-deferred"
	OutcomeDissolved Outcome = "dissolved"
	OutcomeSwitched  Outcome = "switched"
	OutcomeFailed    Outcome = "failed"
)

// Kinds of a failed press, a stable code next to the reason text so the app
// can say what went wrong in the user's language instead of quoting Go error
// text into a translated sentence.
const (
	// KindMasterUnresponsive: the main speaker (or the Bose web server on it)
	// did not answer in time. Pressing again usually works.
	KindMasterUnresponsive = "master-unresponsive"
	// KindMemberUnresolved: a speaker of the template is not where the group
	// remembers it (addresses changed); saving the group again fixes it.
	KindMemberUnresolved = "member-unresolved"
	// KindFormRefused: the main speaker refused to form the group.
	KindFormRefused = "form-refused"
	// KindDissolveFailed: the group could not be taken apart.
	KindDissolveFailed = "dissolve-failed"
	// KindOther: anything else.
	KindOther = "other"
)

// errZoneRead prefixes a failed read of the main speaker's live zone.
var errZoneRead = errors.New("read the main speaker's group")

// kindError tags an error with its Kind without changing its text.
type kindError struct {
	kind string
	err  error
}

func (e *kindError) Error() string { return e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

func withKind(kind string, err error) error {
	if err == nil {
		return nil
	}
	return &kindError{kind: kind, err: err}
}

// errorKind classifies a failed press for the app.
func errorKind(err error) string {
	if err == nil {
		return ""
	}
	var ke *kindError
	if errors.As(err, &ke) {
		return ke.kind
	}
	msg := err.Error()
	switch {
	case errors.Is(err, errZoneRead),
		strings.Contains(msg, "is not answering"),
		strings.Contains(msg, "deadline exceeded"),
		strings.Contains(msg, "Client.Timeout"):
		return KindMasterUnresponsive
	case strings.HasPrefix(msg, "dissolve:"):
		return KindDissolveFailed
	case strings.HasPrefix(msg, "form:"):
		return KindFormRefused
	}
	return KindOther
}

// masterIsSelf reports whether the client reaches m over loopback, i.e. m is
// this speaker. A client that cannot say counts as "not self".
func masterIsSelf(c MasterClient, m Member) bool {
	sc, ok := c.(interface{ IsSelf(Member) bool })
	return ok && sc.IsSelf(m)
}

// toggleMinGap is the shortest distance between two presses of the same key
// that both act. A form or dissolve takes seconds, and a double press is
// almost always one intent.
const toggleMinGap = 3 * time.Second

// warnMinGap rate-limits the WARN line a chronically failing binding (main
// speaker unplugged) could otherwise repeat on every press.
const warnMinGap = time.Minute

// Action is the last thing a press did, for the debug section.
type Action struct {
	At       time.Time `json:"at"`
	Key      string    `json:"key"`
	Template string    `json:"template,omitempty"`
	Outcome  Outcome   `json:"outcome"`
	Error    string    `json:"error,omitempty"`
	// Kind classifies Error (one of the Kind* constants), so the app can
	// explain a refused press in the user's language.
	Kind string `json:"kind,omitempty"`
}

// Store is the NAND-persisted document plus the toggle state machine.
type Store struct {
	path   string
	logger *slog.Logger
	client MasterClient

	mu  sync.RWMutex
	doc Document

	// togMu guards the per-key in-flight flags and press stamps.
	togMu    sync.Mutex
	inFlight map[string]bool
	lastAt   map[string]time.Time
	lastWarn map[string]time.Time
	// formedBy remembers, per key, the template the key's last successful
	// press formed (cleared by a dissolve). It is what lets a press dissolve a
	// group that formed without a member that was offline at the time; see
	// templatePartlyLive. Memory only: after an agent restart the first press
	// on such a group forms again, the next one dissolves.
	formedBy map[string]string
	// ignoredLogged marks a key whose ignored press (busy or too soon) was
	// already logged since its last acting press, so a burst logs one line.
	ignoredLogged map[string]bool

	lastAction Action
	lastError  Action
}

// Load reads the document from path. A missing file is an empty document.
func Load(path string, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{
		path:     path,
		logger:   logger,
		inFlight: map[string]bool{},
		lastAt:   map[string]time.Time{},
		lastWarn: map[string]time.Time{},
		formedBy: map[string]string{},

		ignoredLogged: map[string]bool{},
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, fmt.Errorf("read group keys: %w", err)
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.doc); err != nil {
		return s, fmt.Errorf("parse group keys: %w", err)
	}
	if err := s.doc.Validate(); err != nil {
		// Keep what parsed: a template with a small defect must not take the
		// working ones down with it. The next save from the app rewrites it.
		logger.Warn("group keys: stored document has a defect, bindings may not all work", "err", err)
	}
	return s, nil
}

// SetClient wires the client a toggle talks to the main speaker with.
func (s *Store) SetClient(c MasterClient) {
	s.mu.Lock()
	s.client = c
	s.mu.Unlock()
}

// Get returns a copy of the document.
func (s *Store) Get() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneDoc(s.doc)
}

// Set validates, replaces and persists the document. The only NAND write in
// this package, and it happens on a save from the app, never on a press.
func (s *Store) Set(d Document) error {
	if err := d.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.doc = cloneDoc(d)
	s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	// Durable write (fsync + rename): a plain write can leave a 0-byte file
	// behind a standby power cut.
	return atomicfile.WriteFile(s.path, b, 0o644)
}

func cloneDoc(d Document) Document {
	out := Document{Templates: make([]Template, 0, len(d.Templates))}
	for _, t := range d.Templates {
		t.Members = append([]Member(nil), t.Members...)
		out.Templates = append(out.Templates, t)
	}
	if len(d.Bindings) > 0 {
		out.Bindings = make(map[string]string, len(d.Bindings))
		for k, v := range d.Bindings {
			out.Bindings[k] = v
		}
	}
	return out
}

// Bound reports whether key carries a template that exists, i.e. a press on
// it belongs to this package and not to the per-key webhook.
func (s *Store) Bound(key string) bool {
	if s == nil {
		return false
	}
	_, _, ok := s.binding(key)
	return ok
}

func (s *Store) binding(key string) (Template, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name, ok := s.doc.Bindings[key]
	if !ok || name == "" {
		return Template{}, "", false
	}
	t, ok := s.doc.Template(name)
	return t, name, ok
}

// Snapshot is the debug-section view of the store.
func (s *Store) Snapshot() any {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	doc := cloneDoc(s.doc)
	s.mu.RUnlock()
	s.togMu.Lock()
	la, le := s.lastAction, s.lastError
	s.togMu.Unlock()
	out := map[string]any{
		"templates": doc.Templates,
		"bindings":  doc.Bindings,
	}
	if !la.At.IsZero() {
		out["last_action"] = la
	}
	if !le.At.IsZero() {
		out["last_error"] = le
	}
	return out
}

// LastError returns the most recent failed press: how long ago it was, which
// key, and the reason, or ok=false when no press has failed.
//
// It exists because a refused press was previously INVISIBLE. The reason lived
// only in the debugState snapshot, so the owner pressed the key, nothing
// happened, and only a diagnostic bundle could explain why. Two users spent
// days re-saving a group that was never the problem (2026-09-26). The agent
// version response carries this now, so the app can say it out loud.
func (s *Store) LastError() (Action, bool) {
	if s == nil {
		return Action{}, false
	}
	s.togMu.Lock()
	defer s.togMu.Unlock()
	if s.lastError.At.IsZero() {
		return Action{}, false
	}
	return s.lastError, true
}

// Toggle is the press handler. It reads the main speaker's live zone and
// decides: the template's group is live -> dissolve it; another bound
// template's group is live -> dissolve that and form this one; otherwise ->
// form, and when the main speaker was idle, bring its last station back so
// the press ends in music. Presses that land while a form or dissolve for the
// same key runs, or within toggleMinGap of the previous press, are ignored.
func (s *Store) Toggle(ctx context.Context, key string) Outcome {
	tpl, name, ok := s.binding(key)
	if !ok {
		return OutcomeUnbound
	}
	s.togMu.Lock()
	if s.inFlight[key] {
		logIt := !s.ignoredLogged[key]
		s.ignoredLogged[key] = true
		s.togMu.Unlock()
		// INFO once per press episode: a bundle has to show what became of
		// each press, and "pressed" with no outcome read as a lost press.
		if logIt {
			s.logger.Info("group key: press ignored, the previous one is still running", "key", key, "template", name)
		}
		return OutcomeBusy
	}
	if last, ok := s.lastAt[key]; ok && time.Since(last) < toggleMinGap {
		logIt := !s.ignoredLogged[key]
		s.ignoredLogged[key] = true
		s.togMu.Unlock()
		if logIt {
			s.logger.Info("group key: press ignored, too soon after the previous one", "key", key, "template", name,
				"min_gap", toggleMinGap.String())
		}
		return OutcomeRateLimit
	}
	s.lastAt[key] = time.Now()
	s.inFlight[key] = true
	s.ignoredLogged[key] = false
	s.togMu.Unlock()
	defer func() {
		s.togMu.Lock()
		s.inFlight[key] = false
		s.togMu.Unlock()
	}()

	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return s.finish(key, name, OutcomeFailed, errors.New("no master client wired"))
	}

	outcome, err := s.act(ctx, client, key, tpl)
	return s.finish(key, name, outcome, err)
}

// act runs the state machine for one press; see Toggle.
func (s *Store) act(ctx context.Context, client MasterClient, key string, tpl Template) (Outcome, error) {
	live, err := client.LiveZone(ctx, tpl.Master)
	unknown := false
	if err != nil {
		if !masterIsSelf(client, tpl.Master) {
			return OutcomeFailed, fmt.Errorf("%w: %w", errZoneRead, err)
		}
		// THIS speaker leads the template and its own firmware did not say
		// what group it is in (the Bose web server stalls for seconds at a
		// time, 2026-10-27). Refusing the press here made the key dead for as
		// long as the stall lasted. The key's own last press is the best guess
		// of what is live: it formed this template -> dissolve, otherwise ->
		// form. The form keeps its own probe of the main speaker as the gate.
		s.logger.Info("group key: could not read this speaker's own group, going by the key's last press",
			"key", key, "template", tpl.Name, "err", err)
		unknown = true
		live = LiveZone{}
	}
	formedLast := s.formedLast(key, tpl.Name)
	if templateLive(tpl, live) || (formedLast && (unknown || templatePartlyLive(tpl, live))) {
		if err := client.Dissolve(ctx, tpl.Master); err != nil {
			return OutcomeFailed, fmt.Errorf("dissolve: %w", err)
		}
		return OutcomeDissolved, nil
	}

	// Another bound template live on a DIFFERENT main speaker: take it apart
	// first, so the press switches groups instead of stacking two. A different
	// template on the SAME main speaker needs nothing here: the form below
	// replaces that zone's membership.
	outcome := OutcomeFormed
	for _, other := range s.otherBoundTemplates(key, tpl) {
		olive, oerr := client.LiveZone(ctx, other.Master)
		if oerr != nil || !leads(other, olive) {
			continue
		}
		if derr := client.Dissolve(ctx, other.Master); derr != nil {
			s.logger.Warn("group key: could not dissolve the other group before switching, forming anyway",
				"key", key, "other", other.Name, "err", derr)
			continue
		}
		s.logger.Info("group key: other group dissolved before switching", "key", key, "other", other.Name)
		outcome = OutcomeSwitched
	}

	// Ask before forming: the form itself wakes a standby main speaker, so
	// after it the speaker is awake and idle either way, and only the
	// before-picture says whether the press has to start the music.
	idle, ierr := client.Idle(ctx, tpl.Master)
	if ierr != nil {
		// Unknown counts as idle: a press that ends in silence is the
		// complaint this feature exists to avoid, and a resume on a speaker
		// that already plays its own station is a no-op. Logged, because a
		// main speaker that cannot be read here usually cannot be formed
		// either, and the form error alone does not say that came first.
		s.logger.Info("group key: could not read whether the main speaker is playing, assuming it is not",
			"key", key, "template", tpl.Name, "err", ierr)
		idle = true
	}
	deferred, ferr := client.Form(ctx, tpl)
	if ferr != nil {
		return OutcomeFailed, fmt.Errorf("form: %w", ferr)
	}
	if deferred {
		// Stored, not wired. The speakers join on the next play, so a press
		// that ends in silence here is the feature working as built, not a
		// failure, and the log has to say which of the two it was.
		outcome = OutcomeDeferred
	}
	if idle {
		// A permanent template's form is only STORED while the main speaker is
		// idle (the agent defers a permanent group to the next play so
		// creating one never starts music). The resume here IS that play: the
		// main speaker's play-triggered re-form then wires the zone and wakes
		// the stored members.
		if err := client.PlayLast(ctx, tpl.Master); err != nil {
			s.logger.Warn("group key: group formed but the main speaker's last station did not come back",
				"key", key, "template", tpl.Name, "err", err)
		}
	}
	return outcome, nil
}

// otherBoundTemplates lists the templates bound to the OTHER keys whose main
// speaker differs from tpl's.
func (s *Store) otherBoundTemplates(key string, tpl Template) []Template {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Template
	for k, name := range s.doc.Bindings {
		if k == key {
			continue
		}
		o, ok := s.doc.Template(name)
		if !ok || sameSpeaker(o.Master, tpl.Master) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// formedLast reports whether the last successful press of key formed the
// template called name.
func (s *Store) formedLast(key, name string) bool {
	s.togMu.Lock()
	defer s.togMu.Unlock()
	return s.formedBy[key] != "" && strings.EqualFold(s.formedBy[key], name)
}

// finish records the outcome and writes the one log line per press.
func (s *Store) finish(key, name string, outcome Outcome, err error) Outcome {
	a := Action{At: time.Now(), Key: key, Template: name, Outcome: outcome}
	if err != nil {
		a.Error = err.Error()
		a.Kind = errorKind(err)
	}
	s.togMu.Lock()
	s.lastAction = a
	if err == nil {
		switch outcome {
		case OutcomeFormed, OutcomeDeferred, OutcomeSwitched:
			s.formedBy[key] = name
		case OutcomeDissolved:
			delete(s.formedBy, key)
		}
	}
	if err != nil {
		s.lastError = a
	}
	warnNow := false
	if err != nil {
		if last, ok := s.lastWarn[key]; !ok || time.Since(last) >= warnMinGap {
			s.lastWarn[key] = time.Now()
			warnNow = true
		}
	}
	s.togMu.Unlock()
	switch {
	case err == nil:
		s.logger.Info("group key: press handled", "key", key, "template", name, "outcome", string(outcome))
	case warnNow:
		s.logger.Warn("group key: press failed", "key", key, "template", name, "err", err)
	default:
		// INFO, not DEBUG: a diagnostic bundle has to show every press's
		// outcome, and a repeat failure was invisible there (2026-10-27).
		s.logger.Info("group key: press failed again", "key", key, "template", name, "err", err)
	}
	return outcome
}

// leads reports whether tpl's main speaker is leading the live zone it
// reported, by either of the ids it may be known under.
func leads(tpl Template, live LiveZone) bool {
	if live.Master == "" {
		return false
	}
	if strings.EqualFold(live.Master, tpl.Master.DeviceID) {
		return true
	}
	return live.SelfID != "" && strings.EqualFold(live.Master, live.SelfID)
}

// templateLive reports whether the live zone IS the template's group: the
// template's main speaker leads it and every template member is in it. The
// zone may carry more members than the template (a superset still counts as
// this group, so the press dissolves it). Members match by deviceID or by
// address, because a two-chip chassis is enrolled under its firmware id while
// discovery announced its other MAC.
func templateLive(tpl Template, live LiveZone) bool {
	return leads(tpl, live) && containsAll(live.Members, tpl.Members)
}

// templatePartlyLive reports whether the live zone is what is left of the
// template's group with some members missing: the template's main speaker
// leads it, it has at least one follower, and every follower it has is a
// template member. On its own that cannot tell "a member was offline when the
// key formed the group" from "the user grouped two of the speakers by hand",
// so act only counts it as live when this key formed this template last.
func templatePartlyLive(tpl Template, live LiveZone) bool {
	return leads(tpl, live) && len(live.Members) > 0 && containsAll(tpl.Members, live.Members)
}

// containsAll reports whether every speaker of want is in have.
func containsAll(have, want []Member) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if sameSpeaker(w, h) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// sameSpeaker matches two members by deviceID or, failing that, by address.
func sameSpeaker(a, b Member) bool {
	if a.DeviceID != "" && b.DeviceID != "" && strings.EqualFold(a.DeviceID, b.DeviceID) {
		return true
	}
	return a.IP != "" && b.IP != "" && a.IP == b.IP
}

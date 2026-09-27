package main

import (
	"errors"
	"strings"
	"testing"
)

// Which address the QR code for a speaker's phone remote ends up carrying.
//
// The IP is what it used to be, and an IP is the thing that changes: a new DHCP
// lease and the page a phone has on its home screen points at nothing, with
// nothing to tell the person why. A name survives that.
//
// The reason this is a matrix and not a one-liner is that a name which is merely
// PLAUSIBLE is worse than the IP, and the reference network shows why looking at
// a name is not enough to judge it. Its FRITZ!Box serves one name per speaker
// and hands back "B--roPortable.fritz.box" for the Portable: the speaker's
// umlaut, mangled on the way out of the router. It looks broken and it works,
// because the router mangles it the same way in both directions. Tried on the
// five-speaker network on 2026-09-27, all five names verified.
//
// So the decision is made by resolving and reaching, never by reading. The case
// that makes that non-negotiable is a stale lease: it resolves perfectly and
// answers a DIFFERENT speaker, which would print one speaker's QR code on
// another's panel, and only the identity check catches it.
func TestTheQRCodeOnlyCarriesANameThatWasProven(t *testing.T) {
	const here = "192.168.178.21"
	const me = "Wohnzimmer|SoundTouch 30|abc"
	const other = "Kueche|SoundTouch 10|abc"

	probe := func(resolve map[string][]string, identity map[string]string) phoneNameProbe {
		return phoneNameProbe{
			resolve: func(name string) ([]string, error) {
				ips, ok := resolve[name]
				if !ok {
					return nil, errors.New("no such host")
				}
				return ips, nil
			},
			identity: func(hostport string) (string, error) {
				id, ok := identity[hostport]
				if !ok {
					return "", errors.New("no answer")
				}
				return id, nil
			},
		}
	}

	t.Run("the router's name wins when it resolves here and answers", func(t *testing.T) {
		got := pickPhoneAddress(here, 17008, []string{"Wohnzimmer.fritz.box"}, probe(
			map[string][]string{"Wohnzimmer.fritz.box": {here}},
			map[string]string{here + ":17008": me, "Wohnzimmer.fritz.box:17008": me},
		))
		if got.URL != "http://Wohnzimmer.fritz.box:17008/" {
			t.Fatalf("URL = %q, want the router name", got.URL)
		}
		if got.Source != "router" {
			t.Errorf("Source = %q, want router", got.Source)
		}
		if got.AddressURL != "http://192.168.178.21:17008/" {
			t.Errorf("the address fallback is missing: %q", got.AddressURL)
		}
	})

	t.Run("a name the router serves but nothing resolves is refused", func(t *testing.T) {
		// Shaped like the Portable's mangled name, with the resolver saying no.
		// On the reference network that same name does resolve and is used; what
		// decides is the answer, not the spelling.
		got := pickPhoneAddress(here, 17008, []string{"B--roPortable.fritz.box"}, probe(
			map[string][]string{},
			map[string]string{here + ":17008": me},
		))
		if got.Source != "address" {
			t.Fatalf("Source = %q, want address (the name does not resolve)", got.Source)
		}
		if !strings.Contains(got.Note, "does not resolve") {
			t.Errorf("the note does not say why: %q", got.Note)
		}
	})

	t.Run("a name pointing at a different address is refused", func(t *testing.T) {
		got := pickPhoneAddress(here, 17008, []string{"Wohnzimmer.fritz.box"}, probe(
			map[string][]string{"Wohnzimmer.fritz.box": {"192.168.178.99"}},
			map[string]string{here + ":17008": me},
		))
		if got.Source != "address" {
			t.Fatalf("Source = %q, want address", got.Source)
		}
		if !strings.Contains(got.Note, "resolves elsewhere") {
			t.Errorf("the note does not say why: %q", got.Note)
		}
	})

	t.Run("a stale lease answering another speaker is refused", func(t *testing.T) {
		// The dangerous one: it resolves to this address and something answers,
		// but it is not this speaker. Only the identity check catches it.
		got := pickPhoneAddress(here, 17008, []string{"Kueche.fritz.box"}, probe(
			map[string][]string{"Kueche.fritz.box": {here}},
			map[string]string{here + ":17008": me, "Kueche.fritz.box:17008": other},
		))
		if got.Source != "address" {
			t.Fatalf("Source = %q, want address (that name is another speaker)", got.Source)
		}
		if !strings.Contains(got.Note, "answers a different speaker") {
			t.Errorf("the note does not say why: %q", got.Note)
		}
	})

	t.Run("a name that resolves but does not answer is refused", func(t *testing.T) {
		got := pickPhoneAddress(here, 17008, []string{"Wohnzimmer.fritz.box"}, probe(
			map[string][]string{"Wohnzimmer.fritz.box": {here}},
			map[string]string{here + ":17008": me},
		))
		if got.Source != "address" {
			t.Fatalf("Source = %q, want address", got.Source)
		}
		if !strings.Contains(got.Note, "does not answer through it") {
			t.Errorf("the note does not say why: %q", got.Note)
		}
	})

	t.Run("the speaker's own .local name is the fallback, not the first choice", func(t *testing.T) {
		// Order matters: the router name is ordinary DNS and every phone
		// resolves it, while .local is reliable on iOS and patchy on Android.
		// Both are valid here, and the router name must win.
		got := pickPhoneAddress(here, 17008, []string{"Wohnzimmer.fritz.box", "str-96488d.local"}, probe(
			map[string][]string{"Wohnzimmer.fritz.box": {here}, "str-96488d.local": {here}},
			map[string]string{here + ":17008": me, "Wohnzimmer.fritz.box:17008": me, "str-96488d.local:17008": me},
		))
		if got.Name != "Wohnzimmer.fritz.box" {
			t.Fatalf("Name = %q, want the router name to outrank .local", got.Name)
		}
	})

	t.Run("the .local name is used when the router serves nothing", func(t *testing.T) {
		got := pickPhoneAddress(here, 17008, []string{"str-96488d.local"}, probe(
			map[string][]string{"str-96488d.local": {here}},
			map[string]string{here + ":17008": me, "str-96488d.local:17008": me},
		))
		if got.Source != "speaker" {
			t.Fatalf("Source = %q, want speaker", got.Source)
		}
	})

	t.Run("a speaker that does not answer at all keeps its address", func(t *testing.T) {
		// Nothing can be checked against a speaker that is asleep, and guessing
		// would put an unverified name on the code.
		got := pickPhoneAddress(here, 17008, []string{"Wohnzimmer.fritz.box"}, probe(
			map[string][]string{"Wohnzimmer.fritz.box": {here}},
			map[string]string{},
		))
		if got.URL != "http://192.168.178.21:17008/" {
			t.Fatalf("URL = %q, want the plain address", got.URL)
		}
	})

	t.Run("no candidates at all is not an error", func(t *testing.T) {
		got := pickPhoneAddress(here, 8888, nil, probe(nil, map[string]string{here + ":8888": me}))
		if got.URL != "http://192.168.178.21:8888/" || got.Source != "address" {
			t.Fatalf("got %+v, want the plain address", got)
		}
		if got.Note == "" {
			t.Error("no note, so the log cannot say why the address was used")
		}
	})
}

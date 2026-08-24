package webui

import (
	"fmt"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// The two /addGroup error helpers used to substring-match the WHOLE error
// string, and that string embeds the reply body, whose envelope carries the
// box's raw 12-hex MAC as deviceID. A MAC containing the digit run 5510 or
// 5580 therefore satisfied the check on EVERY failed /addGroup of that box.
// The consequences are not symmetric noise: a false 5510 runs
// healStaleStereoGroups, which dissolves pairs on BOTH speakers, and a false
// 5580 arms the ForcePair retry path.

// envErr builds the typed error the boxapi POST helpers now return, with the
// poisoned MAC in the raw body exactly where the firmware puts it.
func envErr(value, name, mac string) error {
	body := fmt.Sprintf(`<errors deviceID="%s"><error value="%s" name="%s" severity="Unknown">x</error></errors>`, mac, value, name)
	return &boxapi.BoxError{Path: "/addGroup", Status: 500, Value: value, Name: name, Body: body}
}

func TestGroupErrHelpersUseTheTypedCodeNotTheMAC(t *testing.T) {
	// A box whose MAC contains 5580, failing with a code that is NOT 5580:
	// the old substring match fired here on every failure, forever.
	otherErr := envErr("5005", "SOME_OTHER_ERROR", "AA5580BB0C11")
	if isMargeGroupErr(otherErr) {
		t.Error("a MAC containing 5580 must not read as the marge-group rejection")
	}
	if isGroupExistsErr(envErr("5005", "SOME_OTHER_ERROR", "AB5510CD0E22")) {
		t.Error("a MAC containing 5510 must not read as GROUP_ALREADY_EXISTS")
	}

	// The genuine rejections still hit, by value and by name.
	if !isGroupExistsErr(envErr("5510", "GROUP_ALREADY_EXISTS", "AABBCCDDEEFF")) {
		t.Error("a real 5510 must still be recognised")
	}
	if !isMargeGroupErr(envErr("5580", "GROUP_CREATE_GROUP_ON_MARGE_ERROR", "AABBCCDDEEFF")) {
		t.Error("a real 5580 must still be recognised")
	}
	// Name-only match on a typed envelope (a firmware that renames a code but
	// keeps the constant) still counts.
	if !isGroupExistsErr(envErr("9999", "GROUP_ALREADY_EXISTS", "AABBCCDDEEFF")) {
		t.Error("the constant name must be enough on a typed envelope")
	}
}

func TestGroupErrHelpersKeepTheUntypedFallback(t *testing.T) {
	// Transport failures and bare bodies produce untyped errors; the old
	// behaviour is the only signal there and must survive.
	if !isGroupExistsErr(fmt.Errorf("box /addGroup: 500: GROUP_ALREADY_EXISTS")) {
		t.Error("an untyped error naming the constant must still be recognised")
	}
	// A typed envelope with an empty Value means the body was NOT an envelope;
	// the fallback may then match the text, which carries no envelope MAC.
	bare := &boxapi.BoxError{Path: "/addGroup", Status: 500, Body: "GROUP_CREATE_GROUP_ON_MARGE_ERROR"}
	if !isMargeGroupErr(fmt.Errorf("wrapped: %w", bare)) {
		t.Error("an untyped bare-body error must fall back to the text match")
	}
	if isGroupExistsErr(nil) {
		t.Error("nil is not an error")
	}
}

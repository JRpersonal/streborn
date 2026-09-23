package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/dlna"
)

// The caller's deadline has to be the thing that decides how long a library
// browse may take, because only the caller knows whether this is one user tap
// or one share of a fan-out. A client-side ceiling inside dlna that sits BELOW
// the caller's budget silently overrides it, and it does so invisibly: the
// failure arrives as Go's own client-timeout text, which reads like the media
// server giving up rather than STR giving up on it.
//
// That is exactly what happened on #960. libraryBrowseTimeout allowed 70s for a
// slow NAS, dlna.Browse capped its http.Client at 10s, and the reporter's browse
// died at 10.1s with a message that pointed at his server.
//
// This test is cheap and it is the only thing that would have caught it: both
// numbers are correct on their own, and only their relationship is wrong.
func TestTheBrowseCeilingNeverCutsTheBudgetShort(t *testing.T) {
	if dlna.SOAPClientCeiling < libraryBrowseTimeout {
		t.Fatalf("dlna.SOAPClientCeiling = %v is below libraryBrowseTimeout = %v: "+
			"the ceiling would end the browse first and the user would be told his media server timed out",
			dlna.SOAPClientCeiling, libraryBrowseTimeout)
	}
	if dlna.SOAPClientCeiling < librarySearchBudget {
		t.Fatalf("dlna.SOAPClientCeiling = %v is below librarySearchBudget = %v",
			dlna.SOAPClientCeiling, librarySearchBudget)
	}
}

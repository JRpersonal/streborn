package webui

// Hosts for tests that need every firmware call to FAIL, and to fail at once.
//
// Many tests in this package point the box host at an address nothing answers
// on, so the handler under test takes its "speaker unreadable" branch and the
// assertions are about what it decides on its own. A TEST-NET address
// (192.0.2.x) does that, but slowly: nothing refuses the connection, so every
// call waits out the whole budget its caller set, 8 s for a zone dissolve and 3 s
// for a balance read, on Linux and Windows alike. A loopback port is no better
// on a Windows developer machine, where the firewall's stealth mode swallows the
// refusal and the dial retries for about 2 s.
//
// A host carrying a port is not a bare host, so boxapi refuses to build a URL
// for it and every call fails immediately, on every OS, with no dial at all.
// The handler sees an error either way; the kind of error is not what any of
// these tests is about.
//
// Three distinct strings, so a test can still tell the box from its members and
// a self-check such as `ip == s.boxHost` keeps the meaning it has in the field.
const (
	deadBoxHost = "127.0.0.1:1"
	deadMemberA = "127.0.0.1:2"
	deadMemberB = "127.0.0.1:3"
)

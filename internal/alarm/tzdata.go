package alarm

// The timezone database is embedded in the agent binary rather than read from
// the speaker, because the speaker does not have one to read: the stock
// firmware ships no /usr/share/zoneinfo the agent can rely on, and STR has
// never set TZ or /etc/localtime. Without this import time.LoadLocation fails
// on the box and every alarm falls back to UTC, which is the one failure that
// looks like the feature working until an hour after it should have gone off.
//
// It costs 384 KiB on the ARMv7 agent binary (measured 2026-09-08: 19,659,182
// bytes without, 20,053,358 with, so 2.0%), which the desktop app also embeds.
// That is real on a 27-33 MB NAND where install.sh already drops the 16 MB
// Spotify engine when space is tight. It buys DST that is correct forever with
// no user chore, which a stored UTC offset cannot do. If the size ever has to
// come back, the agent is currently built without -w and dropping DWARF
// reclaims considerably more than this import costs; that is a separate change
// and does not belong to the alarm feature.
//
// The import lives in this package, not in cmd/agent, so the tests here are
// hermetic (they do not depend on the CI runner having a system zoneinfo) and
// nothing else in the repo has to know about it.
import _ "time/tzdata"

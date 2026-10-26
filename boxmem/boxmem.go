// Package boxmem holds the speaker memory limits that both the on-box agent
// and the desktop app have to agree on. It lives at the top level, like
// versioncmp, because the desktop app is its own Go module and cannot import
// the agent's internal packages.
package boxmem

// RAMStageFloor is the MemAvailable that must remain on the speaker AFTER a
// whole upload sits in a RAM (tmpfs) stage, or the RAM route is not taken.
//
// tmpfs pages cannot be reclaimed and the speakers have no swap, so every
// staged byte comes straight out of MemAvailable until the stage is removed.
// What is left has to carry the rest of the install: the dirty page cache of
// the RAM->NAND copy, the 512 KB flash-verify window, Go heap growth (GOGC lets
// the heap double before a collection) and whatever BoseApp does meanwhile.
// The field marks where that runs out: #1083 sat at 7.5 MB MemAvailable on the
// cliff, and the 2026-08-22 ST20 that was OOM-killed during the flash verify
// had about 19 MB left and then asked for another 14.6 MB, i.e. under 5 MB of
// real room. 16 MB is twice the lowest figure a speaker came back from, and
// still lets the measured speakers in (45-51 MB MemAvailable, a 17.5 MB agent
// leaves 27-33 MB after the stage).
//
// Two paths use it: the agent's RAM-first OTA (internal/webui) and the desktop
// app's SSH repair install, which stages its install files in /dev/shm.
const RAMStageFloor = 16 << 20

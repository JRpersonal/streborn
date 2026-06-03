# Firmware notes: living with the stock SoundTouch firmware

STR runs on top of the unmodified Bose SoundTouch firmware. Reviving
the speakers after the cloud shutdown meant learning a lot about how
that firmware behaves once its cloud is gone. This page collects the
hard-won, reproducible findings so other people working on these
speakers do not have to rediscover them.

Everything here is observed runtime behaviour (service names, local
port numbers, process states). It contains **no** Bose code, no
firmware binaries, and no decompilation. All device identifiers, IPs,
and MACs are placeholders.

## The Portable ~27-minute reboot loop (and how STR fixes it)

**Symptom.** On the SoundTouch Portable, internet radio would stop and
the speaker would reboot itself roughly every 27 minutes. Other models
(ST10/20/30) were not affected.

**Root cause, pinned live with `strace` + `/proc`:**

1. The Portable's battery service, `BatteryMonitor` (the local Bose
   service registered at `127.0.0.1:17002` in
   `/opt/Bose/etc/services.json`), reads the battery's identity chip
   over I2C and looks up a matching "battery personality" module. The
   battery on the test unit reports type `BOSE_A`, which this firmware
   build has no personality for (it knows `BOSE_ICC`, `BOSE_SANYO`,
   `BOSE_SERVICES`). It logs `CRITICAL: No battery personality module
   for BOSE_A` and its main thread then parks on a futex forever. The
   `:17002` listener is never opened. This is deterministic: killing the
   process makes the supervisor respawn it, and it re-deadlocks at once.
2. `BoseApp` (the main firmware app) runs a battery UI client that wants
   `:17002`. With nothing listening it retries `connect()` in a tight
   loop (~137 failed attempts/second), and each failed attempt leaves a
   new client thread pair blocked in `poll`, each holding one eventfd +
   one timerfd that is never reaped.
3. That leaks ~30 file descriptors/minute. When `BoseApp`'s open-fd
   count reaches its internal ~1024 `select()`/`FD_SETSIZE` ceiling, its
   `:8090` HTTP API deadlocks and the Bose watchdog reboots the box.
   ~27 minutes per cycle.

**The fix (STR v0.6.18).** The retry storm is driven purely by
`connect()` *failing*. The instant anything accepts on `:17002`,
`BoseApp`'s client connects and blocks reading instead of spawning a
new leaking thread, the fd/thread count plateaus, and the box stays up.
So the STR agent itself listens on `127.0.0.1:17002` as a fallback when
the port is unserved, accepts the battery client, and drains the
connection. It waits a short grace period and only binds when the port
is free, so on a box whose `BatteryMonitor` is healthy the real service
keeps the port and the agent stays out of the way. On models with no
battery, nothing connects and the listener sits idle. See
`cmd/agent/boseapp_recovery.go`.

**Ruled out along the way** (so nobody re-derives them): it is **not**
STR's `/etc/hosts` cloud redirect (with the redirect off the leak rate
and reboot interval were identical), **not** the STR agent / gabbo
connection (killing the agent did not change the leak), and **not**
diagnostic probing. It is the stock firmware reacting to an
unrecognised battery, which STR papers over.

This also explains the "battery always shows 50%" cosmetic issue on the
same unit: with `BatteryMonitor` dead, `BoseApp` never receives real
battery data. Restoring the real percentage would require replaying the
proprietary `:17002` push protocol; the reboot fix does not attempt it.

## Hardware preset buttons: the gabbo bus

The speaker exposes an internal WebSocket IPC bus on
`ws://127.0.0.1:8080/` with subprotocol `gabbo`. Physical preset-button
presses and connection-state changes are published there. STR subscribes
(read-only) and, on a `nowSelectionUpdated` / preset event, drives
playback over UPnP. This is how hardware buttons 1 to 6 come back to life
without any cloud. See `internal/boxws/boxws.go`.

## Reaching the agent on BCO speakers (chipset whitelist)

On the newer "BCO" chassis (Portable, and the scm/spotty ST20 revision)
the network chipset only routes inbound external TCP to listeners owned
by a Bose binary. A normal listener like the STR agent on `:8888` is not
reachable from the LAN as-is. STR works around this two ways:

- An `iptables` PREROUTING `REDIRECT` maps an externally reachable,
  Bose-owned port to the agent (the path STR uses on BCO today).
- An `LD_PRELOAD` shim (`usb-stick/shim/shim.c`, built from source on
  every release) can hook `accept()` inside a Bose process to forward
  connections. The shim is deliberately **skipped** on the Portable
  chassis, where it raced the firmware's service-init and wedged it; the
  iptables path is used instead.

Older "Series-I" ST10 (rhino) does not need this; its agent is reachable
directly.

## Bose internal HTTP buffer cap

Bose's internal HTTP library (used by `BoseApp` on `:8090` and the
SoftwareUpdate service on `:17008`) caps a POST at ~1536 bytes including
the request line and headers. Any STR call routed through `:17008`
without an active shim must stay under that, which is why the agent OTA
has an SSH fallback for the binary upload.

## NAND override beats the SD card

The SD card the firmware boots from is unreliable for writes. STR
installs `/mnt/nv/streborn/run-override.sh` on the speaker's NAND, which
the boot path runs **in place of** the SD-based entry point. Treat the
SD card as read-only. Do not re-exec `run-override.sh` while it is
already running: it collides with the Bose service manager and leaves the
speaker in a bad state.

## A Bose "factory reset" does not remove STR

The on-device factory reset (and the Bose app's reset) clears only what
Bose tracks: pairing, account, friendly name, Wi-Fi profile. It does
**not** touch `/mnt/nv/streborn/`. After a reset, STR is still installed
and boots automatically once the brief setup-AP window times out.
Removing STR is therefore a separate, explicit "Uninstall STR" step.

## See also

- [`ARCHITECTURE.md`](./ARCHITECTURE.md) for the component map, ports,
  and data flows.
- [`THREAT-MODEL.md`](./THREAT-MODEL.md) for the security caveats of the
  firmware STR runs on top of.
- [`MODEL-VARIANTS.md`](./MODEL-VARIANTS.md) for the per-variant
  fingerprint table.

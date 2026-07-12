# How Wi-Fi presents internally per SoundTouch model

Live-measured on real hardware (2026-07-12) plus field diagnostics from
issues #270/#119. This is the reference for why Wi-Fi code MUST branch per
chassis class, and for the v0.9.7 hands-off-boot rule.

## The three classes

| | wpa class (sm2) | SMSC-bridge class (scm/BCO) | taigan-bco (Portable) |
|---|---|---|---|
| Example boxes | ST10 (rhino), some ST20/ST30 | some ST20 (spotty), some ST30 (mojo) | SoundTouch Portable |
| Wi-Fi driven by | firmware `wpa_supplicant` on `wlan0` | SMSC-2014 coprocessor, bridged to the SoC | BCO coprocessor, bridged |
| `/networkInfo` shows | `WIFI_INTERFACE` + live `signal=` class | `ETHERNET_INTERFACE eth0` (the bridge!), no signal, no SSID | `ETHERNET_INTERFACE eth0`, no signal, no SSID |
| wpa_cli / wpa_supplicant present | yes | no | no |
| External agent access | `:8888` direct | `:17008` REDIRECT only | `:17008` REDIRECT only |
| Profile store | firmware `NetworkProfiles.xml` + wpa | coprocessor NVRAM (+ firmware store) | coprocessor NVRAM; `AirplayConfiguration.xml` seeds it |
| Programmable via | (runtime API / wpa) | GoAhead `:80` `/goform` (the box's own web interface) | GoAhead `:80` `/goform` |
| NAND size seen | ~33 MB | ~27 MB (smaller!) | ~33 MB |

Key traps:

- **An `eth0` lease on a bridge-class box does NOT mean a LAN cable.** The
  coprocessor presents the Wi-Fi link as USB-CDC ethernet. Never use "eth0
  connected" to conclude "on cable", and never count an eth0 lease as proof
  that a Wi-Fi provisioning step worked (the pre-v0.9.7 pipeline did, and
  logged false "winner" lines).
- **The display's Wi-Fi icon is driven by the firmware NetManager's own
  state machine**, not by actual connectivity. A bridge-class box that is
  perfectly online shows an ORANGE icon when NetManager was pushed into a
  Wi-Fi-pending state - which is exactly what the boot-time
  `/addWirelessProfile` POST (added after v0.8.48) did on every start.
  Live-proven on an scm ST30: clean cold boot with the old agent = orange;
  first hands-off boot (v0.9.7, no provisioning) = white.
- **Signal classes:** only the wpa class reports a live `signal=` in
  `/networkInfo`. On bridge boxes the only source is a gabbo
  `connectionState` frame at connection transitions; treat it as a
  snapshot, never as a current measurement (the agent expires it after
  15 min since v0.9.7).
- **`wlan-mode` on NAND** records the boot classification: `wlan0/wlan1`
  (wpa), `bco` (bridge classes; note a mojo ST30 classifies as `bco` when
  its firmware ships no wpa tools), `taigan-bco`, `ethernet-only`.

## The v0.9.7 hands-off rule

On a NORMAL boot (NAND credential replay) STR does not touch Wi-Fi at all:
the stock firmware re-associates from its own stores, which all three
classes do reliably when healthy. STR provisions Wi-Fi only on explicit
user action (stick `wlan.conf` install/setup, or the app's Wi-Fi settings),
preferring the boxes' own web interfaces (`/goform`). The single exception
is the offline rescue: a box with no lease at all after 90 s gets the
non-destructive goform re-push watchdog until a lease appears (bounded,
never touches an online box) - that covers the scm cold-boot association
failures without re-provisioning healthy speakers.

Open questions (tracked for the web-interface catalog): full inventory of
each class's `:80` web UI in setup/normal mode, and whether NetManager's
Wi-Fi-pending state can be cleared through a documented endpoint instead
of only by leaving it alone.

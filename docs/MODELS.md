# Supported Bose SoundTouch models

Which release asset goes with which speaker, and how far each model has
been validated.

Since the 2026-07-08 install rework the app's **network install** (via the
speaker's `:17000` setup port, no USB stick) is the primary first-install
channel whenever the speaker is reachable on the LAN; the USB stick remains
the fallback and recovery path. This matters for the status table: models
that never read a stick at boot (ST300, SA-4/SA-5, Wave, CineMate) now have
a realistic install path for the first time.

> Per-variant hardware fingerprints (moduleType, components, firmware
> build stamps, kernel, RAM, WLAN-interface presence) live in
> [`MODEL-VARIANTS.md`](MODEL-VARIANTS.md). Update that file when a new
> diagnostic bundle reveals a previously unseen combination.

## Status at a glance

| Model | Platform / variant | STR status |
| --- | --- | --- |
| **SoundTouch 10** | TI AM335x ARMv7l, module SM2, variant `rhino` (Series-II) | **Verified** |
| **SoundTouch Portable** | TI AM335x ARMv7l, BCO coprocessor, variant `taigan` | **Verified** |
| **SoundTouch 20** | TI AM335x ARMv7l, module `scm` + SMSC, variant `spotty` (BCO) | **Working (contributor-confirmed; final stability confirmation in progress)** |
| **SoundTouch 20** | TI AM335x ARMv7l, module SM2 (codename still `spotty`) | **Working (user-confirmed)**: two live SM2-ST20 fingerprints have arrived from the field. It provisions Wi-Fi the Series-II way (real `wlan0`), while `run.sh` still applies the whitelisted-chassis reachability path (REDIRECT `:17008`->`:8888`) because the codename is `spotty` - harmless where the chipset does not need it. Caveat: one unit on the older firmware 27.0.3 was unstable; 27.0.6 is the target for this chassis as for every other. |
| **SoundTouch 30** | TI AM335x ARMv7l, module SM2 **or** `scm` (both observed), variant `mojo` | **Working** (sm2: live-confirmed via the #123 diagnostic, 2026-06-10; scm: maintainer network-installed one end to end 2026-07-09 in ~3.5 min - the first observed scm-module ST30) |
| **SoundTouch 300** | AM335x ARMv7l, module `sm2`, variant `ginger` | **Working (maintainer + user-confirmed)**: the stick-free network install brings the agent up and serving on a factory-reset unit (2026-07-08); a user then ran it end to end (PC and phone control, presets 1-6) 2026-07-11. The stick was never an option on this model (it does not read USB at boot). The one-time reboot-cascade caveat (alternating amber LED, needing a manual power-cycle after an install or update while `shepherdd` sat in `--recovery`, #372) no longer applies as of v0.9.7: `ginger` now also waits for the Bose stack before the fragile reboot, so no power-cycle is needed (see the Wave row). |
| **Wave SoundTouch** | AM335x ARMv7l, module `scm` **or** `sm2` (both observed live), variant `lisa` + SMSC | **Working (maintainer + contributor-confirmed)**: maintainer network-installed end to end 2026-07-09; two independent #182 users then confirmed it on their own Wave IV units (2026-07-09 and 2026-07-11) - presets, NAS/DLNA playback, grouping with an ST20, and the IR remote's preset keys 1-6 recall STR presets once the source is cycled to SoundTouch (same gabbo path as the front-panel buttons). The network install (v0.9.0+) is the only first-install path: the Wave never reads a USB stick at boot. The old one-time caveat (the first-install reboot cascade tripped Bose's `shepherdd` into `--recovery`, needing a single power-cycle) is fixed (#372): the agent waits for the Bose stack (:8090) to come up before its fragile post-install or post-update reboot, so `shepherdd` marks the boot healthy and does not fall into `--recovery`. First shipped for `lisa` in v0.9.2 and generalized in v0.9.7 (`a56b0ae`) to every chassis except the hardware-verified fast ones (`rhino`, `mojo`, `taigan`), which covers `ginger`/ST300 as well. The IR remote's AM/FM/DIGITAL RADIO sources are the Wave head unit's own tuner and are invisible to STR; STR cannot bind streaming presets to them (the gabbo hook is read-only). |
| **Bose SA-4 amplifier** | AM335x ARMv7l, module `scm`, variant `lisa` + SMSC/Lightswitch | **Working (user-confirmed)**: a user network-installed STR end to end 2026-07-09. The stick was never an option (it does not read USB at boot); the stick-free network install is the path. |
| **Bose SA-5 amplifier** | AM335x ARMv7l, module `sm2`, variant `burns` + SMSC | **Working (user-confirmed)**: an owner runs STR v0.9.25 on the SA-5 (#274, 2026-08-01): network install, app + phone-remote control and Now Playing (including inputs renamed back in the Bose-app days) all work. Known gap: the SA-5 reports three AUX inputs (source `AUX`, `sourceAccount` AUX1-AUX3) where STR models a single AUX, so AUX switching from STR does not work yet (tracked on #390). Like the SA-4 it has no buttons or remote and never reads a stick; the network install is the path. |
| **CineMate 520** | module `sm2`, variant `lisa` | **Working (user-confirmed)**: a user network-installed STR end to end 2026-07-09. |
| **CineMate 130** | AM335x ARMv7l (full fingerprint pending from the #491 diagnostics) | **Working (user-confirmed)**: an owner migrated from another mod via the network install and runs radio, presets and the app in a mixed ST10+ST20+CineMate fleet; re-confirmed on v0.9.27 (#491, 2026-08-01). Quirk: this soundbar reports its TV/AUX input as source `LOCAL` instead of `AUX`; STR understands that since v0.9.26/v0.9.27 (AUX badge + status shown). Other CineMate models remain untested. |
| **Bose Lifestyle console** (incl. Lifestyle 535 with a SoundTouch adapter) | AM335x ARMv7l, module `sm2` variant `bardeen` **or** `scm` variant `lisa` + SMSC | **Working (user-confirmed)**: two owners run STR v0.9.28 on Lifestyle consoles, one on each variant, with the agent up, presets registered and multiroom in use (2026-08-03 diagnostics); a third owner reports a Lifestyle 535 driven through a SoundTouch 20 Series II adapter working. Note: an earlier answer from this project said Lifestyle consoles and adapters were not supported and that only standalone SoundTouch speakers worked. That was wrong. The adapter is the SoundTouch module, so from STR's point of view it is an ordinary SoundTouch speaker that happens to feed a Lifestyle system. Like the SA amplifiers it has no USB boot path, so the network install is the way in. |
| **SoundTouch Wireless Link Adapter** | AM335x ARMv7l, module `sm2`, variant `binky` + SMSC | **Working (user-confirmed)**: an owner runs STR v0.9.36 on two of them (2026-08-08 diagnostic). Both report `boxHealth: ok` with the Spotify engine present, and both joined a native multiroom group as followers under a SoundTouch Portable master, verified member by member. The adapter has no speaker of its own, so it is the SoundTouch module feeding whatever it is wired into; from STR it behaves as an ordinary speaker. Like the Lifestyle consoles and the SA amplifiers it has no USB boot path, so the network install is the way in. |
| other (Soundbar, ...) | unknown | **Unknown** |

All ARMv7l models run the same agent binary (`streborn-armv7l`); the
per-model release aliases are byte-identical copies for convenience.

### Status definitions

- **Verified** , exercised live on real hardware by the maintainer: clean
  bootstrap, the speaker provisions onto Wi-Fi, the agent serves
  WebUI/Marge/BMX without crashing, radio + presets work, and it survives
  a reboot/standby cycle.
- **Working (...)** , runs end to end on real hardware; the parenthesis
  names who established that and therefore how strong the evidence is:
  - *maintainer* , the maintainer installed and exercised this chassis
    himself (strongest evidence short of Verified, which additionally
    requires the full reboot/standby/outage cycle).
  - *contributor-confirmed* , a trusted contributor ran it end to end and
    reported back in detail.
  - *user-confirmed* , an owner reported a working install with enough
    detail (or a diagnostic bundle) to trust the report.
  - *confirmation in progress* , plays on real hardware, but the final
    end-to-end stability pass on the current release is still pending.
- **Expected** , same hardware platform as a verified model, no live
  proof yet.
- **Unknown** , different or unconfirmed hardware; no guarantee.

## Two hardware families (this is the important part)

SoundTouch speakers on AM335x split into two families that STR has to
provision and reach completely differently. Both run the same agent
binary; the difference is in how Wi-Fi and external reachability work.

### Series-II (classic): module SM2 with a real `wlan0`

Chassis in this family: `rhino` (ST10), `mojo` (sm2 ST30), `spotty` (sm2
ST20), `ginger` (ST300), `burns` (SA-5), and the `sm2` variants of the
Wave and CineMate (`lisa`).

- Real `wlan0` interface; STR provisions Wi-Fi the documented way
  (`/addWirelessProfile` over the box's HTTP API, or `wpa_supplicant`).
- The STR agent's port `:8888` is reachable directly from the LAN
  (once `run.sh` punches the `INPUT ACCEPT` rule past the Bose firewall).
- This is the original, simplest path. ST10 is the reference target.
- Caveat for the SM2 ST20: its codename is `spotty`, and `run.sh` keys
  the reachability treatment off the codename, so an SM2 ST20 still gets
  the `:17008` REDIRECT (a harmless no-op if its chipset does not need
  it). Wi-Fi provisioning still follows the `wlan0` path.

### BCO / whitelisted chassis: Wi-Fi through a coprocessor

Chassis in this family: `taigan` (Portable), `scm`+`spotty` (ST20), and
the `scm` variants of the ST30, the Wave (`lisa`) and the SA-4.

These speakers drive Wi-Fi through a separate coprocessor exposed as
`eth0` (USB-CDC-Ethernet), and the chipset firewalls inbound TCP: the
agent's `:8888` is dropped from the LAN even though it is listening.
STR handles this with three mechanisms, all live-verified in mid-2026:

1. **Provisioning via `AirplayConfiguration.xml` (M_air).** The
   documented `/addWirelessProfile` returns HTTP 500 on these boxes
   (the Marge/cloud handshake is dead post-shutdown). Instead STR writes
   an accountless `PersistentWifiProfile` directly into
   `/mnt/nv/BoseApp-Persistence/<N>/AirplayConfiguration.xml`; BoseApp's
   network controller reads it at boot and joins Wi-Fi. STR skips this
   rewrite+reboot once the box is already provisioned for the same
   credentials, so a stick left inserted does not force a slow reboot.
2. **External reachability via an iptables REDIRECT.** A whitelisted
   Bose port (`:17008`) is REDIRECTed to the agent's `:8888`, so LAN
   clients reach STR through `:17008`. The desktop app probes both ports
   and uses whichever actually answers (a verified-reachable port always
   wins over the mDNS-announced `:8888`).
3. **The on-screen "white bar" on boot is the box, not a hang.** BoseApp
   on these models takes ~130 s to come up; with a stick inserted there
   is also a one-time provisioning reboot. The speaker does finish
   booting; pull the stick after the initial install for a fast boot.

The desktop app also keeps a recently-seen speaker in its list across a
missed discovery cycle, so a BCO box does not flicker in and out.

## Display language

The box display/voice language is the Bose `sysLanguage` integer (full
enum 0..25 resolved; English = 3, German = 2, Danish = 1, ...). STR no
longer hard-codes one language: the setup wizard picks it from the
chosen country, refined by the app's UI language, and the box-language
picker lists all 24 defined languages by their native name (0 is the
factory sentinel and 14 is undefined, so neither is offered). Speakers with no
matching language fall back to English (for a Ukrainian UI the box
display falls back to Russian, which is more readable than English,
while the app UI itself never offers Russian).

## How to check your model over SSH

```sh
uname -m              # CPU, e.g. armv7l
cat /proc/variant     # Bose variant: rhino (ST10), mojo (ST30), taigan (Portable), spotty (ST20)
hostname              # Bose hostname, often equals the variant
# Series-II vs BCO: a real wlan0 means Series-II; Wi-Fi via eth0 + a
# <componentCategory>SCM</componentCategory> in /info means BCO.
```

If `uname -m` is `armv7l`, the `streborn-armv7l` binary is the right one
regardless of family; the family only changes how STR provisions and
reaches the box, which run.sh detects automatically.

## Adding another model

1. Run the platform analysis (see `RUNBOOK-analyse.md`).
2. Check CPU + module type. If not ARMv7l, a new cross-compile target in
   the Makefile and CI is needed.
3. Same platform: add a row here plus an asset alias in
   `.github/workflows/build.yml`.
4. Live-test, then record the result in the status table above.

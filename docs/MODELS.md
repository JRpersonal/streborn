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
| **SoundTouch 20** | TI AM335x ARMv7l, module SM2 (codename still `spotty`) | **Expected**: provisions Wi-Fi the Series-II way (real `wlan0`), but `run.sh` still applies the whitelisted-chassis reachability path (REDIRECT `:17008`->`:8888`) because the codename is `spotty`. Awaiting a live SM2-ST20 report. |
| **SoundTouch 30** | TI AM335x ARMv7l, module SM2 **or** `scm` (both observed), variant `mojo` | **Working** (sm2: live-confirmed via the #123 diagnostic, 2026-06-10; scm: maintainer network-installed one end to end 2026-07-09 in ~3.5 min - the first observed scm-module ST30) |
| **SoundTouch 300** | AM335x ARMv7l, module `sm2`, variant `ginger` | **Working (maintainer-confirmed)**: the stick-free network install brings the agent up and serving on a factory-reset unit (2026-07-08); full end-to-end playback pass in progress. The stick was never an option on this model (it does not read USB at boot). |
| **Wave SoundTouch** | AM335x ARMv7l, module `scm` **or** `sm2` (both observed live), variant `lisa` + SMSC | **Working (maintainer-confirmed)**: network-installed end to end 2026-07-09 - STR agent + Bose stack coexist and internet radio plays (UPnP, `PLAY_STATE`). One-time caveat: the first-install reboot cascade (bundle + bootstrap + Wi-Fi switch) trips Bose's `shepherdd` into `--recovery` mode, so the Bose services do not start until a single power-cycle; after that both stacks come up cleanly. Reducing that cascade so no manual power-cycle is needed is tracked in #372. |
| **Bose SA-4 amplifier** | AM335x ARMv7l, module `scm`, variant `lisa` + SMSC/Lightswitch | **Working (user-confirmed)**: a user network-installed STR end to end 2026-07-09. The stick was never an option (it does not read USB at boot); the stick-free network install is the path. |
| **Bose SA-5 amplifier** | AM335x ARMv7l, module `sm2`, variant `burns` + SMSC | **Unknown** (#274: same story as the SA-4; fw 27.0.6 confirmed via diagnostics; the network install is the candidate path) |
| **CineMate 520** | module `sm2`, variant `lisa` | **Working (user-confirmed)**: a user network-installed STR end to end 2026-07-09. Other CineMate models remain untested. |
| other (Soundbar, ...) | unknown | **Unknown** |

All ARMv7l models run the same agent binary (`streborn-armv7l`); the
per-model release aliases are byte-identical copies for convenience.

### Status definitions

- **Verified** , exercised live on real hardware: clean bootstrap, the
  speaker provisions onto Wi-Fi, the agent serves WebUI/Marge/BMX without
  crashing, radio + presets work, and it survives a reboot/standby cycle.
- **Working (confirmation in progress)** , provisions and plays on real
  hardware via a trusted contributor; a final end-to-end stability pass
  on the current release is still being confirmed.
- **Expected** , same hardware platform as a verified model, no live
  proof yet.
- **Unknown** , different or unconfirmed hardware; no guarantee.

## Two hardware families (this is the important part)

SoundTouch speakers on AM335x split into two families that STR has to
provision and reach completely differently. Both run the same agent
binary; the difference is in how Wi-Fi and external reachability work.

### Series-II (classic): `rhino` (ST10) and `mojo` (ST30), module SM2

- Real `wlan0` interface; STR provisions Wi-Fi the documented way
  (`/addWirelessProfile` over the box's HTTP API, or `wpa_supplicant`).
- The STR agent's port `:8888` is reachable directly from the LAN
  (once `run.sh` punches the `INPUT ACCEPT` rule past the Bose firewall).
- This is the original, simplest path. ST10 is the reference target.
- Caveat for the SM2 ST20: its codename is `spotty`, and `run.sh` keys
  the reachability treatment off the codename, so an SM2 ST20 still gets
  the `:17008` REDIRECT (a harmless no-op if its chipset does not need
  it). Wi-Fi provisioning still follows the `wlan0` path.

### BCO (AirPlay-capable): `taigan` (Portable) / `scm`+`spotty` (ST20)

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
picker lists all 25 languages by their native name. Speakers with no
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

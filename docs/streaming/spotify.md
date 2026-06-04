# Spotify Connect: integration spike

Status: spike / design. Tracks #78. No code shipped yet.

## Goal

A SoundTouch speaker running STR should be linkable to one or more
Spotify accounts and then appear as a playback target ("device") in the
Spotify app, so a user picks it in the app and audio plays on the
speaker. The Spotify setting is network-wide: it is configured once in
the desktop app and rolled out to every speaker on the LAN.

## The constraint (why the old path is dead)

The SoundTouch firmware's built-in Spotify Connect hung off Bose-issued
Spotify partner credentials baked into the cloud. With the cloud gone and
the OEM agreement ended, the speaker's native Spotify source no longer
authenticates and cannot be revived. Playback therefore has to come from
a Spotify Connect implementation STR controls, not the speaker's dead
built-in source. See #78 for the longer history.

## How Spotify Connect actually works

A Connect "device" advertises itself on the LAN over mDNS/zeroconf
(`_spotify-connect._tcp`). The Spotify app discovers it and authenticates
through Spotify's own servers, so:

- **No password is stored on the device** in zeroconf/discovery mode. The
  user picks the device in their app and it just works.
- **Multiple accounts work for free**: anyone on the LAN sees the device
  and can take it over from their own app. This satisfies "one or more
  accounts" with zero per-account configuration.

This is the UX the goal asks for, and it is implemented by the open
`librespot` family.

## Building blocks (researched)

| Project | Lang | License | Zeroconf | Audio out | Notes |
|---|---|---|---|---|---|
| [librespot](https://github.com/librespot-org/librespot) | Rust | **MIT** | yes (discovery mode) | ALSA, pipe, subprocess, ... | Reference impl. MIT = clean to bundle. |
| [go-librespot](https://github.com/devgianlu/go-librespot) | Go | **GPL-3.0** | yes (builtin mDNS or avahi) | ALSA, PulseAudio, **pipe** (s16le/f32le) | Active. Has an HTTP control API. GPL matters, see below. |
| [AfterTouch / gesellix](https://github.com/gesellix/Bose-SoundTouch) | Go | - | - | - | Community post-cloud SoundTouch toolkit; references for the box side. |

### Licensing (decisive)

STR is MIT. **go-librespot is GPL-3.0, so its Go packages must never be
imported/linked into STR's Go code** (that would force STR to GPL).
Either implementation may only be used as a **separate sidecar binary**
invoked over a process boundary (exec + its HTTP API / pipe), which is
mere aggregation, not a derivative work. Given that, `librespot` (MIT) is
the preferred sidecar because shipping it carries no GPL distribution
obligation at all; go-librespot stays a fallback if its Go toolchain
makes cross-compiling materially easier, accepting that we then ship a
GPL binary and must provide its source/offer.

## Where does the audio go?

A Connect receiver decodes the Spotify stream to PCM and needs an audio
sink. On the box, Bose owns the audio output, which first looked like the
hard blocker. It is not: STR already plays audio on the box by pointing
its UPnP at an HTTP stream, so the on-box receiver can feed that same path
on loopback (see Architecture A). The design:

### Architecture A: on-box sidecar (the chosen path)

The STR agent ships and supervises a librespot sidecar **on the speaker**
in zeroconf mode, so the box itself appears as its own Connect device.
No PC has to be running; the network-wide config is rolled out to every
agent and each box self-advertises.

**The audio path is the key insight, and it is already solved in STR.**
STR does not write audio to ALSA; it plays on the speaker by pointing the
box's own UPnP AVTransport at an HTTP stream URL (the radio path,
Box:8091). Architecture A reuses exactly that:

1. on-box librespot runs in zeroconf mode (box = the Connect device);
2. when a user plays, librespot decodes to a **local HTTP stream** served
   by the agent's stream layer (pipe backend -> PCM/WAV over HTTP, or a
   light transcode);
3. the agent tells the box's own UPnP to play
   `http://127.0.0.1:<port>/spotify`.

So there is no direct ALSA access and no fight with Bose's audio
ownership: Spotify audio reaches the speaker over the same proven path as
radio. The remaining unknowns shrink to a hardware session:

- librespot ARMv7l build (Rust, MIT) and NAND footprint, stripped.
- sustained CPU on the weakest model (ST10): decode + serve/transcode.
- play / pause / seek latency through the Bose UPnP buffer.
- track metadata: surface the current title via the agent's now-playing.

### Architecture B: desktop bridge (fallback only)

librespot runs on the desktop host instead, advertising one device per
speaker and re-streaming to the box via UPnP. This was considered and
**rejected as the primary path**: its one distinguishing piece (re-stream
from the PC) is thrown away in A, so it does not de-risk A's real
question, and it forces the PC to stay on while the device shown is a
PC-hosted proxy, not the box itself. Keep B in reserve only if librespot
turns out not to run on the box at all (NAND / CPU limits).

## Network-wide config and rollout

Per the requirement, Spotify is one network-wide setting, configured in
the desktop app and applied to all speakers:

- A single `spotify` config object (at least: `enabled`, device-naming
  pattern, bitrate, normalization) lives in the desktop app.
- **Architecture A:** the desktop app pushes that config to every
  discovered agent (a new `/api/spotify/config` on the agent, same
  rollout pattern as presets/region). Each agent starts/stops and
  configures its own librespot sidecar. Each box self-advertises, so
  multi-box is natural.
- **Architecture B:** the desktop app owns N librespot instances (one per
  speaker) directly; "network-wide" is intrinsic since one app manages
  all of them. Heavier on the host (N decoders, N streams).
- Multi-account needs no rollout: zeroconf stores no credentials, so the
  same `enabled` config gives every account on the LAN access to every
  speaker.

## Proof-of-concept plan (Architecture A, on approval)

A hardware session on the test speaker (SSH to the maintainer's own box
on his LAN):

1. Cross-compile librespot for ARMv7l (Rust, MIT), strip it, and check
   the NAND footprint against free space.
2. Run it on the box in zeroconf mode; confirm the box appears in the
   Spotify app and a session starts (no audio yet).
3. pipe backend -> a minimal agent HTTP endpoint that serves the PCM/WAV
   stream on loopback.
4. Point the box's own UPnP AVTransport at `http://127.0.0.1:<port>/...`;
   confirm audio plays on the speaker and measure play/pause/seek latency.
5. Measure sustained CPU on the weakest reachable model.
6. Then wrap it: agent supervises the sidecar; a new `/api/spotify/config`
   receives the network-wide config the desktop app rolls out to all
   agents; surface track metadata via now-playing.

## Spike results so far

- **Build (transparent, from source):** `.github/workflows/librespot.yml`
  builds librespot v0.8.0 from source as a static-musl armv7 binary,
  size-optimised, Sigstore-attested, no opaque blob. Features:
  `with-libmdns` (zeroconf), `rustls-tls-webpki-roots` (pure-Rust TLS, no
  OpenSSL), `passthrough-decoder` (pass Ogg/Vorbis through so the Bose
  firmware decodes it, not the Cortex-A8). No ALSA.
- **Footprint:** the binary is **5.5 MB** (5,542,460 bytes). The box has
  ~20.4 MB free on `/mnt/nv`; the STR agent is 11.3 MB, so agent +
  librespot is ~17 MB, under budget. NAND is **not** a blocker, and a
  hand-rolled client would not save meaningful disk (a Go binary would be
  similar or larger). The library-vs-custom question therefore comes down
  to runtime RAM/CPU, not size.
- **Box profile (taigan Portable):** armv7l, single-core Cortex-A8
  (AM33XX) with NEON, glibc 2.15 (hence static musl), ~52 MB RAM
  available. CPU/RAM at runtime is the one open risk; `passthrough-decoder`
  exists specifically to keep decode off the box.
- **Still to measure on hardware:** runtime RAM, CPU during a session,
  and play/pause/seek latency through the box's UPnP buffer.

## Acceptance for closing #78

This doc plus a working (even hacky) PoC of the chosen path on at least
one model, and the remaining blockers before it can ship to all users
(licensing posture for the bundled sidecar, NAND footprint for A, the
PC-on caveat for B).

## Falsified / non-options

- Reviving the speaker's native Spotify source: impossible without Bose
  partner credentials.
- 30-second Web API previews: useless for real playback.
- Importing go-librespot into STR's Go modules: license-incompatible
  (GPL-3.0 vs MIT). Sidecar only.

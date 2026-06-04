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

## The hard part: where does the audio go?

A Connect receiver decodes the Spotify stream to PCM and needs an audio
sink. On our targets that is the crux, and it splits the design:

### Architecture A: on-box sidecar (the real goal)

The STR agent ships and supervises a librespot sidecar on the speaker in
zeroconf mode, so **the box itself appears as its own Connect device**.
Audio must reach the speaker's output, which the Bose firmware owns. Open
questions to resolve on real hardware:

- Can the sidecar write to an ALSA PCM the Bose pipeline exposes, or is
  there a loopback / AUX-style path STR can drive?
- ARMv7l build + NAND footprint (Rust librespot stripped, or go-librespot
  with `CGO_ENABLED=0`).
- Sustained CPU on the weakest model (ST10).

Best UX (box = device), highest risk. Needs a hardware session.

### Architecture B: desktop bridge (the low-risk PoC)

go-/librespot runs inside the desktop app host in zeroconf mode,
advertising one device per speaker (named like the speaker). It decodes
to PCM via the **pipe** backend, STR re-encodes that to an HTTP audio
stream, and points the speaker at it with UPnP `SetAVTransportURI`, the
exact path STR already uses for radio. No box-audio internals touched,
works today. Caveats: the host PC must stay on while playing, and the
device shown is the PC-hosted proxy "`<Speaker> (STR)`", not the box's
own Connect entry.

Recommendation: **prototype B first** to validate the UX and the
re-stream path end to end, while scheduling a hardware session to settle
A's audio question. A is the long-term target; B ships value immediately
and de-risks A.

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

## Proof-of-concept plan (next step, on approval)

1. Vendor a librespot sidecar binary (MIT) for the host OS; run it in
   zeroconf mode named "STR test".
2. Confirm it appears in the Spotify app and starts a session.
3. pipe backend -> minimal Go re-encoder (PCM -> HTTP) -> UPnP
   `SetAVTransportURI` to the test speaker (192.0.2.x), confirm audio.
4. Wrap as a desktop-app feature: per-speaker device, the network-wide
   `spotify.enabled` toggle, start/stop lifecycle.
5. Separately, a hardware session for Architecture A: probe the box audio
   sink options and the ARMv7l footprint.

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

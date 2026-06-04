# Spotify Connect: integration spike

Status: spike / design. Tracks #78. No code shipped yet.

## TL;DR (updated after live testing)

**Native Bose Spotify Connect still works and is the primary path; no new
code is needed for the common case.** Live-verified on a taigan Portable
running STR (2026-06-04): a Spotify app on the LAN discovered the speaker
("Living Room 9870"), it authenticated and played an on-demand track
(now_playing `source="SPOTIFY"`, the box connected to Spotify's AP on
port 4070, no stored account). librespot is kept as the future-proof
fallback (built + run-verified on the box, 5.5 MB) for if/when Spotify
ever drops the frozen eSDK. The rest of this document is the spike trail.

## Why native Spotify works without the Bose cloud

Spotify Connect has two login paths. Bose's app used the **account-linked**
one: enter your account in the Bose iOS app, the Bose cloud brokered the
Spotify OAuth and stored the account on the speaker so it appeared
everywhere. That broker (the Bose cloud) is dead, which is why the account
can no longer be changed from the Bose app.

But the **zeroconf path** is independent and alive: the speaker's eSDK
advertises Spotify Connect on the LAN; the user's Spotify app (logged in
to their account) discovers it and performs the login handshake **itself**,
handing the speaker a one-time credential derived from the app's own
session (via the eSDK's Diffie-Hellman exchange). The speaker logs in to
Spotify's AP (port 4070) directly and streams from the CDN. The Bose cloud
is never in this path, so it survived the shutdown. Evidence:
now_playing shows `sourceAccount="SpotifyConnectUserName"` with no stored
account, and the box holds a live TCP connection to a Spotify AP on 4070.

## Connecting accounts (there is no "linking" step)

Any Spotify account just: open the Spotify app on the same Wi-Fi as the
speaker, tap the device picker, choose the speaker, play. The app does the
auth; whoever connects last controls it. No Bose app, no stored account,
no cloud, no STR config. The only thing lost versus the old Bose flow is
the permanent "appears everywhere under one account" presence (that needed
the cloud-brokered stored account); the practical pick-and-play works.

STR's role: the marge stub answers the Bose cloud source-provider list,
so BoseApp enables the SPOTIFY source, loads the eSDK, and advertises
zeroconf. STR therefore likely delivers native Spotify essentially for
free (to confirm: whether a post-cloud box without STR leaves the source
disabled).

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

## Component choice: native eSDK now, librespot as the fallback

(Updated: the on-box test showed the native eSDK actually works via
zeroconf, see the TL;DR. So native is the primary path today; the
analysis below is why librespot is kept ready as the fallback for the
frozen-component risk, not why it is primary.)

The speaker already ships Spotify's official embedded SDK
(`/usr/lib/libspotify_embedded_shared.so`, dated Aug 2022) plus the audio
socket `/var/volatile/tmp/spotifyaudio.uds`. Bose used the account-linked
Connect model: you entered your Spotify account in the Bose iOS app, the
Bose cloud brokered the Spotify OAuth and pushed the credential to the
speaker, which logged in and registered with Spotify so it appeared in
your app everywhere. Only that broker (the Bose cloud) is dead; the eSDK
itself is intact.

Reusing the eSDK is in fact the primary path: it works today (zeroconf,
no cloud, verified). But it carries one structural risk that keeps
librespot in reserve:

- The eSDK is **frozen at Aug 2022 and will never be updated by Bose**.
  When Spotify next deprecates the protocol version or revokes the
  embedded partner key, native Spotify dies with no recourse, the exact
  vendor-abandonment failure STR exists to undo.
- Driving a closed Spotify C library with Bose's partner key outside
  Bose's flow is heavy reverse engineering with uncertain payoff.

### librespot on-box results (live, 2026-06-04)

Validated end to end on the taigan Portable except the final audio
plumbing:

- Builds + runs (static musl). OAuth login works; the credential is
  cached to `credentials.json` and is **persistent (survives reboot)** ,
  this is the session cache that makes autonomous presets possible.
- Authenticated as a Premium account and registered as a Spotify Connect
  device; it even shows up account-bound ("devices on another network")
  since local zeroconf is off on this box. **librespot refuses Free
  accounts** (`does not support "free" accounts`); the native eSDK does
  not, so Free users keep native live Spotify, presets are Premium-only
  (on-demand recall needs Premium anyway).
- **Idle RAM ~4 MB** (VmRSS 4208 kB), NAND 5.5 MB. Footprint is a
  non-issue.
- Control path works: pressing play in the app makes librespot decode and
  emit audio to the pipe backend.
- Audio routing is the one remaining build: run librespot with
  **`-P/--passthrough`** so it emits the raw Ogg/Vorbis stream (not PCM),
  have the agent serve that over HTTP (streamproxy), and point the box's
  own UPnP at it, so the Bose firmware decodes the Ogg (offloading the
  Cortex-A8). Then preset save/recall: a preset stores the Spotify URI,
  recall tells librespot to play it and routes the audio this way.
  (Pitfall found: without `--passthrough`, librespot writes raw PCM at
  ~176 KB/s; never let that land on NAND.)

librespot is the fallback because it is **open, actively
maintained (v0.8.0, Nov 2025), and STR controls its update path**: if
Spotify ever drops the frozen eSDK, we rebuild librespot from source and
ship it over OTA. It also does the
same **account-linked** model via its OAuth login (`librespot-oauth`), so
the familiar "appears in your Spotify app under your account" UX is
preserved, not just local zeroconf. This mirrors STR already replacing
the dead TuneIn integration with radio-browser. The eSDK reuse stays
documented only as a rejected alternative.

## How login works (no special Spotify partnership) and the data flow

librespot has no partner status with Spotify; it is reverse-engineered.
The robust, least-grey login is **zeroconf**, where Spotify's own app
does the authentication:

1. librespot advertises `_spotify-connect._tcp` on the LAN.
2. The user's official Spotify app (same LAN) discovers the device and,
   being legitimately logged in, performs the auth and hands librespot a
   reusable session credential. librespot itself does no OAuth here.
3. librespot logs in to `ap.spotify.com` with that credential and
   registers the device with Spotify's backend. From then on it shows up
   in the user's Spotify app, including remotely, exactly the
   account-linked UX Bose had. One-time LAN login, then account-bound.

Data flow during playback: control ("play on device X") goes through
Spotify's servers to librespot; librespot pulls the encrypted Ogg from
Spotify's CDN, decrypts it, and with `passthrough-decoder` hands the Ogg
through undecoded to the box, whose Bose firmware decodes and plays it
(Architecture A). Audio never streams phone-to-speaker directly.

A standalone OAuth path also exists (`librespot-oauth`, using a public
Spotify client id) but it is the more fragile, greyer route and is not
needed when zeroconf is used. The residual risk that Spotify breaks
unofficial clients is the reason for choosing a maintained OSS component
we can rebuild and ship over OTA.

The frozen Bose eSDK keeps no idle footprint to stop: `ps` shows no
Spotify process; the eSDK is loaded on demand only when Bose's (now
account-less, dead) Spotify source is selected. Integration just avoids
triggering it and ensures only librespot advertises Spotify Connect.

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
- **On-box run (taigan Portable, live):** the static-musl binary
  **executes** on the box (`librespot 0.8.0 ... exit 0`), confirming the
  build is compatible. With the 5.5 MB binary on `/mnt/nv` there is still
  14.6 MB free.
- **zeroconf is blocked on this box:** the kernel (3.14) has **no IPv6**
  (`/proc/net/if_inet6` absent, no `net.ipv6` sysctl), so libmdns's
  discovery server fails to bind (`os error 97`, EAFNOSUPPORT) and
  librespot, with no discovery and no credentials, exits. `avahi-daemon`
  is installed but not running.
- **Therefore the credential/OAuth path is the one for this hardware:**
  librespot with a provided credential authenticates straight to
  `ap.spotify.com` with no discovery server, side-stepping the IPv6
  issue, and it is exactly the account-linked model (device appears in
  the user's Spotify app). The zeroconf-is-cleaner point is moot here
  because the box cannot run the mDNS responder. (Alternative, not
  chosen: build `with-avahi` and run the box's avahi-daemon, more moving
  parts and Bose-mDNS conflict risk.)
- **Still to measure (needs a one-time OAuth login):** run librespot with
  cached OAuth credentials so it stays up, then idle RAM/CPU, CPU during
  a session, and play/pause/seek latency through the box's UPnP buffer.

## Spotify presets via librespot (multi-account design)

Native eSDK gives live Spotify but cannot recall a preset autonomously
(no persistent session). librespot with a cached credential can: a preset
holds a Spotify URI, recall tells librespot to play it, audio routes to
the box. The vision (which the Bose original never did): several household
members each log in their own Spotify account and save their own
playlists; a preset tile shows it is Spotify and whose account it is.

Data model (done, `internal/presets`): a preset carries `Type="spotify"`,
`URI` (the resource), and `Account` (whose it is) instead of a StreamURL.

Build phases:
- **P0 foundation (done):** preset model carries URI + Account, tested.
- **P1 single account:** agent supervises one librespot (cached cred,
  `--passthrough`), serves its Ogg over HTTP (streamproxy), recall =
  librespot play URI -> box UPnP plays the Ogg. Frontend: save the
  current Spotify now-playing as a preset (capture the URI) and play it.
- **P2 multi-account:** per-account cached credentials; a preset's
  `Account` selects which credential/session plays it (one librespot per
  account, or switch). OAuth done once per account.
- **P3 UI:** preset tiles show a Spotify badge + the account, so each
  member recognises their own.

Prerequisites that are their own work: the OAuth credential must reach
the box (desktop app does the OAuth in a webview, pushes the credential),
and librespot must be deployed (built in CI, see librespot.yml; shipped
on the stick / via OTA, MIT so bundling is fine after the security
audit).

## Before shipping (mandatory gates)

Once the on-box test confirms librespot works, these must be done before
it ships to any user:

1. **Security audit of the librespot source at the pinned tag.** STR
   bundles a binary that runs on users' speakers, so review for
   backdoors, malware, data exfiltration, and unexpected outbound
   endpoints; sanity-check the dependency tree (the Cargo.lock) and
   diff the pinned tag against upstream. Pin to an audited tag and only
   bump after re-auditing. Users must not be surprised one day.
2. **Credits / thanks.** Add librespot (librespot-org, MIT) to the
   project's credits, alongside the existing community acknowledgements.
3. **Website.** Add librespot to the credits/acknowledgements on
   st-reborn.de and describe the Spotify feature on the relevant page.
4. **Architecture.** Add the librespot sidecar + the Ogg-passthrough ->
   UPnP-loopback path to docs/ARCHITECTURE.md (component map + the
   external-dependency / data-flow tables) and the CLAUDE.md diagram.

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

# Qobuz

Status: **researched, not built. Recommendation: close the wish as covered
by casting apps (route C), and reopen it only if Qobuz grants access in
writing.** Vote thread: discussion #1757 ("Qobuz Connect support", opened
2026-11-05, 1 vote).

Researched 2026-11-23 from the code in this repository, read-only probes
of two STR speakers (a Portable, `scm`/`taigan`, and an ST10,
`sm2`/`rhino`, both firmware 27.0.6), the source code of six open Qobuz
Connect projects, and public sources. No Qobuz account was available, so
nothing here was played.

The wish is Qobuz Connect: pick the SoundTouch in the Qobuz app and play
to it, as Spotify Connect does with STR today. There are three ways Qobuz
could reach a SoundTouch speaker:

| Route | What plays the music | Feasibility | Legal exposure |
|---|---|---|---|
| A | the speaker firmware's own Qobuz client | none: the firmware has no Qobuz client | none |
| B | an unofficial Qobuz Connect receiver in STR (on the speaker or in the desktop app) | technically easy: small, pure Go, no decryption needed, fits the speaker | high: needs Qobuz's own app credentials; Qobuz had an open client with Qobuz Connect taken down in September 2026 |
| C | a phone app with a licensed Qobuz integration casting to the speaker's UPnP renderer, AirPlay, or Bluetooth | works today in principle, untested here | none for STR |

The short version: Qobuz Connect needs no device certificate (unlike
Tidal Connect), so the technical door is open, but every open receiver
walks through it with Qobuz's own application id and secret, and Qobuz
has just shown that it enforces against exactly that. In #1757 the
answer was "before I build on one I want to see it hold up and see that
Qobuz tolerates it". The first half holds; the second does not.

## What the firmware does with Qobuz

Nothing. Firmware 27.0.6 has no Qobuz client, and Bose never offered
Qobuz on SoundTouch.

- `GET :8090/sources`, `/serviceAvailability`, `/supportedURLs` and
  `/info` on both probed speakers (2026-11-23): no occurrence of
  "qobuz" in any answer, in any case.
- The source-provider catalogue the firmware resolves accounts against
  (the 25 numbered providers STR serves in
  `internal/marge/responses.go`, `respondSourceProviders`) has no Qobuz
  entry. It does have `TIDAL` (24), `DEEZER` (14), `QQMUSIC` (19).
- **`QPLAY` is not Qobuz.** Every speaker lists two `QPLAY` sources
  (`UNAVAILABLE`), and the UPnP renderer reports `qplay://` as its idle
  `CurrentURI`. QPlay is Tencent's casting protocol for QQ Music, the
  source behind provider 19. The letter Q is a coincidence; nobody
  should read these entries as a dormant Qobuz client.

So there is no route A, and nothing STR's stand-in could revive. Route A
is closed by fact, not by test.

## How Qobuz Connect works

Taken from the source of the open receivers listed below (all agree);
Qobuz publishes no documentation. Qobuz launched Qobuz Connect on
2025-05-15, built with StreamUnlimited (it ships in their StreamSDK
25.02 for hardware brands) and reported more than 100 partners in
December 2025.

1. **Discovery.** The receiver advertises `_qobuz-connect._tcp` over
   mDNS with a TXT record (`path=/streamcore`, a device type, a name, a
   UUID, an SDK version string). Plain mDNS, the same mechanism STR's
   agent already uses (`discovery/`, `internal/mdnshost`).
2. **Handshake.** The Qobuz app calls the receiver over plain HTTP:
   `GET /streamcore/get-display-info` (name, brand, maximum quality),
   `GET /streamcore/get-connect-info` (the receiver's Qobuz app id and
   current session), then `POST /streamcore/connect-to-qconnect` with
   two short-lived JWTs: one for Qobuz's Connect WebSocket (with its
   endpoint URL) and one for the REST API.
3. **Session.** The receiver opens the WebSocket at Qobuz, authenticates
   with the JWT, and from then on exchanges protobuf messages: queue,
   play, pause, seek, volume, state reports back to every app in the
   session.
4. **Audio.** The receiver fetches each track itself:
   `track/getFileUrl` (signed request, `format_id` 5 = MP3 320, 6 = FLAC
   16 bit / 44.1 kHz, 7 = FLAC 24 bit up to 96 kHz, 27 = FLAC 24 bit up
   to 192 kHz, `intent=stream`) returns a signed CDN URL to an
   unencrypted file. A newer endpoint (`file/url` after `session/start`)
   returns encrypted CMAF segments (AES-128, a key wrapped per session);
   the open projects treat `getFileUrl` as the legacy path that still
   works and needs no key.
5. **Reporting.** `reportStreamingStart` / `reportStreamingEnd` tell
   Qobuz what was played, which is how artists get paid.

**No device certificate, no partner key in the handshake.** This is the
difference from Tidal Connect, where the Tidal app only talks to a
receiver that presents a Tidal-issued certificate. The Qobuz app showed
the open receivers below and played to them through 2026.

**But the receiver needs an application id and secret.** Step 4 is a
request signed with an app secret (an MD5 over the method, the
parameters, a timestamp and the secret), and every open receiver gets
that pair from Qobuz's own software:

- qobuz-proxy hardcodes the id and secret of the Qobuz desktop app, and
  logs the user in through Qobuz's OAuth page under that desktop app's
  id.
- gobz-connect and pibuz scrape the web player (`play.qobuz.com`): they
  download its JavaScript bundle and rebuild the secret from obfuscated
  pieces (a seed and per-timezone fragments, then Base64).
- In every case the receiver tells the Qobuz app that it is that
  borrowed application (`get-connect-info`).

Qobuz does not hand out its own ids: "We are not able to provide API
credentials to individual users" (Qobuz, relayed in the Roon community,
2022-01-03). Its old public API documentation repository on GitHub
(`Qobuz/api-documentation`) is gone (404 on 2026-11-23); a forum post
quotes Qobuz saying it removed open access to the API "because of stream
ripping" (unverified).

## Route B: an unofficial Qobuz Connect receiver in STR

### The open projects (2026-11-23)

| Project | Language, licence | State | Credentials | Audio | Notes |
|---|---|---|---|---|---|
| leolobato/qobuz-proxy | Python, MIT | created 2026-02-09, v1.7.7 2026-10-05, 48 stars, active PRs | Qobuz desktop app id and secret, hardcoded | **bridge to DLNA renderers**: proxies the CDN file over plain HTTP, picks the quality from the renderer's `GetProtocolInfo` | the STR shape; reports streams; a SoundTouch owner runs it on two speakers (issue #43) |
| bufadu/gobz-connect | Go, MIT | created 2026-09-18, last push 2026-10-15, 0 stars | scraped from the web player | `getFileUrl`, decodes locally (beep/oto) | deps overlap STR's (`gorilla/websocket`, `grandcat/zeroconf`, plus `protobuf`); 8.6k lines |
| PhilipVinc/pibuz | Rust, MIT | v2.8.1 2026-11-14, very active | scraped from the web player | CMAF with AES decryption, `getFileUrl` as fallback; ALSA | grown out of QBZ's crates; stream reporting still an open issue (#21) |
| nickblt/qonductor | Rust, no licence | 2025-12 to 2026-04 | n/a (protocol library) | n/a | the first public protocol implementation; no licence means no reuse |
| ahcm/qconnect | Rust, EUPL-1.2 | 2026-04 to 2026-05 | from QBZ | CMAF with AES | based on QBZ's crates; aarch64 binary 6.9 MB gzipped |
| tobiasguyer/StreamCore32 | C, own licence | active to 2026-10 | n/a in README | MP3 / FLAC to a VS1063 decoder | runs Spotify and Qobuz Connect on an **ESP32-S3 with 8 MB PSRAM**; credited by qobuz-proxy for the protocol work |
| ciaens/qobuz-connect, delleceste/qobuzconnect2mpd, others | Rust (MIT), C++ (no licence) | 2026 | various | various | small, one author each |
| vicrodh/qbz | Rust | **taken down by its author at Qobuz's request, September 2026** | web player id and secret | CMAF, offline cache | the desktop client that started most of the Rust crates above; see "Legal" |

### Would it fit on the speaker?

Yes, comfortably, and more easily than Spotify did:

- **No decoding, no decryption.** Requesting `format_id` 6 returns FLAC
  16 bit / 44.1 kHz, which the speaker decodes itself (measured on the
  Portable: FLAC 16/44.1 plays; FLAC 24/96 fails with
  `AUDIO_ERROR_DECODER`; `docs/streaming/usb-stick.md`). Qobuz serves the
  CD-quality rendition of hi-res albums on request, so a downsampling
  proxy is not needed at all; the receiver simply never asks for 7 or 27
  and advertises `LOSSLESS` as its maximum. MP3 320 (`format_id` 5) is
  the fallback.
- **No second binary.** The receiver is mDNS, four HTTP handlers, one
  WebSocket with a handful of protobuf messages, signed REST calls, and
  a pass-through HTTP proxy. STR's agent already has the WebSocket and
  mDNS libraries, a stream proxy (`internal/streamproxy`) and the UPnP
  path to `:8091` (`internal/upnp`), and plays its USB files the same
  way from `127.0.0.1:8888`. Pure Go, `CGO_ENABLED=0`, `GOARM=5`.
  Estimated growth of the agent: well under 1 MB with hand-written
  protobuf (`protowire`), 1 to 2 MB with the full protobuf runtime.
  Against the 16 MB Spotify engine this is small; tight speakers
  (`docs/streaming/spotify.md`, NAND) would hold it.
- **RAM.** A few MB at most: one WebSocket and a streaming copy. The
  existence proof is StreamCore32, which runs Qobuz Connect next to
  Spotify Connect on an ESP32-S3.
- **HTTPS.** The speaker rejects `https` URIs (UPnP 402, measured in the
  Tidal spike, PR #1953). The Qobuz CDN hosts answer plain `http` with
  the same status as `https` and without a redirect (403 / 400 without a
  token, probed 2026-11-23), so a signed URL may well work over `http`
  as Tidal's did; untested, and moot because the agent would proxy the
  stream anyway: qobuz-proxy refreshes the signed URL after 240 s
  because it expires after about five minutes, which a speaker that
  pauses or seeks with `Range` would otherwise hit.
- **Reachability.** The Qobuz app must reach the receiver's HTTP port
  from the phone. On whitelisted chassis only `:17008` is redirected to
  the agent (`docs/MODEL-VARIANTS.md`); the receiver would advertise the
  port the phone can actually reach, the problem the Spotify engine's
  zeroconf endpoint already had.
- **SoundTouch renderer quirks** are known from qobuz-proxy issue #43
  and its fork's fixes (2026-11-06 and 2026-11-16): `CurrentURI` is
  empty while playing (use `TrackURI`), the renderer reports `qplay://`
  after Stop and at the end of a track, drops `TrackURI` to a dummy
  after a pause into standby, and rejects `SetNextAVTransportURI` with
  401 (no gapless). STR's own UPnP code meets the same renderer every
  day.

What a build would be: Qobuz as a Connect device per speaker (the
speaker's own name in the Qobuz app), a Qobuz login once per household
(OAuth in the desktop app or phone page), the play report to Qobuz, and
later a Qobuz album or playlist on a hardware key (the agent holds the
token, so it could start a queue without the app). About 8 to 12 days
including the login UI, the renderer quirks and a field test; the
receiver core alone about 5.

### Why route B is not built: legal

- **Qobuz enforces against open clients, including Qobuz Connect.** The
  QBZ desktop client (MIT, Rust; thousands of users, Linux, macOS,
  Windows) was taken down by its author in September 2026 after Qobuz
  asked him to. In the repository's README ("The end of the line") and
  in its issue #4 (2026-10-13) he lists what a "compliant" version would
  have had to drop: "Q Connect (acceptable, a shame, but acceptable,
  because it's a product they sell)", the offline cache ("it exposes the
  encrypt/decrypt of the tracks"), and the login, "because it extracts
  the app secrets/id from the web and acts a web player". He had asked
  Qobuz for formal access in April 2026 and offered to ship the
  sensitive parts as closed binaries; the answer, months later, was the
  request to shut down. A Qobuz support reply that users shared
  (Tildes, early October 2026; unverified) says the app "placed Qobuz in
  a non-compliant position" toward rights holders and "leads to streams
  not being counted".
- **An STR receiver would do two of those three things**: borrow
  Qobuz's application credentials, and be a Qobuz Connect device that
  Qobuz sells to hardware partners. Reporting the streams (as
  qobuz-proxy does) answers the counting complaint, not the credentials
  or the Connect licensing.
- **Terms of use** (Qobuz GCUS, Xandrie SA, read 2026-11-23): Art. 4 names
  the site, the Qobuz player, the desktop and the mobile apps as the
  ways to listen; Art. 7 forbids modifying or decompiling Qobuz's
  applications and circumventing protection measures; Art. 17 and
  21.8.3 forbid bypassing "the technical measures implemented to
  protect the works"; Art. 12 lets Qobuz delete or deactivate an
  account "without notice or compensation". The owner's account is at
  stake, not only the project.
- **Germany, §95a UrhG.** The OLG Hamburg (21 Nov 2024, 5 U 54/23,
  youtube-dl) held that YouTube's "rolling cipher", an obfuscated
  signature on the stream URL, is an effective technical measure, and
  that a tool built around getting past it falls under §95a(3). Qobuz's
  stream URLs are guarded the same way: a request signature with a
  secret that the web player ships obfuscated, and that the receivers
  rebuild from its bundle. That is a closer match than Deezer's or
  Tidal's cases, and no decryption is needed for it to apply. The CMAF
  path (AES) would be clear encryption on top and is out of the
  question. No court has ruled on Qobuz; this is an inference, not
  legal advice.
- **Who would be hit.** STR is a public German project under one named
  maintainer; a Qobuz request would reach that person directly, and the
  project's own credibility rests on "no Bose material, no grey zone".
  Qobuz's tone with QBZ was polite, but the outcome was a shutdown.

Verdict: technically the easiest streaming service STR has looked at
(no certificate, no decryption, no transcode, fits the speaker), legally
the one where the provider has most recently and most specifically
acted against exactly this feature.

### What would change the verdict

Written permission from Qobuz: an application id for STR, or admission
to the Qobuz Connect partner programme. Two addresses: Qobuz's API
contact (third-party sources name `api@qobuz.com`; not confirmed on
Qobuz's own site) and the Qobuz Connect partner route through
StreamUnlimited. The odds are low (QBZ asked first and was shut down;
Qobuz says it cannot give credentials to individuals), but one e-mail
costs nothing and is the honest answer to the thread. If Qobuz says yes,
everything in "Would it fit on the speaker" stands and route B is
8 to 12 days.

## Route C: casting from a phone, AirPlay, Bluetooth

Every SoundTouch is a UPnP MediaRenderer on `:8091` (MP3, AAC, FLAC and
WAV up to 48 kHz, plain `http` only). The Qobuz app itself does not cast
to UPnP/DLNA (Chromecast, AirPlay and Qobuz Connect only; no source shows
a DLNA output in the official app). Apps with a built-in Qobuz login do:

- **BubbleUPnP** (Android): Qobuz library and playback to any UPnP
  renderer, long established. Its setting "Proxy Qobuz tracks"
  (Renderer settings, Qobuz) makes the phone relay the stream, which is
  what a renderer that refuses `https` or stutters needs; the developer
  recommends it for such renderers and says it does not change the
  quality. The phone must keep BubbleUPnP running.
- **mconnect Player** (iOS and Android): Qobuz to UPnP renderers.
- **AGD Stream HD** (Android, updated 2026-08) lists Qobuz to UPnP
  renderers.

These integrations log in with the app vendor's own arrangement with
Qobuz, not with anything STR provides; STR builds nothing and carries no
exposure.

Without any third-party app:

- **AirPlay** from the Qobuz app on iPhone, iPad or Mac, on speakers
  whose `/sources` lists `AIRPLAY` `READY` (the probed Portable does; the
  probed ST10 lists it `UNAVAILABLE`).
- **Bluetooth**, on every SoundTouch model.

What none of these gives: the speaker in the Qobuz app's own Connect
list, a hardware key with Qobuz content, or playback with the phone
away. Hi-res is not available on a SoundTouch by any route; the cast
apps should be set to CD quality (16 bit / 44.1 kHz), or a 24/96 album
will fail on the speaker's decoder.

Also seen: qobuz-proxy (route B's shape, run by the owner on a PC, NAS
or Home Assistant) already drives SoundTouch speakers (qobuz-proxy issue
#43, a fork with SoundTouch fixes). It is a third-party tool with the
legal questions above; STR should neither bundle nor recommend it.

## Tests that settle the open points

None needs a test build in a user's hands. The route B tests are not
listed: they would need the unofficial API this report advises against.

1. **Firmware knows no Qobuz, on every chassis** (no account, GET only).
   Done on a Portable and an ST10; repeat on an ST30, an ST20 and a Wave
   if at hand:
   `for p in sources serviceAvailability supportedURLs info; do printf '%s ' $p; curl -s -m 8 http://<speaker-ip>:8090/$p | grep -ci qobuz; done`
   Every line `0` confirms route A is closed on that chassis.
2. **Which speakers can take AirPlay** (no account, GET only):
   `curl -s -m 8 http://<speaker-ip>:8090/sources | grep -o '<sourceItem source="AIRPLAY"[^>]*>'`
   `status="READY"` means the FAQ's AirPlay line applies to that model.
3. **BubbleUPnP to the Portable** (Qobuz account or trial, Android;
   maintainer runs; Portable at volume 5, set in the STR app first).
   1. BubbleUPnP: Library, Qobuz, log in. Renderer: the Portable.
      Renderer settings, Qobuz: quality CD (16 bit), "Proxy Qobuz
      tracks" off.
   2. Play a CD-quality album. Read
      `curl -s -m 8 http://<portable-ip>:8090/now_playing` (source,
      `playStatus`) and listen 30 s.
   3. Turn "Proxy Qobuz tracks" on and repeat.
   4. Skip to the next track from BubbleUPnP; let one track end by
      itself.
   5. Set quality to Hi-Res and play a 24/96 album.
   6. While it plays, press key 1 on the speaker; then open the STR app.

   Reading the result: step 2 plays means BubbleUPnP hands the speaker a
   URL it accepts; step 2 fails and step 3 plays means the FAQ must say
   "turn on Proxy Qobuz tracks". Step 4 decides whether track changes
   work with this renderer (it rejects `SetNextAVTransportURI`, so the
   app has to Stop and Play). Step 5 is expected to fail
   (`AUDIO_ERROR_DECODER` or a skip) and decides the "set CD quality"
   line. Step 6 passes when the STR preset plays and the STR app shows
   the speaker sanely (no stuck state, no error); it shows STR and an
   outside UPnP controller coexist.
4. **mconnect Player** (Qobuz account, iOS; maintainer runs; Portable at
   volume 5): steps 1 to 5 of test 3 with mconnect (it has no proxy
   switch; if it fails where BubbleUPnP with proxy works, the FAQ names
   BubbleUPnP for Android and AirPlay for iOS instead).
5. **AirPlay from the Qobuz app** (Qobuz account, iPhone; maintainer
   runs; Portable at volume 5): pick the Portable in the AirPlay menu of
   the Qobuz app, play, read `:8090/now_playing` (source `AIRPLAY`).
   Pass means the FAQ's iOS answer needs no third-party app.

Cost: one evening with Qobuz's free trial. Every step is undone by
stopping playback; nothing is written to the speaker.

### AirPlay per model, checked 2026-11-23

Read from `:8090/sources` on the maintainer's speakers, all on firmware 27.0.6:

| Model | `AIRPLAY` source |
|---|---|
| SoundTouch Portable (two units) | `READY` |
| SoundTouch 30 (scm chassis) | `READY` |
| SoundTouch 10 (two units) | `UNAVAILABLE` |

The firmware registers AirPlay only where the speaker's AirPlay authentication chip answers, so a SoundTouch 10 has Bluetooth as its only phone route. Any answer that points users to AirPlay has to name that.

## Summary for planning

**Verdict: close as covered by route C** (BubbleUPnP or mconnect casting
to the speaker's UPnP renderer, AirPlay from the Qobuz app where the
speaker has it, Bluetooth everywhere). **Build route B only if Qobuz
grants STR an application id or Qobuz Connect partnership in writing.**

**User-facing outcome:** no "SoundTouch" in the Qobuz app's Connect
list. Qobuz plays on a SoundTouch from BubbleUPnP (Android) or mconnect
(iOS, Android) at CD quality, from the Qobuz app over AirPlay on
AirPlay-capable models, and over Bluetooth on all. No hardware key with
Qobuz content. One FAQ answer on the website, nothing in the app.

**Legal exposure:** route C none for STR. Route B high: Qobuz's own
application credentials (Terms Art. 7, §95a UrhG by analogy with the
youtube-dl ruling), and Qobuz had an open client remove exactly this
feature in September 2026.

| Step | Effort | UI | Needs |
|---|---|---|---|
| Probes: firmware has no Qobuz (route A) | done (Portable, ST10) | none | nothing |
| Tests 1 and 2 on the remaining chassis | 10 minutes | none | the speakers |
| Tests 3 to 5 (cast apps, AirPlay) | one evening | none | a Qobuz trial, an Android phone, an iPhone |
| FAQ answer "Qobuz on SoundTouch" (website, EN and DE) | half a day | none | tests 3 to 5 for the exact wording |
| Ask Qobuz in writing for an app id or Connect partnership | one e-mail | none | nothing |
| Route B (receiver in the agent, login, play reports, keys) | 8 to 12 days | Qobuz login card, Connect device per speaker | Qobuz's written yes |

**Tests for a real speaker** (details above):

1. `for p in sources serviceAvailability supportedURLs info; do printf '%s ' $p; curl -s -m 8 http://<speaker-ip>:8090/$p | grep -ci qobuz; done`
   on each remaining chassis; all `0` closes route A there. No account.
2. `curl -s -m 8 http://<speaker-ip>:8090/sources | grep -o '<sourceItem source="AIRPLAY"[^>]*>'`;
   `READY` puts the model in the FAQ's AirPlay line. No account.
3. BubbleUPnP to the Portable, proxy off then on, CD then Hi-Res, skip,
   track end, key 1 during the cast; read
   `curl -s -m 8 http://<portable-ip>:8090/now_playing`. Decides the
   proxy and quality wording and STR coexistence. **Maintainer runs,
   Portable at volume 5, needs a Qobuz account.**
4. mconnect Player on iOS, same steps without the proxy switch.
   **Maintainer runs, Portable at volume 5, needs a Qobuz account.**
5. Qobuz app on an iPhone, AirPlay to the Portable, read `now_playing`
   (source `AIRPLAY`). **Maintainer runs, Portable at volume 5, needs a
   Qobuz account.**

## Sources

- Discussion #1757 and the reply of 2026-11-05.
- Probes of 2026-11-23: `:8090/sources`, `/serviceAvailability`,
  `/supportedURLs`, `/info` on a Portable (`scm`) and an ST10 (`sm2`),
  firmware 27.0.6; Qobuz CDN hosts over `http` and `https`, no token.
- qobuz-proxy: https://github.com/leolobato/qobuz-proxy (source read:
  `connect/discovery.py`, `auth/oauth.py`, `auth/api_client.py`,
  `backends/dlna/proxy_server.py`, `backends/dlna/capabilities.py`);
  SoundTouch issue https://github.com/leolobato/qobuz-proxy/issues/43
- gobz-connect: https://github.com/bufadu/gobz-connect (`scraper.go`,
  `api.go`, `go.mod`)
- pibuz: https://github.com/PhilipVinc/pibuz (`crates/qbz-qobuz/src/client.rs`)
- qonductor: https://github.com/nickblt/qonductor
- qconnect: https://github.com/ahcm/qconnect
- StreamCore32: https://github.com/tobiasguyer/StreamCore32
- QBZ: https://github.com/vicrodh/qbz (README "The end of the line",
  issue #4)
- Qobuz support reply as shared on Tildes:
  https://tildes.net/~music/1wd2/ (unverified)
- "We are not able to provide API credentials to individual users":
  https://community.roonlabs.com/t/is-there-a-qobuz-rest-api/183752
- Qobuz terms: https://www.qobuz.com/us-en/legal/terms
- StreamUnlimited, Qobuz Connect in StreamSDK:
  https://www.streamunlimited.com/qobuz-connect-now-available-via-streamsdk/
- Qobuz, 100 Connect partners:
  https://community.qobuz.com/press-en/qobuz-connect-celebrates-over-100-partners
- BubbleUPnP "Proxy Qobuz tracks":
  https://groups.google.com/g/bubbleupnp/c/Z4rOFKwfgck
- OLG Hamburg, 21 Nov 2024, 5 U 54/23 (youtube-dl), as cited in
  `docs/streaming/deezer.md`.

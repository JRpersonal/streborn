# SiriusXM, and the other services the firmware once carried

Status: **research only, nothing built.** Vote thread: idea #997 (closed
duplicate #1103). Researched 2026-11-23 from the code in this repository,
read-only `GET` probes of two STR speakers (a Portable, scm, and an ST10,
sm2, both 27.0.6), the neighbour project AfterTouch
(gesellix/Bose-SoundTouch), and public sources. No SiriusXM account was
available and nothing was played.

The short answer to the question promised in #997 (2026-11-02): the
firmware's SiriusXM source did **not** talk to SiriusXM directly. Since
2019 it was a BMX service whose adapter ran in Bose's cloud, so the
Pandora pattern (link an account, the speaker's own client does the rest)
cannot bring it back.

There are three ways SiriusXM could reach a SoundTouch speaker:

| Route | What plays the music | Feasibility | Legal exposure |
|---|---|---|---|
| A | the speaker firmware's own SiriusXM source | the Pandora shape: not possible; STR as the BMX adapter: possible, but needs everything route B needs | as route B |
| B | an unofficial SiriusXM client run by STR, audio to the speaker over UPnP | technically a week, needs a US tester | medium: against SiriusXM's terms; decrypts AES-128 HLS with the key the service hands the subscriber |
| C | something outside STR (Music Assistant, AirPlay, Bluetooth) | works today where the pieces exist | none for STR |

Recommendation: answer #997 with the finding, keep it open as "build if a
US tester commits and the sidecar shape is accepted", and point to route C
for today. Do not build route A as a BMX adapter unless route B exists
first; it would only add the firmware's own source badge on top of it.

## What the firmware does with SiriusXM

1. **Two SiriusXM identities exist.** The source-provider catalogue knows
   `SIRIUSXM` (provider id `17`, dated 2014) and `SIRIUSXM_EVEREST` (id
   `38`, dated 2019-11-25, "SiriusXM Everest"). STR's catalogue
   (`respondSourceProviders` in `internal/marge/responses.go`) stops at id
   `25` and serves only the old `17`. AfterTouch serves all 43 entries
   (`pkg/service/constants/constants.go`). *Established.*

2. **The current one is a BMX service, not a firmware client.** The BMX
   registry the Bose cloud served (`content.api.bose.io/bmx/registry/v1/services`,
   as recorded by AfterTouch in `pkg/service/handlers/static/bmx_services.json`)
   declares `SIRIUSXM_EVEREST` with
   `baseUrl {BMX_SERVER}/core02/svc-bmx-adapter-siriusxm-everest-eco1/prod/live-adapter`,
   links `/availability`, `/token`, `/navigate/`, `/logout`, stream types
   `liveRadio` and `onDemand`, and
   `authenticationModel: {"loginPageProvider": "BOSE"}`. So the login page
   came from Bose, and the adapter that held the SiriusXM session and
   handed the speaker its playback documents was a Bose cloud service
   (the same family as the Orion adapter that STR already stands in for,
   `svc-bmx-adapter-orion`). TuneIn and the custom stations use the same
   mechanism, and STR serves both (`internal/marge/bmxservices.go`,
   `internal/webui/bmxtunein.go`). The soundcork project reached the same
   conclusion independently: "the `bmx` server seems to be required for
   TuneIn radio, custom radio streams, and SiriusXM". *Established.*

3. **Pandora is different, and that is why it works.** The firmware's
   configuration names Pandora's own tuner hosts (`device-tuner.pandora.com`,
   AfterTouch's `docs/analysis/UPSTREAM-URLS`), and STR's US-services work
   confirmed that the Pandora client logs in to Pandora itself and only
   registers the account with the stand-in (`docs/streaming/us-services.md`).
   No SiriusXM host appears in that list. For SiriusXM, the speaker is a
   generic BMX client: it asks the adapter for a token, a menu and a
   playback document, and plays the stream URL it is given.
   *Strong inference; confirm with test 2 below.*

4. **What the speakers show today.** On both probed speakers neither
   `/sources` nor `/serviceAvailability` names `SIRIUSXM` or
   `SIRIUSXM_EVEREST` (STR does not offer either), and `/supportedURLs` is
   identical on both (the same 99 routes; nothing SiriusXM-specific, only
   the generic `setMusicServiceAccount`, `setMusicServiceOAuthAccount`,
   `removeMusicServiceAccount`, `navigate`, `select`). Country codes were
   `EU` (Portable) and `GB` (ST10). *Established.*

5. **What STR's stand-in already does with it.** `boxsnapshot` lists
   `SIRIUSXM` as a lost cloud service, `numericProviderID` maps it to `17`
   for a reflected entry, and `transportsource.go` treats it as a live
   source without skip. Nothing else. A reflected `SIRIUSXM` entry would
   be named in `/full`, but with nothing behind it.

### What the stand-in would have to serve for the native source

Unlike Pandora, a stored registration is not enough. For the firmware's
`SIRIUSXM_EVEREST` source to play, STR would have to be the adapter:

- provider `38` in the catalogue and a `SIRIUSXM_EVEREST` entry in the BMX
  registry, pointing at the agent;
- the account: either a Bose-style login page (the
  `loginPageProvider: BOSE` model, which no longer exists) or a source
  written into `/full` by STR with an STR-defined credential;
- the adapter's `/token`, `/navigate` (channel list) and playback documents,
  where the playback document's stream URL points at STR's stream proxy;
- behind that, a SiriusXM client: login, session refresh, channel list,
  the HLS playlist, its AES-128 key, decryption, and the HLS to ADTS
  conversion STR already has.

The last point is all of route B. Route A therefore only adds the
firmware's own source on top of route B. Its one gain would be the
firmware's native preset handling; STR's own presets already put any
stream on keys 1 to 6, so the gain is small.

## Region and testing

SiriusXM streaming is sold in the United States and, as a separate
service, in Canada. Its web terms (effective 2018-01-19) say the
Streaming Service "is intended to be enjoyed within the Service Area only"
and forbid "spoofing of a web device"; one device at a time per
subscription. Whether the API refuses a non-US IP was not verified (the
aiosxm README says nothing about it).

For STR this means:

- The maintainer, in Germany without an account, can run the black-box
  test of the firmware's BMX behaviour (test 2) and every no-audio test.
- Every login, channel list and playback test needs a US or Canadian
  subscriber with an active streaming plan (satellite-only plans do not
  stream). Pandora's route A was confirmed the same way, by a tester with
  a US connection (2026-10-11).
- A test build in a user's hands is not an option (standing rule: users
  test release builds only), so route B would ship as a release feature
  marked experimental, with forensics in the bundle, like Deezer A2.

## Route B: an unofficial SiriusXM client

The Spotify pattern: a client logs in with the owner's account, STR turns
the stream into something the speaker plays over UPnP.

| Project | Language, licence | State (2026-11) | API | Notes |
|---|---|---|---|---|
| aiosxm (MizterB) | Python, MIT | 0.3.1, 2026-10-14, active | current `api.edge-gateway.siriusxm.com` (the web player's) | session with refresh-token cookie; bundled proxy re-serves playlist, AES-128 key and segments; "solely for personal use by individual subscribers" |
| Music Assistant `siriusxm` provider | Python, Apache-2.0 | stage `stable`, moved to aiosxm on 2026-08-28 | via aiosxm | ffmpeg decrypts using the key URL the aiosxm proxy serves |
| sxm-client, sxm-player (AngellusMortis) | Python, MIT | last push 2022 and 2021 | legacy `player.siriusxm.com/rest` | the legacy endpoints "no longer respond" (aiosxm README) |
| andrew0/SiriusXM | Python | archived 2024-01 | legacy | the base most older forks copied |
| PR #737 (a contributor's native Go client) | Go | closed 2026-08-27 | legacy, with a fixed key blob | redirected to a sidecar; see #997 |

What a Go port would need, all inside the agent or a sidecar on the
speaker:

- login and refresh against the edge gateway (port of aiosxm's auth);
- the channel list, for a "SiriusXM" source in Find (desktop and phone);
- per channel the HLS playlist and its key (`stream.get_key()` in
  aiosxm), AES-128-CBC decryption of each segment (`crypto/aes`, standard
  library), and STR's existing HLS to ADTS conversion
  (`internal/streamproxy/hls.go`, `mpegts.go`; today it has no
  `EXT-X-KEY` support at all);
- the session token kept on the speaker (0600, like the Deezer kept
  login), so a key press plays with the computer off; never the password,
  never in a log or bundle;
- presets: the agent's own stream URL per channel, stored like a radio
  station, so no firmware change is involved.

Size is not the problem: the decrypt is a few hundred lines. Placement
is: `CONTRIBUTING.md` says a reimplemented client for a paid service is a
sidecar in its own repository, and the #737 contributor was told exactly
that. A sidecar on the speaker competes with the Spotify engine for NAND
on the ST30 and the Portable; a sidecar on a computer loses the "keys work
with the computer off" property.

### Legal footing

Better than Deezer, worse than Pandora route A:

- **Terms.** SiriusXM's terms forbid reverse engineering (D.1), limit use
  to personal use (C.4), one device at a time and the Service Area (C.3).
  An unofficial client is outside what they allow; the owner's account is
  what is at stake.
- **Encryption.** Unlike Deezer (a fixed secret taken from Deezer's own
  clients), the current API hands the logged-in subscriber the AES-128 key
  for the stream, and any standard HLS player decrypts with it. The legacy
  approach in #737 shipped a fixed key; a port must not.
- **Enforcement.** SiriusXM does watch GitHub: the public github/dmca
  repository holds six SiriusXM notices (2020-05, 2020-06, 2021-04,
  2023-02, 2025-06, 2025-07), filed by outside IP counsel. Every one is
  about SiriusXM's own leaked code (website scripts, supply-chain code, an
  Android app's source, head-unit source), and each answers "No" to
  claiming a technological access-control measure. None targets a
  third-party client: sxm-client, aiosxm and Music Assistant's provider
  (a large, visible project that ships it as stable) are untouched.
  Absence of enforcement is not permission, and the notices show the
  counsel exists and acts.
- **Germany.** The §95a UrhG reasoning from `deezer.md` (OLG Hamburg,
  2024-11-21, 5 U 54/23: even a weak measure counts) applies to a
  distributor in Germany. Here the key comes from the service to the
  paying user, which is a weaker case for "circumvention" than Deezer's,
  but not zero. This is an inference, not legal advice.

Verdict: about a week of work plus tester cycles, medium exposure, for a
US-only audience (three votes across #997 and #1103 so far). Not worth
building in-house before a US tester commits; acceptable as a
contributor sidecar along the lines already offered in #997.

## Route C: outside STR, available today

- **Music Assistant** has a stable SiriusXM provider and two ways to reach
  a SoundTouch: its DLNA player provider (the speaker is a UPnP renderer
  on `:8091`) and an alpha `bose_soundtouch` provider (2026-06) that maps
  the physical preset keys to Music Assistant content. Needs an always-on
  host (Raspberry Pi, NAS). Untested with STR on the same speaker: both
  want the preset keys, so an owner should use one or the other for keys.
- **AirPlay** from the SiriusXM app on iPhone, iPad or Mac, on speakers
  whose firmware lists `AIRPLAY` `READY` in `:8090/sources` (the probed
  Portable does; the probed ST10 lists it `UNAVAILABLE`).
- **Bluetooth** from the SiriusXM app, on every model.

Worth one FAQ line, nothing in the app.

## Re-check of the other services the firmware carried

The catalogue the firmware resolves against has 43 entries. The ones a
user could still ask for:

| Service | How the firmware did it | 2025-2026 change | Closure |
|---|---|---|---|
| Pandora, iHeartRadio | own clients, talk to the service directly | none | built (us-services.md) |
| Deezer | own client, Bose-issued token | new developer apps stopped 2025-10-19 | A1 built, A2 closed (deezer.md) |
| Amazon Music (#1104, #1270, #1335, #1493) | own client with an LWA refresh token; access tokens fetched through Bose | Web API still closed beta | **keep closed**, rationale sharpened |
| TuneIn (#751) | BMX service | none; public OPML still answers | closure fine, its rationale is outdated |
| Apple Music (#1354, #1646) | never on SoundTouch (AirPlay only) | none | **keep closed** |
| YouTube / YT Music (#1102, #996) | never on SoundTouch | YouTube harder to resolve | **keep closed** |
| Radioplayer (UK), Radio.com | BMX services with third-party adapters | Radioplayer's adapter host `boserp.radioapi.io` did not answer (2026-11-23) | not asked; stations are in radio-browser |
| Google Play Music, Rdio, DAR.fm, Juke, QQ Music, Saavn, 7digital | dead services or China-only | none | not asked |
| Napster / Rhapsody | not in the catalogue at all | none | not asked |

### Amazon Music: keep closed

The closing comments say "Amazon encrypts its stream with DRM ... no open
client" and, from #1335 on, "the old Amazon connection ran through the
Bose cloud". Both are right, and AfterTouch's attempt (their issue #188,
2026-04/05) makes the second one concrete: the speaker keeps an
`AmazonSecret` refresh token in `Sources.xml`, but asks Bose's OAuth host
(`streamingoauth.bose.com/oauth/device/<id>/music/musicprovider/20/token/...`)
for every access token, because Bose held the client secret. An own
Login-with-Amazon profile completes the OAuth round trip but cannot play:
Amazon Music must enable the security profile, and the Web API is "in
closed Beta status and access is limited to already approved developers"
(checked 2026-11-23). So a Deezer-A1-style "keep the login" does not help
either: the kept refresh token is useless without Bose's secret. No
closure is wrong; `docs/NOT-POSSIBLE.md` could add the token sentence.

### TuneIn (#751): closed for the right reason, with an outdated line

#751 was about a station missing from STR's search, closed for tidiness.
The line "STR does not use TuneIn" is no longer true: since #500 the agent
resolves TuneIn station ids through TuneIn's public OPML API
(`opml.radiotime.com`, `bmxtunein.go`) for speakers that list a `TUNEIN`
source, and `docs/FEATURES.md` says so. The same API also searches:
`Search.ashx?query=...&render=json&types=station` answered on 2026-11-23
without a key, and `Tune.ashx?id=<guide id>` returned a direct MP3 URL. So
a "TuneIn" fallback in the station search (only when radio-browser has no
hit, no new UI) would have answered #751's actual request. A search for
"Cool FM Belfast" returned stations, but the first hit was another Cool
FM; whether TuneIn carries the Northern Irish stream was not checked
further. Exposure: low to medium (TuneIn's partner API is closed; the OPML
API is unofficial but STR already depends on it).

### Apple Music: keep closed

Unchanged in 2025-2026: MusicKit plays only inside Apple's own players
with Apple's DRM and needs a paid developer token; there is no server-side
playback. The answers in #1354 and #1646 (AirPlay where the speaker has it,
Bluetooth everywhere, own files through DLNA) stand.

### YouTube / YouTube Music: keep closed

The rationale (no stable audio URLs, resolver tied to the requesting IP,
yt-dlp breaks on bot checks) has only grown stronger: through 2025 YouTube
added proof-of-origin tokens and moved clients to SABR streaming, and
yt-dlp now needs an external JavaScript runtime for full YouTube support.
The "bring your own stream server, paste its URL" answer from #996 stands.

### AirPlay per model, checked 2026-11-23

Read from `:8090/sources` on the maintainer's speakers, all on firmware 27.0.6:

| Model | `AIRPLAY` source |
|---|---|
| SoundTouch Portable (two units) | `READY` |
| SoundTouch 30 (scm chassis) | `READY` |
| SoundTouch 10 (two units) | `UNAVAILABLE` |

The firmware registers AirPlay only where the speaker's AirPlay authentication chip answers, so a SoundTouch 10 has Bluetooth as its only phone route. Any answer that points users to AirPlay has to name that.

### TuneIn search, checked 2026-11-23

`https://opml.radiotime.com/Search.ashx?query=<name>&types=station&render=json` answered 200 without a key and returned station items. The ranking is loose (a search for one well-known French station did not put it first), so as a fallback behind radio-browser it would need STR's own ranking by exact name match.

## Summary for planning

| Service | Verdict | User-facing outcome | Effort | Legal exposure |
|---|---|---|---|---|
| SiriusXM, Pandora-style (firmware source) | **close as not possible** | answer in #997: the speaker's SiriusXM went through Bose's adapter | 0.1 day (the post) | none |
| SiriusXM, STR client (route B, optionally behind the BMX adapter) | **build if** a US subscriber commits to short test cycles and the maintainer accepts the terms exposure; preferably as a contributor sidecar | a "SiriusXM" source in Find, channels on preset keys | 6 to 8 days in-house (+1 to 2 for the BMX adapter shape) | medium |
| SiriusXM, route C | **document** | FAQ line: Music Assistant, AirPlay, Bluetooth | 0.2 day | none |
| Amazon Music | **keep closed** | unchanged; optional sentence in NOT-POSSIBLE.md about the Bose token exchange | 0.1 day | n/a |
| TuneIn | **keep closed, build the fallback search if wanted** | station search also finds TuneIn-only stations | 1 to 1.5 days | low to medium |
| Apple Music | **keep closed** | unchanged | 0 | n/a |
| YouTube / YT Music | **keep closed** | unchanged | 0 | n/a |

Tests for a real speaker (replace `192.0.2.1` with the speaker's address,
`8888` with `17008` where the app shows that port):

1. **No SiriusXM today, no account** (any speaker, GET only):
   `curl -s http://192.0.2.1:8090/sources | grep -o 'SIRIUSXM[^>]*'` and
   `curl -s http://192.0.2.1:8090/serviceAvailability | grep -o 'SIRIUSXM[^/]*'`.
   Expect nothing on a speaker that never had SiriusXM. On a US speaker
   that had it, a bundle's `bose_sources_file` section shows whether the
   entry is `SIRIUSXM` or `SIRIUSXM_EVEREST` and whether a secret is
   present.
2. **Does the firmware hold a SiriusXM client of its own?** Maintainer
   runs, SSH on his own Portable, read-only:
   `grep -rli siriusxm /opt/Bose 2>/dev/null` then, for each file found,
   `grep -aoi 'siriusxm[A-Za-z_./-]*' <file> | sort -u | head -40`.
   Pass for the BMX reading: identifiers and BMX adapter names only, no
   `siriusxm.com` host. A SiriusXM host would reopen the Pandora route.
   Nothing is written; record only short identifiers.
3. **What the firmware asks a SiriusXM adapter for** (no account; a local
   test build; maintainer runs on the Portable, volume 5): add provider
   `38` to the catalogue, a `SIRIUSXM_EVEREST` entry to the BMX registry
   with `baseUrl` on the agent, and a `SIRIUSXM_EVEREST` source with a
   dummy credential to `/full`; log every request under the adapter path;
   restart the speaker. Then
   `curl -s http://192.0.2.1:8090/sources | grep -o 'SIRIUSXM_EVEREST[^>]*'`
   (expect `READY` or the reason it is not), and select a made-up channel:
   `curl -s -X POST -d '<ContentItem source="SIRIUSXM_EVEREST" type="stationurl" location="/v1/playback/station/test" sourceAccount="test"><itemName>test</itemName></ContentItem>' http://192.0.2.1:8090/select`.
   Read the agent's `/bmx/` log for the paths asked. If the adapter answers
   the playback path with a known radio stream in the TuneIn document
   shape, audio plays: that settles the route A shape without SiriusXM.
   Power-cycle recoverable; undo by booting the release build.
4. **The client works from a computer** (needs a US or Canadian
   subscriber; a US location if the API checks it): in a fresh virtual
   environment `pip install aiosxm==0.3.1`, start its bundled proxy with
   the tester's credentials, then
   `ffprobe http://127.0.0.1:<port>/stream/channel/<id>/playlist.m3u8`.
   Pass: an AAC audio stream is reported. A refusal names the geo or
   entitlement problem before any STR code is written.
5. **The speaker plays decrypted SiriusXM audio** (needs the tester from
   test 4; maintainer runs only if the tester's proxy is reachable, else
   the tester runs it at volume 5): with the proxy from test 4 on the same
   LAN, paste `http://<computer>:<port>/stream/channel/<id>/playlist.m3u8`
   into STR's station search and play the "Play this stream URL" card. This
   fails today without key support in the HLS converter; after route B's
   decrypt lands, pass is audio and a now-playing title.
6. **Route C works next to STR** (needs a US subscriber): Music
   Assistant with the SiriusXM provider and the DLNA player provider,
   speaker at volume 5, play a channel to the speaker. Check that STR's
   preset keys still play their own stations afterwards.
7. **TuneIn fallback search** (no account; maintainer runs on the
   Portable, volume 5):
   `curl -s "https://opml.radiotime.com/Search.ashx?query=<station>&render=json&types=station"`,
   then `curl -s "https://opml.radiotime.com/Tune.ashx?id=<guide id>&render=json"`,
   paste the returned stream URL into the station search and play it.
   Pass: audio through the stream proxy without any `TUNEIN` source on
   the speaker.
8. **Amazon, for the record** (needs an owner whose stock speaker still
   had Amazon): a bundle's `bose_sources_file` shows an `AMAZON` entry with
   a secret, and `:8090/sources` shows `AMAZON` not `READY` after STR
   install. No build follows from either result.

Sources: AfterTouch (gesellix/Bose-SoundTouch) `bmx_services.json`,
`constants.go`, `handlers_bmx_siriusxm.go`, `docs/analysis/UPSTREAM-URLS`,
issue #188; deborahgu/soundcork README; MizterB/aiosxm README (PyPI 0.3.1);
music-assistant/server providers `siriusxm`, `dlna`, `bose_soundtouch`;
SiriusXM web terms of use; Amazon Music Web API overview
(developer.amazon.com/docs/music/API_web_overview.html); TuneIn OPML
(`opml.radiotime.com`), queried 2026-11-23.

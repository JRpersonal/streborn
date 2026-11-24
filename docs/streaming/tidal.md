# Tidal

Status: **not built; the speaker has no Tidal client, Tidal Connect is
closed to STR, and the one working route (an unofficial client in STR)
waits on a yes from TIDAL that has not come.** Vote thread: idea #1105
(7 votes); closed duplicates #1413 and #1770.

Researched 2026-11-17 (spike, ToS) and 2026-11-23 (this report) from the
code in this repository, read-only probes of two STR speakers (a Portable,
taigan, and an ST10, rhino, both 27.0.6), the measurement spike in draft
PR #1953 (`cmd/tidalspike`, branch `spike/tidal`, never to be merged), and
public sources. The spike ran once against a Tidal Premium trial; nothing
else here played audio.

There are four ways Tidal could reach a SoundTouch speaker:

| Route | What plays the music | Feasibility | Legal exposure |
|---|---|---|---|
| A | the speaker firmware's own Tidal client | none: the firmware has no Tidal source | none |
| B | an unofficial Tidal client in STR, handing the stream to the speaker over UPnP | measured: works (AAC 320, plain http) | medium to high: unauthorised client, against Tidal's terms |
| C | a phone or computer casting to the speaker (AirPlay, Bluetooth, DLNA apps) | AirPlay and Bluetooth: yes; DLNA apps: open, one doubt (https) | none for STR |
| D | Tidal Connect, so the Tidal app sees the speaker | closed: needs a Tidal device certificate | not available to STR |

Recommendation: route C, with one test that decides what FAQ q49 may
promise. Route B stays parked until TIDAL answers the question already
put to it (below); route D is closed for good, route A does not exist.

## What users ask for

| Where | Ask |
|---|---|
| #1105 (maintainer's vote thread, 2026-10-03) | Tidal in general; 7 votes, no comments |
| #1413 (closed duplicate, 2026-10-22) | "Please integrate TIDAL Connect into the SoundTouch platform." |
| #1770 (closed duplicate, 2026-11-07, German) | connect the Tidal app to the SoundTouch over Wi-Fi |
| #678 (General, 2026-08-23) | "+1 for hopefully later Tidal support", left Spotify for Tidal |
| #385 (General) | names "TIDAL and Amazon Music Connect" from the old roadmap; the vote there is for Amazon |
| support mail (2026-11) | Tidal on the speaker; answered with FAQ q49 |

Two of the three explicit asks are for **Tidal Connect**: pick the speaker
in the Tidal app. Nobody asked for hi-res, and nobody asked for preset keys,
although keys are what makes STR different from casting. No Tidal thread
names a model or a quality. Demand is modest next to Deezer (#1239, 36
votes) and does not grow quickly (7 votes in seven weeks).

## What the firmware does with Tidal

Nothing that can be seen. Read-only GETs on 2026-11-23:

1. **No Tidal source.** `:8090/sources` on the Portable and on the ST10
   lists AUX, STORED_MUSIC, AIRPLAY, NOTIFICATION, DEEZER, QPLAY, SPOTIFY,
   UPNP, STORED_MUSIC_MEDIA_RENDERER, ALEXA, LOCAL_INTERNET_RADIO (ST10:
   also BLUETOOTH). No TIDAL entry, available or not. Contrast Deezer,
   Pandora and iHeart, whose firmware clients show up as source slots or
   register themselves (`deezer.md`, `us-services.md`).
2. **No Tidal service.** `:8090/serviceAvailability` (which echoes what
   STR's stand-in answered, `DefaultServices` in
   `internal/marge/templates.go`) names PANDORA, AIRPLAY, ALEXA, AMAZON,
   BLUETOOTH, BMX, DEEZER, LOCAL_INTERNET_RADIO, LOCAL_MUSIC, NOTIFICATION,
   QPLAY, SPOTIFY, STORED_MUSIC_MEDIA_RENDERER, TUNEIN, UPNP. That list was
   modelled on a real ST10's answer, which did not name TIDAL either.
3. **No Tidal endpoint.** `:8090/supportedURLs` is identical on both
   speakers (103 entries). It has the generic account calls
   (`/setMusicServiceAccount`, `/setMusicServiceOAuthAccount`,
   `/removeMusicServiceAccount`) and nothing service-specific.
4. **Only a catalogue row.** The cloud's source-provider catalogue, which
   STR serves (`respondSourceProviders`, `numericProviderID` in
   `internal/marge/responses.go`), has `24 TIDAL`. AfterTouch
   (gesellix/Bose-SoundTouch, `pkg/service/constants`) records that row as
   created on 2016-04-08, the same day as WBMX and SOUNDCLOUD: Bose reserved
   the id in its cloud, but no SoundTouch firmware ever shipped a Tidal
   client. Bose's service lists for SoundTouch (Spotify, Pandora, Deezer,
   iHeartRadio, SiriusXM, Amazon Music, TuneIn) never included Tidal, and
   Bose's SoundTouch Web API v1.1 (April 2026) does not mention it.
   AfterTouch uses `TIDAL` in its examples as "a custom/unknown service".
5. **STR's own references are defensive.** `internal/boxsnapshot` flags a
   TIDAL preset or source as a lost cloud service, and
   `internal/webui/restore_test.go` uses TIDAL as an example of an unknown
   source. Neither implies a firmware client.

*Established for the source list and endpoints; "no client in the
binaries" is a strong inference.* Test 1 below settles it with a string
search on the maintainer's own speaker; Error 1028 ("Unknown source")
is what BoseApp answers for a source that never registered, which is
what a `/select` with `source="TIDAL"` should get.

## Route A: the speaker's own Tidal client

Does not exist (above). Unlike Deezer, there is no login to keep and no
client to hand a token to. Closed, unless test 1 finds Tidal strings in
the firmware's music-service binaries.

## Route B: an unofficial client in STR

The Spotify shape without a sidecar: STR's agent talks to Tidal's private
v1 API with the owner's account and pushes the stream URL to the speaker
over UPnP AVTransport on `:8091`.

### What the spike measured (2026-11-17, Portable, trial account, DE)

- **Login.** OAuth device-code login ("enter this code at link.tidal.com")
  with the client id python-tidal uses for limited-input devices works,
  and the refresh token keeps working (it is not rotated).
- **Subscription gate.** A free account gets 401 (subStatus 5003) on every
  quality and 4005 on previews. Premium (any paid tier) is required.
- **Stream.** With Premium: a BTS manifest, unencrypted AAC in MP4
  (LOW `mp4a.40.5`, HIGH `mp4a.40.2` at about 320 kbit/s), a plain signed
  CDN URL valid 60 minutes, Range requests answered. No decryption at all,
  which is the decisive difference to Deezer.
- **Lossless.** LOSSLESS and HI_RES requests were downgraded to HIGH for
  this client id. python-tidal reports since 2026 that the device-code
  client is capped at AAC 320 and lossless needs the PKCE client (#404
  there). Open, but not on the critical path: the speakers' own DACs and
  drivers gain little from lossless, and no user asked for it.
- **Speaker.** The SoundTouch refuses an `https` URI (UPnP 402, "No URI
  supplied"). The same signed URL over plain `http` is served by the CDN
  and played on the Portable (PLAY_STATE, position moving, volume 5).
  STR's UPnP path already tries the `http` variant of an `https` stream
  (`ResolveStreamURL` in `internal/upnp/upnp.go`).

### What it would look like in STR

- **Agent.** A small Tidal client inside the agent (Go, a few hundred KB,
  no sidecar; the 31 MB NAND is not the constraint). Device-code login
  shown in the apps, tokens in `/mnt/nv/streborn/tidal-login.json` (0600,
  never logged, masked in bundles, deleted by Remove STR).
- **Stable local URL.** The speaker gets `http://127.0.0.1:8888/tidal/<track>`,
  not the CDN URL. On every GET the agent asks Tidal for a fresh signed URL
  and streams the bytes through, the `internal/streamproxy` pattern. That
  removes the 60-minute expiry (a long pause, a preset pressed days later)
  and the https problem in one place.
- **Queues and keys.** Albums and playlists play through the existing
  `queue` preset type (one push per track, track ends timed from the
  duration Tidal returns; `SetNextAVTransportURI` is refused by 27.0.6, see
  `internal/webui/queueprearm.go`). A held key stores the playlist id, not
  URLs, so it plays after any reboot.
- **Browse.** Search, the account's playlists, favourites and mixes in the
  Find tab and on the phone page.
- **Effort.** About 6 to 8 days, starting from the spike's auth and manifest
  code (rewritten, not merged). Ongoing: Tidal rotated the keys third-party
  clients use on 2025-10-15 (python-tidal #371), which broke them until
  new ones were extracted. Expect that again, with every STR user's Tidal
  source dead until a release ships.
- **Not delivered.** The Tidal app does not see the speaker. It is "Tidal
  inside STR", not the Tidal Connect the duplicates asked for.

### Terms and law

- **TIDAL's terms.** TIDAL staff call python-tidal-style clients
  "unauthorized usage". The Content Guidelines forbid circumventing security
  technology, reverse engineering and automated access. Developer Terms
  v3.0 allow playback only through the unmodified TIDAL Player SDKs (web,
  iOS, Android); alarms, voice control and mixing with other services need
  written approval. STR would use a client id lifted from TIDAL's own apps,
  and ship it to every user. The owner's account is at stake, not only the
  project.
- **German law.** No stream is decrypted, so the Deezer argument (§95a(2)
  UrhG, per-track encryption) does not apply directly. A client id and
  secret taken from TIDAL's apps could still be argued to be an access
  control; the OLG Hamburg (21 Nov 2024, 5 U 54/23, youtube-dl) treated even
  a weak rolling cipher as an effective measure and §95a(3) as covering the
  distribution of a tool. Weaker case than Deezer, not nil. No takedown of a
  playback-only Tidal project was found (only TiDown, 2016, a downloader).
  An inference, not legal advice.
- **Letting the user bring the client id** (as the spike does, via an
  environment variable) shifts the act to the owner but makes setup
  unusable for the people who ask for Tidal. Not a fix.

### Has TIDAL been asked?

Yes. The maintainer asked in TIDAL's developer forum on 2026-11-18
(tidal-music discussions #454, "Approved way to play TIDAL on a UPnP
speaker without Connect? (open-source project for Bose SoundTouch)"),
after TIDAL Support referred the question there. As of 2026-11-23 it has
no reply. The pattern there:

- Over twenty requests for full-length playback since early November
  (#440 to #459: Linux, macOS, Android, in-car, DJ tools, a personal
  client) are all unanswered.
- MuPiBox, an open-source Raspberry Pi player with a larger following,
  asked the same on 2026-10-15 (#417) and followed up on 2026-11-07
  ("a no would be acceptable, silence is not"); still no answer.
- The last staff answers on the subject (2023, #27): no open-source or
  Linux SDK planned, and Tidal Connect "only available for our so-called
  device partners".

So route B is "build if TIDAL says yes", and a yes is unlikely.

## Route C: casting from a phone or computer

Every SoundTouch is a UPnP MediaRenderer on `:8091` (sinks include
`audio/flac`, `audio/mp4`, `audio/aac`, `audio/x-m4a`, LPCM), but the
Tidal app itself has no DLNA output (Android: Chromecast and Tidal
Connect; iOS: AirPlay). What works without STR building anything:

- **AirPlay** from the Tidal app on iPhone, iPad or Mac, on speakers whose
  firmware offers it. The firmware registers AIRPLAY only on allowlisted
  products whose Apple MFi chip answers; `:8090/sources` shows it as
  `AIRPLAY` `READY`. On the two probed speakers: Portable `READY`, ST10
  `UNAVAILABLE`. Expected to work (AirPlay is source-agnostic); not yet
  played with Tidal (test 3).
- **Bluetooth**, on every model. Works with any app.
- **DLNA control points with a Tidal login** (BubbleUPnP on Android,
  mConnect Player on iOS and Android), as FAQ q49 already says. Both log
  in to Tidal (BubbleUPnP has had OAuth login since TIDAL's 2021 change;
  an outage in October 2025 was temporary) and hand the renderer a URL.
  **The doubt:** if they hand over Tidal's `https` CDN URL, the speaker
  answers 402 and nothing plays, exactly as in the spike. Whether they
  proxy the stream, rewrite it to `http`, or fail is unknown. FAQ q49
  currently promises this route untested; test 2 decides it.
- **Android audio capture into UPnP** (BubbleUPnP "Audio Cast" and similar,
  Android 10 and later) works only if the Tidal app allows playback
  capture. Untested; Bluetooth covers the same case more simply.

None of these gives a hardware key or works with the phone away. That is
the honest gap route C leaves, and the only thing route B would add.

## Route D: Tidal Connect

Closed. The Tidal app only pairs with a receiver that presents a TIDAL
Connect device certificate and private key, issued to device partners.

- resurekto/tidal-connect-ng (0BSD, C++) reimplements the receiver but
  ships no certificate; users take one from iFi Zen Stream builds. No
  commits since 2026-10-16, no stars.
- An existing SoundTouch bridge does this today: paillat/tidal-soundtouch
  (GitLab, MIT, created 2025-11-16, last activity 2025-11-17). Its
  `run.sh` downloads iFi's proprietary Tidal Connect binary and its
  certificate from TonyTromp/tidal-connect-docker, runs one ARMv7 Docker
  container per speaker on a separate Raspberry Pi, re-encodes to MP3 via
  Icecast, and uses Home Assistant automations to point the SoundTouch at
  that stream. It shows the demand is real, and that the only way to Tidal
  Connect is a redistributed partner certificate. STR cannot ship that,
  and should not point users at it.

## Tests that settle the open points

See the numbered list in the summary below. Only test 2 changes a
user-facing text right away (FAQ q49); tests 1 and 3 close the record;
test 4 is needed only if TIDAL says yes.

### AirPlay per model, checked 2026-11-23

Read from `:8090/sources` on the maintainer's speakers, all on firmware 27.0.6:

| Model | `AIRPLAY` source |
|---|---|
| SoundTouch Portable (two units) | `READY` |
| SoundTouch 30 (scm chassis) | `READY` |
| SoundTouch 10 (two units) | `UNAVAILABLE` |

The firmware registers AirPlay only where the speaker's AirPlay authentication chip answers, so a SoundTouch 10 has Bluetooth as its only phone route. Any answer that points users to AirPlay has to name that.

## Summary for planning

**Verdict: close as covered by route C (AirPlay, Bluetooth, and DLNA apps
if test 2 passes); build route B only if TIDAL answers #454 with written
approval.** Close #1105 with a reply that names the casting routes, links
FAQ q49, and says the thread reopens if TIDAL approves. Tidal Connect
(what #1413 and #1770 asked for) is impossible for STR.

- **User-facing outcome now:** Tidal from the phone: AirPlay from the Tidal
  app where the speaker lists AirPlay, Bluetooth everywhere, and (after
  test 2) BubbleUPnP or mConnect to the speaker over the network. No preset
  keys for Tidal.
- **User-facing outcome with route B:** Tidal search, playlists and
  favourites in the STR apps, AAC 320, Tidal playlists on preset keys 1 to
  6, no Tidal app control. Paid Tidal tier required.
- **Effort:** route C: a FAQ edit and an evening of testing. Route B: 6 to 8
  days, plus an unplanned release each time TIDAL rotates its client keys.
- **Legal/ToS exposure:** route C none. Route B: against TIDAL's Developer
  Terms and Content Guidelines, owner accounts at risk of suspension, a
  client id taken from TIDAL's apps distributed with every build; German
  §95a(3) risk weaker than Deezer's (no decryption) but not nil. Route D:
  would mean redistributing a partner certificate; out.

| Step | Effort | UI | Needs |
|---|---|---|---|
| Firmware check (test 1) | 15 min | none | SSH on own speaker |
| DLNA cast test (test 2), then fix FAQ q49 | one evening | website FAQ only | Tidal trial, Android phone, Portable |
| AirPlay check (test 3) | 10 min | none | Tidal trial, iPhone, Portable |
| Close #1105 with the casting answer | 15 min | none | tests 2 and 3 |
| Route B | 6 to 8 days | Tidal in Find, a login card, Tidal keys | written yes from TIDAL |
| Route D | none | none | impossible |

### Tests

1. **Is there a Tidal client in the firmware at all?** (no audio; own
   speaker only, SSH already enabled; read-only)

   ```
   ssh root@192.0.2.1 'grep -l -i tidal /opt/Bose/* /opt/Bose/lib/* 2>/dev/null; echo exit=$?'
   ssh root@192.0.2.1 'grep -l -i deezer /opt/Bose/* /opt/Bose/lib/* 2>/dev/null'
   ```

   The Deezer line is the control: it must list the binary that holds the
   Deezer client (STSCertified or BoseApp). If the Tidal line lists nothing,
   route A is closed for good. If it lists a binary, read the matching
   strings locally (`grep -a -o -i '.\{0,40\}tidal.\{0,40\}' <file>` on a
   copy outside the repo) and note only short identifiers; nothing from the
   binary goes into the repo.

   Optional, maintainer runs, on an idle Portable (nothing playing):

   ```
   curl -s -X POST -d '<ContentItem source="TIDAL" type="tracklisturl" location="1" sourceAccount=""></ContentItem>' http://192.0.2.1:8090/select
   ```

   Expected: an error with code 1028 ("Unknown source"). Anything else is a
   lead worth a log read.

2. **Does a DLNA app play Tidal on a SoundTouch?** (maintainer runs;
   Portable, volume 5; Tidal trial; Android phone with BubbleUPnP, then
   mConnect Player)

   ```
   curl -s -X POST -d '<volume>5</volume>' http://192.0.2.1:8090/volume
   ```

   In BubbleUPnP: Library, TIDAL, sign in with the trial; Renderer: the
   Portable; play any album track. While it plays (or right after it
   fails), read what the speaker was given:

   ```
   curl -s -H 'Content-Type: text/xml; charset="utf-8"' \
     -H 'SOAPACTION: "urn:schemas-upnp-org:service:AVTransport:1#GetMediaInfo"' \
     -d '<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:GetMediaInfo xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID></u:GetMediaInfo></s:Body></s:Envelope>' \
     http://192.0.2.1:8091/AVTransport/Control
   curl -s http://192.0.2.1:8090/nowPlaying
   ```

   Read only the scheme and host of `CurrentURI` (`http` or `https`;
   the phone's own address, or a Tidal CDN host). Do not paste the signed
   URL anywhere. Repeat with mConnect Player. If either fails, look in its
   settings for a proxy or "stream through phone" option and try once more.

   - **Plays** (with either app, any setting): FAQ q49 keeps the route,
     drops "I have not tested this", and names the app and setting that
     worked.
   - **Fails with both** (the app reports a renderer error, `nowPlaying`
     never shows `PLAY_STATE`, `CurrentURI` starts with `https`): FAQ q49
     loses the DLNA paragraph and says AirPlay or Bluetooth instead. This is
     also the only case where a small STR helper could be argued for (the
     agent as an `https`-to-`http` relay renderer), but that serves every
     https source, not Tidal; a separate decision.

3. **AirPlay from the Tidal app** (maintainer runs; Portable, volume 5;
   iPhone with the Tidal trial): set volume 5 as in test 2, pick the
   Portable in the Tidal app's AirPlay picker, play a track, then
   `curl -s http://192.0.2.1:8090/nowPlaying`. Pass: `source="AIRPLAY"`
   and `PLAY_STATE`. Decides whether the #1105 closing reply and FAQ q49
   may name AirPlay as tested. Note in the reply that AirPlay is available
   only where `:8090/sources` lists `AIRPLAY` `READY` (the probed ST10 does
   not).

4. **Only if TIDAL approves route B: do the spike's findings still hold?**
   (maintainer runs; Portable, volume 5; Tidal trial; spike built from
   branch `spike/tidal`, client id and secret via environment only)

   ```
   tidalspike refresh && tidalspike whoami
   tidalspike track <trackid> --quality all --probe
   TIDALSPIKE_TEST_SPEAKER=192.0.2.1 tidalspike play <trackid> --speaker 192.0.2.1 --quality HIGH
   ```

   Pass: `whoami` shows a paid subscription, HIGH answers a BTS manifest
   with `mp4a.40.2` and no encryption, `play` reaches `PLAY_STATE` with
   the position moving. A 401 on refresh or playbackinfo with a valid
   subscription means the keys were rotated again: count it as a
   maintenance cost before building. If TIDAL's approval comes with its own
   client id, rerun the same three commands with it and add
   `--quality LOSSLESS` to see whether FLAC is offered.

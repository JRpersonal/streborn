# Deezer

Status: **route A1 built, not yet confirmed on a speaker.** A speaker
that is logged into Deezer when STR is installed keeps that login (A1
below). A new login is still open (A2). Vote thread: idea #1239.

Researched 2026-11-21 from the code in this repository, read-only probes
of five STR speakers, a neighbour project's field reports, and public
sources. No Deezer account was available, so nothing here was played.

There are three ways Deezer could reach a SoundTouch speaker:

| Route | What plays the music | Feasibility | Legal exposure |
|---|---|---|---|
| A | the speaker firmware's own Deezer client | keeping a login: likely; a new login: open | low: the owner's own account on the speaker's own client |
| B | an unofficial Deezer client in STR | technically easy, small | high: every full track has to be decrypted |
| C | a phone or computer casting to the speaker | depends on the phone app, not on STR | none for STR |

Recommendation: route A, in two steps, starting with a measurement
that costs no user anything. Route B stays ruled out (as answered in
#1239 on 2026-10-30). Route C is worth a FAQ line, nothing more.

## What the firmware does with Deezer

Firmware 27.0.6 carries a Deezer client (source `DEEZER`, provider id
`14` in the source-provider catalogue, which STR already serves). What
is known, and how firmly:

1. **The login lives on the speaker, the playback goes to Deezer.**
   A Deezer preset points at Deezer itself (`location` is a Deezer id
   such as a playlist number, or `https://api.deezer.com/user/me/flow`
   for Flow), never at a Bose host. Stock speakers still play Deezer
   six months after the Bose shutdown (#1239: a stock ST10 with an
   active subscription; a second stock ST10 playing Flow). So neither
   the playback nor the token refresh needs the Bose cloud.
   *Established.*

2. **The login is an account token, and it is the cloud document's
   `<credential>`.** In Bose's design the account document
   (`GET /streaming/account/<id>/full`) carries each linked service as
   a `<source>` with a `<credential type="...">` element, and the
   speaker keeps it in `/mnt/nv/BoseApp-Persistence/1/Sources.xml`
   (an entry with `secret` and `secretType` attributes; documented by
   the AfterTouch project for Spotify). `https://api.deezer.com/user/me/flow`
   answers `OAuthException: An active access token must be used` without
   a token, so the credential is most likely a Deezer OAuth access token
   issued to Bose's Deezer application (long-lived, `offline_access`).
   Not an ARL cookie, which older notes in this repo assumed.
   *Strong inference; the credential type has never been seen.*

3. **STR is what turns DEEZER `UNAVAILABLE`.** The firmware rebuilds
   its source list from `/full` on every boot and drops what the
   document does not name. Since v0.8.5 STR names a DEEZER source the
   speaker had before STR (the reflect file seeded by
   `internal/boxsnapshot`, see "Path A" in `internal/marge/responses.go`),
   but with an empty `<credential type="token"></credential>`, because
   `/sources` never exposes the token. Result: the entry survives, the
   status is `UNAVAILABLE`, and a key press answers 1036 "not logged
   in" (seen on a reporter's ST10, 2026-08-25). Reproduced on this
   fleet: one Portable carries a reflected test entry, `UNAVAILABLE`.
   *Established.*

4. **Handing the original credential back keeps Deezer working.**
   AfterTouch (gesellix/Bose-SoundTouch, MIT, Go), a cloud replacement
   that migrates a speaker by copying its own `Sources.xml` (token
   included) into its stand-in, reports an ST20 (sm2, 27.0.6) after
   migration with `DEEZER` `READY` and Deezer presets playing
   (gesellix/Bose-SoundTouch#521, 2026-06). Its users also report that
   migrated Deezer presets work and that new albums, tracks and
   playlists play when sent to `/select` by their Deezer id (#574).
   *Established by a third party, not on an STR speaker.*

5. **A new login without Bose is the open part.**
   - Bose's own help page (HC1296) describes linking Deezer as a login
     "through the default browser" from the SoundTouch app, which reads
     like a Deezer OAuth login under Bose's Deezer application. For
     Spotify that exchange ran through Bose's OAuth service
     (`oauth.streaming.bose.com`, documented by AfterTouch), which is
     gone. If Deezer's ran there too, the firmware cannot mint a new
     token on its own.
   - Deezer accepts no new developer applications: it stopped creating
     new API access on 2025-10-19, citing abuse
     (https://en.deezercommunity.com/features-feedback-44/api-auth-impossible-80857),
     and the AfterTouch maintainer hit the same block on 2026-08-09.
     Nobody can get an own OAuth app id, and the public API gives such
     apps only 30-second previews anyway.
   - The firmware still lists `/setMusicServiceAccount`,
     `/setMusicServiceOAuthAccount` and `/removeMusicServiceAccount`
     in `/supportedURLs` on all five probed speakers (ST10 sm2, ST30
     scm, Portable scm, all 27.0.6).
   - AfterTouch sent `<credentials source="DEEZER"
     displayName="Deezer Premium"><user>..</user><pass>..</pass></credentials>`
     to `setMusicServiceAccount` on a real speaker: "the speaker's own
     account-creation handshake starts but never completes, and doesn't
     clean up afterward either", root cause unknown (#574). Whether the
     firmware's Deezer client then talks to Deezer, to a Bose host that
     is gone, or to the stand-in is exactly what nobody has logged.
   - Pandora is the template that works: there the firmware logs in
     itself and registers the source with the stand-in
     (`POST /streaming/account/<id>/source`), which STR stores and
     hands back (`docs/streaming/us-services.md`). If the Deezer client
     behaves the same, STR's side is a table entry.
   *Open.*

6. **Choosing content needs no login on STR's side.** Deezer's public
   API (`https://api.deezer.com/search/playlist?q=...`, albums, tracks)
   answers without a token and returns the numeric ids a `DEEZER`
   ContentItem uses as `location`. The account's own playlists and
   Favourites (`type="user"`, `location` = the account number, from the
   #1239 export) are what the speaker would have to list itself, via
   `/navigate` as for Pandora; untested for Deezer. *Search: verified
   2026-11-21. Listing via the speaker: open.*

### What the spike found in STR, and what changed

- The request log (`/__spy/log`) masked `<credential>` values only for
  Pandora and iHeart, so a Deezer token would have been recorded in
  clear. Now every `<credential>`, every `<pass>` and the
  `Authorization` header are masked, whatever the service.
- A Deezer registration from the firmware was filed as `STORED_MUSIC`
  and its credential dropped. It is now answered as the Deezer source
  it is, and its credential is kept (A1).
- `internal/boxsnapshot` reads `/sources` only, so the reflect file
  never holds a credential. Its comments say "ARL"; see point 2.
- `/streaming/account/<id>/provider_settings` is answered with an empty
  `<providerSettings/>`. AfterTouch's logs show the firmware asking for
  provider 14's settings; whether Deezer needs a value there is unknown.

## Route A: the speaker's own Deezer client

### A1. Keep the login a speaker already has (built)

The credential is on the speaker until STR's stand-in answers the
first account sync without it. STR's agent already runs on the speaker
and already reads firmware files in `/mnt/nv/BoseApp-Persistence/`
(NetworkProfiles.xml, AirplayConfiguration.xml). What it does now
(`internal/marge/deezerlogin.go`):

1. **Capture.** At every agent start, before the stand-in listens, the
   agent reads `Sources.xml` once (`internal/boxsources`). The stand-in
   is what answers the firmware's account sync, so on the first start
   after an install this read comes before STR's first answer. If the
   file holds a `DEEZER` entry with a login, its account, display name,
   `secret` and `secretType` go to `/mnt/nv/streborn/deezer-login.json`
   (mode 0600). An unchanged login writes nothing. A file without a
   Deezer login leaves an earlier kept login alone, because an older STR
   may have made the firmware rewrite its file without the token.
2. **Serve back.** `/full` names the Deezer source with that login as
   its `<credential>`. This happens whether or not the reflect file lists
   Deezer: the snapshot that seeds the reflect file reads `/sources`,
   which an older STR had already emptied. The account source list
   (read right after a registration) names it `READY`. The login is
   revealed only to a loopback caller, the speaker itself through
   `/etc/hosts`; any LAN client gets `***`.
3. **Registration.** A Deezer `addSource` from the firmware is answered
   as Deezer (provider 14), and a credential in it replaces the kept
   login. So a login the speaker makes itself is the one handed back
   from then on, should A2 ever work.
4. **Untouched without Deezer.** No file, and every account document is
   byte for byte what it was. A test holds the documents with and
   without the feature side by side.
5. **Never leaves the speaker.** No log line, debug section or bundle
   carries the login or the account. The debug section `deezer_login`
   says only whether a login is kept, its type, where it came from and
   when. `Remove STR` deletes it with the rest of `/mnt/nv/streborn/`.
   A Bose factory reset does not (see `docs/THREAT-MODEL.md`).

What this gives, once confirmed: an owner who installs STR on a speaker
that plays Deezer today keeps Deezer, with the hardware keys and new
content by id. A speaker that already runs STR is helped only if its
firmware still holds the token. The `bose_sources_file` debug section
shows whether it does (see Tests).

The same capture would carry a Pandora or iHeart login over with its
credential, instead of the name-only carry-over in us-services.md.

Open details:

- whether the firmware rewrites `Sources.xml` without the token after
  STR's credential-less `/full` (decides whether already-converted
  speakers can be helped);
- whether the token is bound to the speaker or to the Bose account
  (in Bose's design one account document served every speaker of an
  account, so most likely the latter: one owner's token could then
  serve all of that owner's speakers);
- whether `provider_settings` needs an entry for provider 14.

UI: none. The speaker simply keeps playing Deezer. Not done: one line
in the existing "lost services" notice when a Deezer entry had no token
to carry.

### A2. A new login on the speaker

Only if the firmware's own Deezer client can still log in without Bose.
The odds are poor (point 5: the Bose app linked Deezer through a
browser login, and AfterTouch's password attempt hung), but the test
is cheap and settles it (below). If the firmware registers the
source with the stand-in the way Pandora does, the build is the
Pandora one:

- `DEEZER` in `nativeServices` (`internal/marge/usservices.go`),
  provider id 14, availability on by default (Deezer is not
  geo-restricted like Pandora);
- `POST/DELETE /api/deezer/account` calling the speaker's own
  `setMusicServiceAccount` / `removeMusicServiceAccount`;
- a Deezer card next to the Pandora one in speaker settings (e-mail,
  password, Connect, Disconnect);
- content: paste a Deezer link (playlist, album, track) into the
  existing play-URL field and hold a key to keep it, which needs only
  the id from the link; a station list in the Find tab only if the
  speaker's `/navigate` lists the account's playlists.

Effort if the test passes: 1 to 2 days for the stand-in and the
agent endpoints, 2 to 3 days for the card and the link handling. UI:
one settings card plus accepting Deezer links where URLs are already
accepted. No new tab.

If the handshake hangs on STR as it did on AfterTouch, the request log
and syslog show where. If it waits on a Bose host that is gone, A2 is
closed: STR does not impersonate Bose's OAuth service or reuse Bose's
Deezer application credentials.

## Route B: an unofficial client in STR

The Spotify pattern: STR runs its own Deezer client, logged in with the
owner's account, and hands the audio to the speaker over UPnP.

Every candidate uses Deezer's private web API: an `arl` session cookie
copied from a browser, `gw-light.php` for the user and track tokens,
`media.deezer.com/v1/get_url` for the CDN URL, and then decryption of
the track (Blowfish CBC on every third 2048-byte block, the key derived
from the track id and a fixed secret taken from Deezer's own clients).
Only the 30-second previews are served unencrypted.

| Project | Language, licence | State (2026-11) | Login | Notes |
|---|---|---|---|---|
| OpenDeezer (Cycl0o0) | Go, AGPL-3.0 | created 2026-06-25, last commit on `main` 2026-07-15, 21 stars, one author | ARL only | decrypts; usable as a Go module (`sdk/deezer`); MP3_128 free, MP3_320 Premium, FLAC HiFi; no armv7 build, arm64 TUI 12 MB |
| deezer-py / deemix | Python, GPL-3.0 | dormant since 2022, reported broken 2025 | ARL | decrypts; DMCA target |
| pleezer (roderickvd) | Rust, "Sustainable Use License" (not OSI) | last push 2025-10 | e-mail/password or ARL | a Deezer Connect receiver; Connect reached end of life in May 2025; ALSA/JACK output only, cannot start a playlist by itself (studied 2026-06-17) |
| ReFreezer, streamrip | Dart, Python, GPL-3.0 | 2025 and 2026 | ARL | decrypt; Debian dropped streamrip's Deezer support on 2025-12-15 |
| 89z/deezer, joshbarrass/deezerdl | Go | 2021, 2019 | ARL | small Decrypt helpers, abandoned |

Fit on the speaker is not the problem. Blowfish is in
`golang.org/x/crypto`, and an MP3 or FLAC stream needs no transcode
(the speaker takes both over UPnP, as measured in the Tidal spike,
PR #1953), so the client
could live inside the agent for a few hundred kilobytes, without a
second sidecar next to the 16 MB Spotify engine. The 31 MB NAND would
hold it.

The problem is the decryption:

- Deezer's terms (Art. 6 of the German version) forbid circumventing
  its technical protection measures and any attempt at ripping;
  Art. 7 allows suspending the account. The owner's account is at
  stake, not only the project.
- Deezer enforces this against tools: GitHub DMCA notice of 2021-02-10
  against deezloader, deemix-gui and six others, alleging circumvention
  of "effective technological measures" with maliciously retrieved
  keys (github/dmca 2021-02-10-deezer.md), and earlier waves in 2017
  to 2019 that named deemix and freezer.
- In Germany, §95a(2) UrhG names encryption as a technical measure,
  and §95a(3) forbids producing and distributing tools primarily
  designed to circumvent one. The OLG Hamburg (21 Nov 2024, 5 U 54/23,
  youtube-dl) held that even an easily broken measure (YouTube's
  rolling cipher) counts. A per-track Blowfish key is a stronger case
  than that. No court has ruled on Deezer specifically; this is an
  inference, not legal advice.
- Unlike Tidal, where AAC comes unencrypted and the open question is a
  client id, Deezer has no full track without decryption at all.

Verdict: technically a few days, legally the worst of all streaming
sources STR has looked at. The answer given in #1239 on 2026-10-30
(not on OpenDeezer, not on any ARL client) stands.

## Route C: casting from a phone or computer

Every SoundTouch is a UPnP MediaRenderer on `:8091` (MP3, AAC, FLAC,
plain `http` only), but Deezer does not let anything use it:

- the Deezer app itself casts only to Chromecast (and Deezer Connect,
  now retired); it has no DLNA or UPnP output;
- BubbleUPnP does not support Deezer ("Deezer does not allow third
  party apps", its developer, 2022) and mConnect does not list it.

What does work without STR building anything, because the phone or
computer sends the sound itself:

- **Bluetooth**, on every SoundTouch model;
- **AirPlay** from iPhone, iPad or Mac on the speakers whose firmware
  offers AirPlay (it lists `AIRPLAY` in `:8090/sources`);
- **Android audio capture into UPnP** (BubbleUPnP's "Audio Cast",
  AudioRelay and similar, Android 10 and later). A community list
  reports Deezer as capturable; BubbleUPnP's free version limits it.
  Untested here.

None of these gives a hardware key or works with the phone away. Worth
one FAQ answer on the website ("Deezer: Bluetooth, AirPlay, or audio
cast from Android"), nothing in the app.

## Tests that settle the open points

Ordered by cost. None needs a test build in a user's hands.

1. **Does a Deezer token survive on an STR speaker's NAND?** The debug
   section `bose_sources_file`, in every diagnostic bundle, lists each
   `Sources.xml` entry with its source type, `secretType` and whether a
   `secret` is present (never the value). The next bundles from owners
   who had Deezer (the #1239 reporters, the 2026-06 and 2026-08 mail
   reporters) answer whether converted speakers can still be helped.
   No account, no risk.
2. **Can the firmware log in to Deezer without Bose?** On Jens' own
   Portable: take a Deezer trial (Premium),
   call `setMusicServiceAccount` with source `DEEZER`, then read
   `:8090/sources`, the stand-in's request log and the box syslog.
   Undo with `removeMusicServiceAccount`. Cost: one evening and a free
   trial; power-cycle recoverable.
3. **Does A1 keep a live login?** Needs a speaker that is still stock
   and logged into Deezer, after A1 shipped in a normal release. The
   owner installs that release and opens `:8090/sources`: `DEEZER`
   `READY` and a Deezer key that plays is the pass. A bundle then shows
   `deezer_login.kept` true. If it fails, the kept file still holds the
   login, so a fix to the served shape needs no second capture.

### What to ask the owner of the stock ST10 in #1239

Nothing until step 3, and then one thing: install the release that
carries A1 and post the `DEEZER` line of `:8090/sources` plus whether a
Deezer key plays. Before A1, installing STR on that speaker would have
replaced its Deezer login with an empty one, which is why the earlier
ask was rightly declined. A second stock ST10 with Deezer is in the
same thread and would do as well.

## Summary for planning

| Step | Effort | UI | Needs |
|---|---|---|---|
| Mask every `<credential>` in the request log | done | none | nothing |
| Bundle reports `Sources.xml` entries (type, secret present yes/no) | done | none | nothing |
| A2 login test on an own speaker | one evening | none | a Deezer Premium trial |
| A1 keep the existing login across an install | done, field check open | none | a stock, Deezer-linked speaker for the field check |
| A2 build, only if the test passes | 3 to 5 days | one settings card, Deezer links in the play-URL field | the same trial account |
| Route B | about a week | a source in Find | ruled out (decryption) |
| Route C | a FAQ answer | none | nothing |

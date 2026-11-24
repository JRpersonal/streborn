# SoundCloud

Status: **researched 2026-11-23, nothing built.** Vote thread: idea #1122
("SoundCloud on the speaker and on preset keys", 1 vote, no comments),
split out of issue #241, which proposed an app-side resolver feeding STR's
stream proxy.

Researched from the code in this repository, read-only `GET`s on two STR
speakers (a Portable, scm/taigan, and an ST10, sm2/rhino, both 27.0.6),
one public SoundCloud track page, SoundCloud's developer documentation and
its public API issue tracker (`soundcloud/api`), and the source and issue
trackers of yt-dlp, Mopidy-SoundCloud, scdl's `soundcloud.py` and Music
Assistant's `SoundcloudPy`. No SoundCloud account and no API key were used,
and no audio was downloaded.

There are three ways SoundCloud could reach a SoundTouch speaker:

| Route | What plays the music | Feasibility | Legal exposure |
|---|---|---|---|
| A | the official SoundCloud API, resolved by STR, played through the stream proxy | blocked: keys only with a paid creator plan, one shared key per app, 1,500 plays a day per app, tokens need a server STR does not run | low per se, but the key-sharing and quota rules make it unworkable for an open-source app |
| B | SoundCloud's private web API (`api-v2`) with the web player's `client_id` | technically a few days; breaks several times a year | ToS breach, named as such by SoundCloud's API team |
| C | a phone casting or sending audio to the speaker | works today where the speaker has AirPlay, and everywhere over Bluetooth | none for STR |

Two technical facts cut across A and B:

1. **SoundCloud now delivers only HLS AAC in fragmented MP4.** The
   progressive MP3, HLS MP3 and HLS Opus transcodings were retired at the
   end of 2025. STR's stream proxy converts HLS with MPEG-TS or raw
   ADTS/MP3 segments, and refuses fMP4 ("stage 3" of #124, never built).
   So nothing SoundCloud serves plays through STR today, whatever route
   resolves the URL.
2. **The firmware has no SoundCloud client.** No `SOUNDCLOUD` source and
   no `SOUNDCLOUD` service on either speaker; the name exists only as id
   23 in the source-provider catalogue STR's stand-in serves.

Recommendation: close #1122 as covered by Bluetooth and AirPlay (route C,
one FAQ line), and say why. Reopen when SoundCloud gives listener apps a
key without a creator plan (its own tracker, `soundcloud/api#605`, asks for
exactly that and is open). The one piece of work worth doing anyway is
fMP4 support in the stream proxy: it is the format blocker for SoundCloud
and also for radio stations that moved to CMAF HLS.

## What the firmware does with SoundCloud

Read-only `GET`s on `:8090` of both speakers, 2026-11-23:

- `/sources`: Portable lists AUX, STORED_MUSIC, AIRPLAY (`READY`),
  NOTIFICATION, DEEZER, QPLAY, SPOTIFY, UPNP, STORED_MUSIC_MEDIA_RENDERER,
  ALEXA, LOCAL_INTERNET_RADIO. ST10 lists the same family (AIRPLAY there
  is `UNAVAILABLE`, BLUETOOTH `UNAVAILABLE`). No `SOUNDCLOUD` on either.
- `/serviceAvailability`: PANDORA, AIRPLAY (ST10 only), ALEXA, AMAZON,
  BLUETOOTH (ST10), BMX, DEEZER, LOCAL_INTERNET_RADIO, LOCAL_MUSIC,
  NOTIFICATION, QPLAY, SPOTIFY, STORED_MUSIC_MEDIA_RENDERER, TUNEIN, UPNP.
  No `SOUNDCLOUD`.
- `/supportedURLs`: the generic music-service calls
  (`/setMusicServiceAccount`, `/setMusicServiceOAuthAccount`,
  `/removeMusicServiceAccount`, `/search`, `/navigate`, `/select`), nothing
  SoundCloud-specific.
- `internal/marge/responses.go` serves `SOUNDCLOUD` as provider id 23 in
  `sourceProviders`, a copy of Bose's catalogue. The firmware never
  registers such a source; nothing in STR depends on it.

Conclusion: there is no route A as in Deezer (the speaker's own client).
Any SoundCloud playback has to arrive as a stream URL over UPnP `:8091`,
like radio.

## The stream shape today

From one public track page (a long-standing public demo track, fetched
once, HTML only), the hydration JSON lists the track's transcodings:

| preset | protocol | mime type |
|---|---|---|
| `aac_160k` | `hls` | `audio/mp4; codecs="mp4a.40.2"` |
| `aac_96k` | `hls` | `audio/mp4; codecs="mp4a.40.2"` |
| `abr_sq` | `hls` | `audio/mpegurl` (an adaptive master playlist) |

No `progressive`, no `audio/mpeg`, no Opus. This matches SoundCloud's own
announcement ("Moving to Modern Streaming: Introducing AAC HLS on
SoundCloud APIs", developers.soundcloud.com/blog/api-streaming-urls,
2025-09-15): `http_mp3_128_url`, `hls_mp3_128_url` and `hls_opus_64_url`
removed on 2025-12-31, `hls_aac_160_url` preferred and `hls_aac_96_url` as
fallback, `preview_mp3_128_url` kept for 30-second previews only. The page
also carries `policy`, `monetization_model` and a short-lived
`track_authorization` JWT (geo-tagged) that the web player passes when it
asks for a stream URL.

What the AAC playlist delivers, per the mime type: fragmented MP4 (an
`#EXT-X-MAP` init segment with `moov`, then `moof`/`mdat` media segments),
AAC-LC. The playlist is VOD (`#EXT-X-ENDLIST`). Encrypted variants exist
for label content: protocols `ctr-encrypted-hls` and `cbc-encrypted-hls`,
which yt-dlp skips and reports as DRM (its extractor, lines 308 to 311 and
376 to 377 on master, 2026-11).

Signed URLs are short-lived: an app owner on SoundCloud's tracker reports
that the playlist link returned for `hls_aac_160_url` "expire[s] after a
few minutes", so a new one is fetched for each play (`soundcloud/api#607`,
2026-10-28). Whether the segment URLs inside the playlist outlive a long
DJ mix is not documented (test 5).

## Format fit with STR's stream proxy

- The renderer on `:8091` plays plain `http` only. STR's proxy
  (`internal/streamproxy`) re-serves any `https` URL as
  `http://127.0.0.1:8888/stream/raw?u=<b64>` (`handleRaw`) or a preset as
  `/stream/<slot>`.
- HLS is supported since #124 (`hls.go`, `mpegts.go`): it follows a
  master or media playlist, plays a VOD list once in order, demuxes
  MPEG-TS to ADTS/MP3, and passes raw ADTS or MP3 segments through.
- **fMP4 is refused twice**: `parseMediaPlaylist` sets `isFMP4` on
  `#EXT-X-MAP` and `serveHLS` returns "fMP4/CMAF segments not supported
  yet"; `demuxSegment` returns not-playable for any segment starting with
  `ftyp`, `styp`, `moof`, `sidx` or `moov`. The box then sees a 415 and
  `/api/stream-status` records the failure.
- Had SoundCloud kept HLS MP3, the proxy would have played it unchanged
  (raw MP3 segments pass through). That door closed on 2025-12-31.

What stage 3 needs (`fMP4 -> ADTS`), all in the agent, no new dependency:

1. read the init segment once: `moov/trak/mdia/minf/stbl/stsd/mp4a/esds`
   gives the AudioSpecificConfig (object type, sample-rate index, channel
   configuration);
2. per media segment: `moof/traf/tfhd` (default sample size) and `trun`
   (per-sample sizes), then cut the `mdat` payload into access units;
3. prefix each access unit with a 7-byte ADTS header built from the
   config, and write the result to the box as the existing ADTS stream.

That is a few hundred lines with fixtures generated by ffmpeg and a fuzz
test like `mpegts_fuzz_test.go`. A shortcut worth one test first: hand the
speaker the init segment and the media segments concatenated as one
`.m4a`. USB-stick tests show the renderer plays ordinary M4A (AAC)
(`docs/streaming/usb-stick.md`, "M4A (AAC) | plays (heard)"); whether it
plays a *fragmented* MP4 is unknown (test 3). If it does, stage 3 shrinks
to a concatenating pass-through with the `audio/mp4` mime.

Opus would not have helped either: `.opus` is not among the formats the
renderer is known to play.

## Route A: the official SoundCloud API

State of the API (2026-11):

- **Registration reopened, behind a paywall.** SoundCloud closed open
  app registration in 2016 (case-by-case form, "API Sign-up Changes",
  2016-09-23) and for years granted almost nothing. In May 2026 it opened
  self-service registration, but only for Artist Pro subscribers ("In
  May we opened self-service app registration for Artist Pro
  subscribers", blog 2026-06-16; "You need a SoundCloud Artist Pro
  subscription to register API applications and receive credentials",
  docs/api/register-app). A listener asking for a key for a personal,
  non-commercial project was told "The API registration is now available
  exclusively through Artist Pro subscriptions" (`soundcloud/api#612`,
  2026-11-02). Stream calls of newly registered apps are paywalled too:
  "We started paywalling our API this year. For now only newer accounts
  ... are affected" (`#577`, 2026-09-16).
- **Auth.** OAuth 2.1: client credentials (HTTP Basic, against
  `secure.soundcloud.com/oauth/token`) for public resources, authorization
  code with PKCE for user resources (likes, own playlists). Every call,
  including the stream lookup and the fetch of the `hls_*` URL, carries
  `Authorization: OAuth <token>`; tokens live about an hour.
- **Token limits.** Client-credentials tokens: 50 per 12 h per app, 30 per
  hour per IP. So installs cannot each mint their own token from a shared
  key; a central broker would have to hand tokens out.
- **Stream limits.** 1,500 `GET /tracks/:id/stream(s)` per 24 h per
  registered app (`client_id`), about 100 requests a minute overall,
  search 3,000 an hour, likes 500 a day; "up to 10x" for approved
  production apps (docs/api/rate-limits). Since 2026-11-10 only the fetch
  of the actual HLS transcoding counts (`#607`).
- **Streams endpoint.** `GET /tracks/{urn}/streams` returns
  `hls_aac_160_url` / `hls_aac_96_url` (plus `preview_mp3_128_url`).
- **Catalogue.** Go+ and major-label tracks are not served to third-party
  apps at all: "due to contractual reasons we can't expose our major
  label content (that is the one protected by DRM) to third party
  applications" (`#605`, 2026-11-17); "We do not expose Go+ tracks for our
  public API and there are no plans to do so" (`#567`). Ad-monetised and
  region-restricted tracks come back `preview` or `blocked`; one Go+
  subscriber found about 25% of their likes not streamable (`#578`), and
  whole label catalogues answer `403` on `/streams` (`#623`). What plays is
  independent uploads, DJ mixes and creator content.
- **Terms** (docs/api/terms-of-use): credit the uploader and SoundCloud
  wherever track info is shown (section 08); no caching beyond the
  session, no offline (05); no "alternative digital content service" and
  no "webcasting radio app" (03); no scraping (10.4); no reverse
  engineering (10.15); "another person's client ID" only for the same app
  (10.19).

Why it does not fit STR:

1. **Keys.** STR would need one registration, under Jens' own paid
   Artist Pro account, shared by every install, with the client secret
   inside an open-source binary where anyone can lift it. The
   alternative, every owner registering their own app, needs every owner
   to buy a creator plan to listen.
2. **Tokens.** 50 tokens per 12 h per app means a central STR service
   that holds the secret and hands out tokens. STR runs no server by
   design (no cloud dependency is the project's point), and a hardware key
   pressed while that service is down would fail.
3. **Quota.** 1,500 plays a day for the whole user base, 15,000 if
   SoundCloud approves a production tier. A play is a press of a key or a
   track change in a playlist.
4. **Format.** Even with a key, the stream is fMP4 (above).

What it would look like if 1 to 3 were solved (for the record):

- Search and paste: a SoundCloud card in the station search (desktop and
  phone), fed by `/search` and `/resolve` for a pasted
  `soundcloud.com/<user>/<track>` or `/sets/<playlist>` link, with the
  required attribution (uploader name, "on SoundCloud").
- Presets: a new preset type `soundcloud` storing the track or playlist
  URN, not a URL, because signed URLs expire in minutes. The agent
  resolves at press time (`/tracks/{urn}/streams`, then the
  `hls_aac_160_url` with the token) and hands the signed playlist to the
  proxy. A playlist or the owner's likes would reuse the play queue the
  music library uses (`Type "queue"`, `queueLiveURLFn`), resolving each
  track as it starts.
- Effort: stage 3 two days; resolver, token handling, preset type,
  press-time resolution and queue reuse three days; search card on two
  surfaces, attribution and i18n two days. About **seven days**, plus a
  token service STR does not have.

## Route B: SoundCloud's private web API

Every open-source SoundCloud player uses the web player's private API
(`api-v2.soundcloud.com`) with the `client_id` the website itself uses,
scraped out of a JavaScript asset on `a-v2.sndcdn.com`:

| Project | How it authenticates | Recent state |
|---|---|---|
| yt-dlp (Unlicense) | scrapes `client_id:"<32 chars>"` from the site's scripts, caches it, re-scrapes on 401 | 2025-10 and 2025-11 "Support new API URLs"; 2026-01 to 02 403s from DataDome bot protection on `api-v2` (issue #15660, `x-datadome: protected`), fixed with "Fix client ID extraction" (#16019) and Chrome TLS impersonation (#16020) |
| scdl / `soundcloud.py` | same scrape, now with browser impersonation by default | active (pushed 2026-10) |
| Mopidy-SoundCloud | tokens from mopidy.com plus a scraped public id | 2026-03: regex fix after "playing only 30 seconds" (#145); 2026-09 README now warns that SoundCloud authentication is broken (#148) |
| Music Assistant (`SoundcloudPy`) | the user copies `client_id` and the `oauth_token` cookie out of the browser dev tools | "breaks regularly (401 errors whenever the token or web client_id changes) and recently again when progressive MP3 transcodings were removed" (its own users, `soundcloud/api#605`) |

Breakage history in the twelve months to 2026-11: API URL change
(2025-10), playlist API change (2025-11), MP3 and Opus removal
(2025-12-31), DataDome bot protection needing a browser TLS fingerprint
(2026-01), client-id format change (2026-02). Roughly one break a
quarter, each needing a release before keys work again. On the speaker the
DataDome point is worse than for yt-dlp: the agent's Go HTTP client has a
Go TLS fingerprint, and imitating Chrome means pulling in a uTLS-style
library and tracking Chrome versions ("Anything older than chrome-116
seems to get 403'd", #15660).

Terms: section 10.4 (scraping) and 10.19 (another person's client ID).
SoundCloud's API lead to an open-source client author: "by scraping our
v2 API you're violating our term! This can lead to suspension of your
accounts" (`soundcloud/api#567`, 2026-08-10). The public (`hls`)
transcodings carry no encryption, so for unencrypted tracks this is a
terms question, not the circumvention question Deezer raises
(`docs/streaming/deezer.md`, route B). The `ctr-` / `cbc-encrypted-hls`
label tracks are DRM and stay out of reach of any route; touching them
would be circumvention.

Verdict: about four days to build (stage 3 plus resolver, search, preset
type), then a standing maintenance cost and a named terms breach that
puts owners' SoundCloud accounts at risk, for one vote. Not recommended.

## Route C: casting from a phone or computer

- **SoundCloud's own app** casts to "devices that support Bluetooth,
  AirPlay, or Chromecast" (help.soundcloud.com, "Cast to devices"). No
  SoundTouch is a Chromecast target.
- **AirPlay** from iPhone, iPad or Mac works on speakers whose firmware
  lists `AIRPLAY` in `:8090/sources` (`READY` on the probed Portable; the
  probed ST10 lists it `UNAVAILABLE`: the firmware registers AirPlay only
  where the speaker's AirPlay hardware answers). Full catalogue, Go+ included, because it is
  SoundCloud's own app playing.
- **Bluetooth** on every model.
- **DLNA control points** (BubbleUPnP and similar): no current evidence
  that any of them offers SoundCloud as a source; not verified. Android
  audio capture into UPnP (BubbleUPnP "Audio Cast", Android 10+) would
  carry it like any app, untested here.
- **SoundCloud podcasts** publish RSS feeds (`feeds.soundcloud.com`)
  whose enclosures are meant for podcast apps. If the podcast idea
  (#1167) ships, SoundCloud-hosted podcasts would come along, provided the
  enclosures are still progressive MP3 after the 2025 format change
  (untested, test 6).

None of these gives a hardware key, except podcasts if #1167 lands. Worth
one FAQ answer ("SoundCloud: AirPlay or Bluetooth from the SoundCloud
app").

## Tests that settle the open points

Placeholders: Portable `192.0.2.10` (scm, agent via `:17008`), maintainer
PC `192.0.2.20`. Every play test is on the Portable at volume 5,
maintainer runs. Stop after each (`POST /api/stop`).

1. **Firmware knows no SoundCloud** (anyone, read-only; done 2026-11-23
   on a Portable and an ST10, re-run on an ST20/ST30 for completeness):

   ```sh
   for p in sources serviceAvailability supportedURLs; do
     curl -s http://192.0.2.10:8090/$p | grep -io soundcloud || echo "$p: none"
   done
   ```

2. **Current proxy refuses SoundCloud's format** (maintainer runs; no
   SoundCloud involved). Build an HLS in SoundCloud's shape from any own
   MP3 and serve it from the PC:

   ```sh
   ffmpeg -i test.mp3 -c:a aac -b:a 160k -f hls -hls_segment_type fmp4 \
     -hls_time 10 -hls_playlist_type vod -hls_fmp4_init_filename init.mp4 sc.m3u8
   python -m http.server 8000
   curl -X PUT  http://192.0.2.10:17008/api/box/volume -d '{"value":5}'
   curl -X POST http://192.0.2.10:17008/api/play \
     -d '{"url":"http://192.0.2.20:8000/sc.m3u8","title":"fMP4 test","codec":"AAC"}'
   curl -s http://192.0.2.10:17008/api/stream-status
   ```

   Expected today: no audio, stream-status names "fMP4/CMAF segments not
   supported yet". Same file with `-hls_segment_type mpegts` must play
   (control).

3. **Does the renderer play fragmented MP4 directly?** (maintainer runs;
   decides whether stage 3 is a concatenation or an ADTS rewrite):

   ```sh
   cat init.mp4 sc*.m4s > frag.m4a      # served by the same http.server
   curl -X POST http://192.0.2.10:17008/api/play \
     -d '{"url":"http://192.0.2.20:8000/frag.m4a","title":"frag m4a","mime":"audio/mp4"}'
   ```

   Pass: the whole file plays, and `GET /api/box/volume` still reads 5.

4. **Stage 3 build** (maintainer runs, on a dev agent with fMP4 support):
   repeat test 2 unchanged. Pass: audio from start to end, the speaker
   stops on its own at `ENDLIST`, no reconnect loop in the agent log.

5. **A real SoundCloud stream end to end** (maintainer runs, measurement
   only, after test 4 passes). Get the signed `aac_160k` playlist URL of a
   Creative Commons track and of a 60+ minute DJ mix on the PC (yt-dlp
   uses the web player's id; nothing of this goes into STR's code):

   ```sh
   yt-dlp -g -f hls_aac_160k "https://soundcloud.com/<user>/<cc-track>"
   curl -X POST http://192.0.2.10:17008/api/play \
     -d '{"url":"<printed m3u8 URL>","title":"SC test","codec":"AAC"}'
   ```

   Pass: the track plays to the end. For the mix, note whether playback
   survives past 10, 30 and 60 minutes (`/api/stream-status`, agent log
   `hls:` lines); a 403 mid-mix means segment URLs expire and a feature
   would need re-resolution on failure.

6. **SoundCloud podcast RSS** (maintainer runs; for #1167): from the RSS
   feed of a SoundCloud-hosted podcast, read the first enclosure's headers
   and the first 4 KB only:

   ```sh
   curl -sIL "<enclosure URL>"                 # Content-Type, Accept-Ranges
   curl -sL -r 0-4095 "<enclosure URL>" | head -c 4 | xxd   # ID3 or 0xFFFB
   curl -X POST http://192.0.2.10:17008/api/play \
     -d '{"url":"<enclosure URL>","title":"SC podcast","codec":"MP3"}'
   ```

   Pass: `audio/mpeg`, progressive, plays.

7. **Route C** (maintainer runs): SoundCloud app on an iPhone, AirPlay to
   the Portable at volume 5, one Go+ or label track and one independent
   upload. Pass: both play; that is the FAQ answer.

### AirPlay per model, checked 2026-11-23

Read from `:8090/sources` on the maintainer's speakers, all on firmware 27.0.6:

| Model | `AIRPLAY` source |
|---|---|
| SoundTouch Portable (two units) | `READY` |
| SoundTouch 30 (scm chassis) | `READY` |
| SoundTouch 10 (two units) | `UNAVAILABLE` |

The firmware registers AirPlay only where the speaker's AirPlay authentication chip answers, so a SoundTouch 10 has Bluetooth as its only phone route. Any answer that points users to AirPlay has to name that.

### Results on a real speaker, 2026-11-23

Run on a SoundTouch Portable at volume 5, test tones made with ffmpeg (AAC 128 kbit/s, 44.1 kHz, 30 s) and served over plain http from a PC on the LAN, started with `POST /api/play`:

| Test | What was played | Result |
|---|---|---|
| T2a | HLS with fMP4 segments (`#EXT-X-MAP`, `.m4s`), SoundCloud's current shape | Fails. Agent log: `stream proxy: HLS playback failed ... hls: fMP4/CMAF segments not supported yet`; the speaker reports `BAD_URL` and drops to `INVALID_SOURCE`. |
| T2b | The same tone as HLS with MPEG-TS segments | Plays (`LOCAL_INTERNET_RADIO`, `PLAY_STATE`). |
| T3 | The fMP4 init segment and all media segments joined into one `.m4a`, handed to the speaker directly as `audio/mp4` | Fails. The speaker's decoder reports `Could not obtain first-frame` and `ERROR_DECODE`. |

So the speaker cannot decode fragmented MP4 at all, not even as one file. Stage 3 cannot be a pass-through: the proxy has to unpack the AAC frames from the fMP4 fragments and re-serve them as ADTS (or MPEG-TS), the way it already re-serves TS segments.

## Summary for planning

**Verdict: close #1122 as covered by Bluetooth and AirPlay (route C).**
Reopen if SoundCloud issues listener apps keys without Artist Pro and per
user tokens (`soundcloud/api#605`). Build fMP4 support in the stream proxy
on its own merit; it is the format blocker for any SoundCloud route and
helps CMAF radio.

**User-facing outcome:** a FAQ line ("SoundCloud plays from the SoundCloud
app over AirPlay or Bluetooth; STR cannot put SoundCloud on a key, because
SoundCloud gives no API key to apps like STR"), and the #1122 reply
saying the same.

| Step | Effort | UI | Needs |
|---|---|---|---|
| FAQ line and #1122 reply | an hour | none | nothing |
| fMP4 to ADTS in the stream proxy (HLS stage 3) | 2 days | none | tests 2 to 4 |
| Route A (official API) | about 7 days | SoundCloud card in search, preset type | Artist Pro key, a token service STR does not run, a quota of 1,500 plays a day; not possible as STR is built |
| Route B (private api-v2) | about 4 days, then a fix every few months | same | a ToS breach SoundCloud names; not recommended |
| SoundCloud podcasts via #1167 | 0 beyond #1167 | none | test 6 |

**Legal and ToS exposure:** route A low but unworkable (shared secret in
an open binary, quota for the whole user base); route B a direct breach of
API terms 10.4 and 10.19, with SoundCloud warning of account suspension;
DRM label tracks out of reach on every route; route C none.

**Tests for a real speaker** (details above):

1. `GET :8090/sources`, `/serviceAvailability`, `/supportedURLs` for
   `SOUNDCLOUD` (anyone; done on two speakers).
2. ffmpeg-made fMP4 HLS through `POST /api/play`, expect "not supported"
   today, MPEG-TS control plays (maintainer runs, Portable, volume 5).
3. Concatenated fragmented `.m4a` straight to the renderer with
   `mime audio/mp4` (maintainer runs, Portable, volume 5).
4. Test 2 again on a dev agent with stage 3 (maintainer runs, Portable,
   volume 5).
5. A real SoundCloud `aac_160k` playlist through STR's proxy, one track
   and a 60+ minute mix (maintainer runs, Portable, volume 5, measurement
   only).
6. A SoundCloud podcast RSS enclosure: headers, first 4 KB, then play
   (maintainer runs, Portable, volume 5).
7. SoundCloud app over AirPlay to the Portable (maintainer runs, volume 5).

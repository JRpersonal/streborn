# Subsonic servers (Navidrome, Gonic, Airsonic, ...)

Status: **researched, not built.** Vote thread: idea #1751 (2 votes).
Related: idea #1754, "A playlist on a preset key plays its current content
(smart playlists rebuilt by the server)" (1 vote).

Researched 2026-11-23 from the code in this repository (main at `77e80615`),
the upstream repositories of Navidrome, Gonic and Airsonic-Advanced, the
OpenSubsonic specification, and read-only calls against the public Navidrome
demo server (`demo.navidrome.org`, Navidrome 0.64.2). No speaker was touched
and nothing was played on one.

## The ask, and who asks

The reporter in #1751 wants to browse and play a library on a self-hosted
Subsonic server: configure URL, user and password in the desktop app, browse
by artist, album and playlist, search, and put an album or playlist on a
hardware key. A second participant, the admin of that server, added the fact
that decides the design: **the server is reached over the internet** (its own
subdomain, behind a reverse proxy), not on the listener's LAN. That is why the
answer already given in the thread ("run Jellyfin or MiniDLNA on the same
music folder") does not help them: DLNA is LAN only. The admin offered a test
account on their Navidrome and help with testing; the offer was accepted in the
thread on 2026-11-08.

#1754 comes from a support mail: smart playlists of a few hundred tracks,
rebuilt nightly by the server, put on preset keys, play the same songs every
day because a folder key keeps the track list it was saved with.

## 1. What works today, without new code

STR's Library plays any UPnP/DLNA MediaServer it can discover or that is added
by address. So the question is which Subsonic servers also speak DLNA.

| Server | DLNA/UPnP MediaServer? | Evidence |
|---|---|---|
| Navidrome 0.64.2 (2026-09-24) | **No** | No UPnP, DLNA or SSDP code in the tree. Issue navidrome#305 "UPnP/DLNA support" closed as not planned (2023-12). PR navidrome#3618 "Draft: DLNA Media Server" (a proof of concept, open since 2025-01, last touched 2026-09) is not merged. |
| Gonic v0.23.0 (2026-10-22) | **No** | No UPnP, DLNA or SSDP code in the tree; no issue or PR mentions DLNA. |
| Airsonic-Advanced (kagemomiji fork, active, last push 2026-09-18, last release 11.1.4 of 2024-09) | **Yes** | `UPnPService.java` and `service/upnp/*ContentDirectory*.java` in the tree: a ContentDirectory by folder, artist, album and genre, switched on in its settings. The older `airsonic-advanced/airsonic-advanced` repository has not been pushed since 2024-04. |
| Ampache | Yes, an optional UPnP backend in its config | From Ampache's own configuration; not checked here. |
| Funkwhale | No | |

So **Airsonic-Advanced (and probably Ampache) already play in STR's Library**
on the LAN, with folders, folder keys and Recently played, once their DLNA
switch is on. Untested on an STR speaker, but it is the same path every NAS
takes. Navidrome and Gonic, the two servers people mean when they say
"Subsonic" today, do not.

Two more routes need no STR code, with limits:

- **A Subsonic-to-UPnP bridge on the LAN.** The upmpdcli Subsonic plugin
  (by GioF71, navidrome discussion #2324, maintained into 2025-12, tested
  against Navidrome and LMS) runs upmpdcli as a UPnP MediaServer that reads a
  Subsonic server. STR's Library would list it like any NAS. It needs an
  always-on LAN device with Docker, which the #1751 admin explicitly declined
  ("a bit too much over-engineering"). Whether its track URLs point at the
  Subsonic server itself (then an `https` server fails on the speaker, see
  section 2) or at the bridge is not documented; untested.
- **Casting from a phone.** Symfonium (Android, paid) plays from Subsonic,
  Navidrome and Jellyfin servers to UPnP/DLNA renderers, and has a proxy mode
  for renderers that cannot fetch the server's URL directly (its users report
  it is needed with an `https` Navidrome). Every SoundTouch is a UPnP renderer
  on `:8091`, so this should play, with the phone kept on and no hardware
  keys. Untested on a SoundTouch.

None of these serves the #1751 reporter (a remote `https` server, keys
wanted). A FAQ line can name Airsonic-Advanced's DLNA switch and Symfonium.

## 2. The Subsonic API, as far as STR needs it

### Authentication

Every call carries `u` (user), `v` (protocol version), `c` (client name) and
one credential set (OpenSubsonic API reference):

- `p`: the password in the clear (or `enc:`-hex). Never used by STR.
- `t` and `s`: `t = md5(password + salt)`, lowercase hex, a fresh random
  salt of at least six characters per call. Supported by Navidrome, Gonic
  and Airsonic-Advanced. Error `41` if a server does not support tokens.
- `apiKey`: OpenSubsonic extension `apiKeyAuthentication`; with it, none of
  `u`, `p`, `t`, `s` may be sent (error `43`), a bad key is error `44`.
  **Navidrome added it on 2026-09-28** (navidrome#6219), after 0.64.2: the
  demo server's `getOpenSubsonicExtensions` does not list it yet, so it
  arrives with Navidrome's next release. Gonic does not have it.

A token and its salt are a replayable credential: whoever holds the pair can
call every API method as that user until the password changes. They hide the
password itself (useful when it is reused elsewhere), nothing more. An API key
is the same strength but can be revoked on its own. So for STR: store the API
key when the server offers one, otherwise the password, and in either case
keep it on the agent and out of every URL that leaves the agent.

The `formPost` extension (Navidrome, Gonic) accepts all parameters in a POST
body, which also keeps credentials out of the server's access log.

### Browsing

| Need | Call | Notes |
|---|---|---|
| Artists | `getArtists` (ID3) | One answer for the whole library, no paging. A large library is a large JSON body: cap it on the agent. |
| Albums of an artist, songs of an album | `getArtist`, `getAlbum` | |
| All albums, newest, random | `getAlbumList2?type=alphabeticalByName/newest/random&size=&offset=` | Paged, so it maps onto Browse pages. |
| Folders | `getMusicFolders`, `getIndexes`, `getMusicDirectory` | The file-tree view. In Navidrome the folder ids are synthetic but stable. |
| Playlists | `getPlaylists`, `getPlaylist` | Smart playlists included (section 4). |
| Search | `search3?query=&artistCount=&albumCount=&songCount=` | Server-side, one call. |
| Cover | `getCoverArt?id=&size=` | |

A song (`Child`) carries `id`, `title`, `artist`, `album`, `duration`,
`suffix`, `contentType`, `bitRate`, `size`, and in OpenSubsonic
`samplingRate` and `bitDepth`: everything STR's library items and keys need
(title, artist, album, length, MIME).

### Streaming

`stream.view?id=<song>` with optional `format` (`raw` disables transcoding),
`maxBitRate` (kbit/s), `estimateContentLength`, and `timeOffset` for music
where the server has the `transcodeOffset` extension (Navidrome has it).

Measured on the Navidrome demo (0.64.2, an MP3 of 6,556,160 bytes, request
with `Range: bytes=0-1023`):

| Request | Answer |
|---|---|
| `format=raw` | `206`, `audio/mpeg`, `Accept-Ranges: bytes`, `Content-Range: bytes 0-1023/6556160` |
| `format=mp3&maxBitRate=128` | `200`, `audio/mpeg`, `Accept-Ranges: none`, no length: a live stream |
| `format=mp3&maxBitRate=128&estimateContentLength=true` | `206`, `Content-Range: bytes 0-1023/4834994`: an estimated length, ranges claimed |
| unknown song id, or wrong password | **`200`** with an XML error body (`Content-Type: application/xml`) |

Three consequences for STR:

1. **An error is a `200`.** A speaker handed a `stream.view` URL that fails
   gets an XML document as "audio". Whatever fetches it has to check the
   Content-Type (`text/xml` / `application/xml` / `application/json` means
   an error) before a byte reaches the speaker, and turn it into a real HTTP
   error so the queue's bad-URL handling skips the track.
2. **Raw files behave like a NAS file** (length, ranges), which the speaker's
   direct play path needs: the firmware drops the connection after a few
   hundred KB and resumes with `Range: bytes=32768-` (measured on an ST10,
   `internal/webui/librarykey.go`). A transcoded stream behaves like radio.
3. **Can the speaker fetch `stream.view` itself?** Only from a plain `http`
   server it can reach, with the credentials in the query, because the
   renderer sends no headers and does not do HTTPS (usb-stick spike,
   section 4). That rules out the #1751 case (remote `https`) and would put
   the credential into everything that records a URL: the speaker's own
   now-playing (`:8090/now_playing`, readable by anyone on the LAN),
   `presets.json`, `recent.json`, `last-play.json`, the logs and the
   diagnostic bundle. The radio path does not help either:
   `boxurl.RawStream` wraps the upstream URL in base64 inside the URL the
   speaker gets, which is encoding, not hiding.

The renderer's formats decide when to transcode (sink list in
`docs/streaming/usb-stick.md`, section 4): MP3, AAC/M4A, FLAC up to 48 kHz,
Ogg Vorbis and WAV play as they are; Opus, ALAC (unknown), AIFF, APE,
WavPack, DSD and FLAC above 48 kHz (`samplingRate`) get
`format=mp3&maxBitRate=320`. The renderer judges by the URL extension, so the
URL STR hands it must end in the extension of what is actually sent.

## 3. Shape in STR

### Desktop app only is not enough

Keys resolve on the speaker (the `boxws` hook reads `presets.json`), the
phone page browses from the speaker (`/api/library/*`), and a key must play
with the computer off. A Subsonic source that lives only in the desktop app
would cover the app's Library and nothing else, and would have to hand the
speaker credential-carrying URLs. So the agent has to know the server.

### Recommended: the agent serves the Subsonic server as one more media server

v1.9 already did exactly this for the USB stick (`internal/usbmedia`,
`internal/webui/usbmusic.go`): the agent answers a small UPnP ContentDirectory
under `:8888/usb/`, lists it in `/api/library/servers`, resolves it over
loopback in `resolveMediaServer`, and "every existing library path (browse,
play, folder queues, library keys, Recently played) uses it like any NAS"
(usbmusic.go header). The desktop app reads the same entry from each speaker
(`speakerUSBSticks` in `desktop-app/app_library.go`) and browses it through
the agent with the existing `dlna` client.

A Subsonic server fits the same mould, as a translating ContentDirectory:

```
desktop app / phone page --Browse SOAP--> agent :8888/subsonic/<sid>/control
                                            |  getArtists, getAlbum, getPlaylist, search3 ...
                                            v
                                    Subsonic server (LAN http, or WAN https)

speaker renderer --GET--> http://127.0.0.1:8888/subsonic/<sid>/file/<songId>.<ext>
                            agent adds the credential, fetches stream.view, checks the type
```

- **Containers.** Root: Artists, Albums, Playlists, Folders (and optionally
  Recently added, Random). Object ids carry the kind and the Subsonic id
  (`ar:<id>`, `al:<id>`, `pl:<id>`, `dir:<id>`), so a key's `FolderRef` and
  `TrackRef` (`internal/presets/presets.go`) store stable ids and the
  existing re-resolve (`libraryreresolve.go`) works unchanged.
- **Items.** `res` URLs are `/subsonic/<sid>/file/<songId>.<ext>`, built from
  the request's Host like the stick's, so the desktop app gets a LAN address
  and the speaker's own renderer gets `127.0.0.1:8888` after the own-host
  rewrite (`usbLoopbackURL`, generalised from `/usb/` to both prefixes). The
  URL never contains a credential, so `presets.json`, `recent.json`, the
  speaker's now-playing and the bundles stay clean.
- **The file route** builds `stream.view` with a fresh salt (or the API key),
  chooses `raw` or `mp3` per song as in section 2, passes `Range` through
  for `raw`, refuses an XML or JSON answer with a `502`, and fetches with
  the stream proxy's TLS transport (system roots plus the clock-tolerant
  verify in `internal/streamproxy/tlsclock.go`, because a speaker's clock can
  be wrong) behind the SSRF dial guard. Played directly, never through the
  radio proxy: the proxy's guard refuses loopback, which is the stick's
  lesson (`usbFileMIME`).
- **Search** maps the ContentDirectory `Search` to one `search3` call, as the
  stick answers Search itself (`usbmedia/search.go`), so no client walks the
  tree.
- **Cover art** through `/subsonic/<sid>/art/<id>` (credential added the same
  way), so neither the phone nor the desktop app sees it.
- **A stable UDN** derived from the server URL and user (a name-based UUID),
  so the same server configured on four speakers is one server in the
  desktop app (deduplicated by UDN, unlike the per-speaker stick), and a key
  copied between speakers resolves on the new one.
- **Not announced over SSDP**, like the stick: no new timer, no fight with
  BoseApp over UDP 1900.

What this buys over a bespoke `/api/subsonic/...`: no second code path in the
desktop Library, the phone page, the queue, the keys, the re-resolve and
Recently played. The test oracle is the repo's own `dlna` client.

What it costs on the speaker: binary growth of roughly 100 to 200 KB
(`encoding/json`, `encoding/xml`, `crypto/md5`, `net/http` are linked
already), which matters on the `scm` ST30 with about 3.7 MB free. RAM: one
Browse page at a time; the unpaged `getArtists` answer must be read through
an `io.LimitReader` (a few MB) and paged out of memory, never kept. No NAND
use beyond the config file. No timers: the server is called only when a
client browses, searches or plays, or a key is pressed.

### Configuration

- Desktop app: one "Add a Subsonic server" entry next to the existing
  "add a server by address" box in the Library (`library_manual.go`): URL,
  user, password or API key, a Test button. On save it is sent to every STR
  speaker (the same fan-out the app uses for other fleet settings). No new
  view; the server then appears in the existing server list.
- Agent: `/mnt/nv/streborn/subsonic.json`, mode `0600` (like
  `deezer-login.json` and `wlan-creds`), holding URL, user and either the API
  key or the password. A write-only API: `POST` to set, `DELETE` to remove,
  `GET` returns URL, user, server type and version and `hasSecret`, never the
  secret. The diagnostic bundle reports the same masked shape.
- The phone page needs nothing new: it lists `/api/library/servers`.

### Where the code goes

| Piece | Where | Days |
|---|---|---|
| Subsonic client: auth (token+salt, apiKey when the server lists it, fall back on `41`/`42`), ping, artists, albums, playlists, folders, search3, cover, stream URL, error decoding, body caps; tests against an `httptest` fake with Navidrome's and Gonic's shapes | `internal/subsonic` (agent only) | 1.5 |
| ContentDirectory translation (Browse pages, DIDL, Search via search3), sharing the device description, SCPD and DIDL writer with `internal/usbmedia` (move them into a small shared package) | `internal/subsonic` + `internal/webui/subsonicmusic.go` | 1.5 to 2 |
| File and cover routes: format choice, Range pass-through, type check, own-host rewrite for both prefixes, direct-play MIME | `internal/webui` | 1 |
| Config store (0600), write-only API, `/api/library/servers` entry, `resolveMediaServer`/`libraryResolveServer` hook, debug section with masked fields | `internal/webui`, `internal/subsonic` | 1 |
| Desktop app: the add/edit entry with Test, fan-out to all speakers, merge kind `subsonic` entries deduplicated by UDN | `desktop-app/` | 1 to 1.5 |
| i18n EN/DE (others follow), FEATURES, threat-model lines, this doc | | 0.5 |
| Tests against a real Navidrome (the #1751 test account) and Gonic, and on speakers | | 1 to 1.5 |
| **Total** | | **7.5 to 9** |

#1754 adds 1 to 1.5 days (next section).

## 4. Smart playlists on a key (#1754)

**How a folder key resolves today.** A folder or playlist saved on a key is
a queue preset: `Items` holds up to `presets.MaxQueueItems` (1,000) tracks as
they were at save time, plus a `FolderRef` (server UDN, container id). A
press plays the stored `Items`. The folder is read again only when the first
stored track **no longer serves** (`reresolveQueuePreset` in
`internal/webui/libraryreresolve.go`), and then only to map the stored
tracks to their new URLs. New songs never reach the key. That is true for
every DLNA server today, and it would be true for a Subsonic playlist served
through the bridge above, because the bridge only changes where the list comes
from, not when it is read.

**What Navidrome does.** Smart playlists (`.nsp` files, or rules a client
such as Feishin sends) are evaluated on the server "when the UI or any
Subsonic client accesses the playlist", at most every
`SmartPlaylistRefreshDelay` (default 5 s, overridable per playlist with
`refreshDelay`). So one `getPlaylist` call at press time returns the current
content: STR has nothing to evaluate itself.

**What #1754 needs** is one change in the shared queue-key recall, not in
Subsonic: on a press, read the key's folder again (`collectFolderTracks` on
the `FolderRef`, the same bounded walk) and play what comes back; on an
error, a timeout or an empty answer, play the stored `Items` as today and
log why. Store the fresh list only when it differs, so NAND is not written on
every press (the queue preset bloat findings apply).

- **Subsonic playlists: on by default.** One HTTP call, the server does the
  work, and a playlist is exactly the thing that changes.
- **DLNA folders: a per-key switch, off by default**, because a sleeping NAS
  makes every press wait (the open question in #1754). With a short budget
  (about 3 s for the first page) before falling back to the stored list.

Answers to the two questions in #1754: a switch per key for DLNA, always for
Subsonic playlists; when the server is not reachable, the stored list plays.

Effort: 1 to 1.5 days on top, most of it tests (press with the server off,
slow, changed, emptied).

## 5. Security

- **Credentials** live in one place, the agent's `subsonic.json` (`0600`), on
  each speaker that has the server. The desktop app keeps no copy beyond the
  form it sends (re-entering is the edit path), so it adds no secret store on
  three operating systems. They never appear in a URL the agent hands out, in
  a preset, in Recently played, in the speaker's now-playing, in logs, or in
  a diagnostic bundle (masked as `hasSecret`).
- **Root on the speaker reads them.** Anyone with SSH (opt-in) or a
  firmware-level exploit reads the file, as with the Wi-Fi passphrase today.
  Prefer the API key where the server has one (revocable on its own); the
  setting text should say "a dedicated user or an API key, not your admin
  login".
- **The agent API is LAN-only but not authenticated** (THREAT-MODEL). Anyone
  on the LAN can set or delete the server (as with every agent setting), not
  read the secret. And anyone on the LAN can browse and play the configured
  library through the speaker's `/subsonic/` routes, as with the music stick.
  For a WAN server this is new: the speaker turns a remote, password-protected
  library into one that every device on the home LAN can play and download.
  Same `isLocalLAN` gate as `/usb/`; one THREAT-MODEL entry.
- **SSRF.** The server URL is user-supplied. Every fetch goes through
  `netutil.SafeHTTPURL` (http/https only) and a client whose dialer runs
  `netutil.DialGuardSSRF` (loopback, link-local and metadata addresses
  refused at dial time, LAN allowed): `GuardedClient`, or the stream proxy's
  transport with that dial hook. Redirects stay guarded at dial; the file
  route follows none to a different host. Song and cover ids from the server
  are only ever query values, URL-escaped, never paths.
- **Untrusted server text** (titles, artists, playlist names) reaches the
  DIDL through the XML escaping the stick uses, and the apps through their
  existing escaping.
- **TLS.** A WAN server must present a valid certificate; no "accept
  self-signed" switch in v1.

## 6. Risks

1. **Transcoded streams on the direct path.** A transcoded `stream.view` has
   no length and no ranges, or (with `estimateContentLength`) an estimated
   length with ranges. The speaker's direct path re-requests with
   `Range: bytes=32768-`. Unknown how the firmware takes a live MP3 there,
   and whether Navidrome's estimated length ever disagrees with the bytes.
   Tests 4 and 5 below settle it before the format rule is fixed.
2. **Upload of a home server over the internet.** A remote FLAC needs about
   1 Mbit/s of the server's upload for the whole track. A `maxBitRate`
   setting (default: raw on LAN addresses, `mp3` 320 for WAN) avoids
   stutter reports.
3. **Large libraries.** `getArtists` is unpaged; a library of tens of
   thousands of artists is a multi-MB answer on a speaker with little RAM.
   Capped read; the Albums container (paged) is the fallback.
4. **Server variety.** Gonic, Airsonic-Advanced, Ampache, LMS and others
   differ in details (folder ids, error shapes, missing OpenSubsonic
   fields). v1 is tested against Navidrome and Gonic only and says so.
5. **NAND on the `scm` ST30.** A few hundred KB more binary where the OTA
   already drops the engine to fit.

## Summary for planning

**Verdict: build if** it reaches the top of the Ideas list (#1591) or a
release wants a library feature, with the #1751 admin's Navidrome test account
in hand. Two votes today. The design is cheap because v1.9's USB stick laid
the path: one more media server the agent serves, and everything else in the
Library already works with it. Build #1754 in the same release: it is a small
change to the shared folder-key recall and is what makes a playlist key worth
having. Meanwhile, a FAQ line: Airsonic-Advanced's DLNA switch already works
with STR's Library, Navidrome and Gonic do not speak DLNA, and Symfonium can
cast from them.

**User-facing outcome.** In the desktop app's Library, "Add a Subsonic server"
(URL, user, password or API key, Test). The server then appears in the server
list of the desktop app and the phone page on every speaker, with Artists,
Albums, Playlists and Folders, search, play, play a folder, put an album or
playlist on a key, and Recently played; also for a server on the internet over
`https`, which DLNA cannot do. A playlist key plays the playlist's current
content, so a Navidrome smart playlist stays fresh.

**Effort:** 7.5 to 9 days for Subsonic, plus 1 to 1.5 days for #1754.

**Risks:** transcoded streams on the speaker's direct path (tests 4 and 5),
a WAN server's upload bandwidth, unpaged artist lists on large libraries,
untested server variants, NAND on the `scm` ST30, and the LAN gaining
playback access to a remote library (THREAT-MODEL entry).

**Tests**

1. *Before any code, no speaker:* against the #1751 test account (or a local
   Navidrome and Gonic in Docker), record `ping`, `getOpenSubsonicExtensions`,
   `getArtists` size, `getPlaylists` with a smart playlist (`readonly`), and
   the headers of `stream.view` for `format=raw`, `format=mp3&maxBitRate=320`,
   with and without `estimateContentLength`, each with
   `Range: bytes=32768-`. Pass: the shapes match section 2; note any
   difference in Gonic.
2. *Agent unit tests:* token and salt per call, apiKey only when listed,
   fallback on `41`/`42`, `43` never produced; an XML/JSON answer on the file
   route becomes `502`; no credential in any `res`, cover or file URL, in a
   saved preset, in `recent.json` or in the debug state; SSRF guard refuses
   `http://127.0.0.1:...` and `http://169.254.169.254/` as server URL.
3. *Raw file, direct path (maintainer runs, Portable, volume 5):* on the
   Portable, with a LAN Navidrome over plain `http`, play an MP3 album from
   the desktop Library. Pass: plays, the next track follows, the length shows,
   `:8090/now_playing` shows a `127.0.0.1:8888/subsonic/...` location with no
   `t=`, `s=`, `p=` or `apiKey=` in it.
4. *Transcoded stream (maintainer runs, Portable, volume 5):* play a FLAC
   above 48 kHz or an Opus file, which the bridge sends as `mp3`. Pass: plays
   to the end, the queue advances, and the agent log shows how the firmware's
   `Range` re-request was answered. Fail here means the format rule needs
   `estimateContentLength` or the radio-style path.
5. *Navidrome `stream.view` through the agent, by hand (maintainer runs,
   Portable, volume 5):* `POST /api/play` with the bridge file URL of one
   song (`http://192.0.2.1:8888/subsonic/<sid>/file/<songId>.mp3`, the
   Portable's own address). Pass: the agent rewrites it to loopback and the
   song plays; then the same with a deleted song id: the speaker does not go
   amber on an XML body, the agent logs the `502`.
6. *WAN `https` server (maintainer runs, Portable, volume 5):* the #1751 test
   account. Play an album; then set the Portable's clock back a year (the
   tlsclock case) and play again. Pass: both play; the bundle shows the
   server as `hasSecret: true` and no secret.
7. *Keys (maintainer runs, Portable, volume 5):* put an album and a playlist
   on keys 5 and 6, power-cycle the Portable, press 5 then 6. Pass: both play
   from the first track with the computer off; copy key 6 to another speaker
   and press it there (read-only check of `/api/presets` on that speaker for
   the `FolderRef`).
8. *Smart playlist on a key, #1754 (maintainer runs, Portable, volume 5):* a
   Navidrome `.nsp` playlist ("added in the last 1 day") on key 6. Add an
   album to the server, rescan, press key 6. Pass: the new album's songs are
   in the queue. Then stop Navidrome and press key 6: the stored list plays
   and the log names the fallback.
9. *DLNA folder key with the live switch (maintainer runs, Portable,
   volume 5):* a folder key on a NAS with the switch on; add a file, press.
   Pass: the new file is in the queue. Switch off: the stored list, as today.
10. *Phone page, no audio needed:* open the phone page on two speakers.
    Pass: the Subsonic server is listed once per speaker, browses, and the
    desktop app lists it once (deduplicated by UDN).
11. *Airsonic-Advanced today, no code:* enable its DLNA switch in a local
    Docker instance and open STR's Library. Pass: it is listed and browses;
    if so, the FAQ line can say "works today".

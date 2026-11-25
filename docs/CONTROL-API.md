# STR local control API

STR exposes a small HTTP control API on the speaker itself, so home
automation (Home Assistant, ioBroker, Node-RED, a shell script, ...) can
turn the speaker on and off, set the volume, recall presets and control
playback without any Bose cloud and without a separate middleware.

Everything here is local to your LAN. There is no authentication: treat
reachability on your network as the trust boundary, the same as the
speaker's own Bose API.

## Base URL and discovery

The agent listens on port **8888**:

```
http://<speaker-ip>:8888
```

On the BCO/whitelisted chassis (SoundTouch Portable, and some ST20), the
same API is reached on port **17008** instead (the box redirects it to the
agent), so if `:8888` is refused, use `:17008`.

STR announces itself over mDNS as `_streborn._tcp.local`, so you can
discover the speaker's address and port automatically.

All request and response bodies are JSON.

## Playback

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/status` | - | Current now-playing (source, track, art, play state). |
| `POST` | `/api/play` | `{"url":"https://...","title":"...","codec":"MP3"}` | Play any stream URL. `title`, `icon`, `codec` are optional. `codec` is the station codec (`MP3`, `AAC`, `AAC+`); an HE-AAC stream without it can play silence. Do not send `mime` for a stream: it marks a music-library file and hands the URL straight to the speaker. |
| `POST` | `/api/play/<slot>` | - | Play STR preset slot `1`-`12` (`7`-`12` is the second row, see Presets). |
| `POST` | `/api/pause` | - | Pause. |
| `POST` | `/api/resume` | - | Resume. |
| `POST` | `/api/stop` | - | Stop. |
| `POST` | `/api/next` | - | Next track (within a folder / playlist). |
| `POST` | `/api/prev` | - | Previous track. |
| `GET`  | `/api/position` | - | Where the speaker is inside the track: `{"ok":true,"positionSec":N,"durationSec":N,"seekable":bool}`. `durationSec` is `0` for radio. `seekable` says whether `/api/seek` would take a jump right now. |
| `POST` | `/api/seek` | `{"positionSec":90}` | Jump to that second of the playing track. Only for a music-library file the speaker fetches itself (alone or as the current track of a folder) while it plays. Answers `{"ok":true,"positionSec":N,"durationSec":N}`, or `{"error":"...","reason":"..."}` with `409` (`not-seekable` for radio, Spotify and proxied tracks, `not-playing` while paused or stopped, `length-unknown`), `400` (`bad-position`, `out-of-range`) or `502` (`seek-refused`, `speaker-unreachable`). |
| `POST` | `/api/spotify/liked` | `{"liked":true}` / `{"liked":false}` | Save the playing Spotify song to Liked Songs, or remove it from there: what the remote's thumbs keys do, for an app. Only while STR's own Spotify is what the speaker plays (also paused); a speaker with a display shows LIKED or REMOVED. The song goes to the Liked Songs of the Spotify account the SPEAKER's engine is logged in with, which may differ from the account on the phone that asks. Answers `{"ok":true,"liked":bool,"uri":"spotify:track:...","name":"...","repeat":bool}` (`repeat`: the same request for the same song a moment ago, nothing written twice), or `{"error":"...","reason":"..."}` with `409` (`not-playing` when STR's Spotify is not what plays, `no-track`, `not-a-song`, `no-engine` on a speaker without the Spotify engine), `400` (`bad-body`, or plain text for invalid JSON) or `502` when the engine or Spotify refused (`engine-too-old`, `no-session`, `unauthorized-401`, `forbidden-403`, `rate-limited-429`, `rejected-400`, `timeout`, `failed`). An agent that has this reports `"spotifyLiked":"true"` in `/api/agent/version`. |
| `POST` | `/api/spotify/playmode` | `{"action":"shuffle"}` / `{"action":"repeat"}` | Switch the playing Spotify playlist's shuffle on or off, or step its repeat (off, the whole playlist, the one song, off): what a remote key bound to shuffle or repeat does, without the display notice. The state is read fresh from the engine first, so a press moves on from what is really set. Only while STR's own Spotify is what the speaker plays (also paused). Answers `{"ok":true,"shuffle":bool,"repeat":"off"/"context"/"track"}` with the state after the step (the field the press did not change is left out when the engine could not be read), or `{"error":"...","reason":"..."}` with `409` (`not-playing`, `no-engine`), `400` (`bad-body`, or plain text for invalid JSON) or `502` (`engine-refused`). An agent that has this reports `"spotifyPlayModes":"true"` in `/api/agent/version`; `/spotify/info` then carries the same state as `"playModes":{"shuffle":bool,"repeat":"..."}`. |
| `GET`  | `/api/spotify/playmode` | - | The engine's shuffle and repeat now, unchanged: `{"ok":true,"shuffle":bool,"repeat":"off"/"context"/"track"}`, or `409` `no-engine` / `502` `engine-refused`. |
| `GET`  | `/api/stream/title` | - | Live radio title of the stream STR is proxying: `{"title":"Artist - Title","swapped":bool}`. `title` is `""` when the station sends none. `swapped` says the station below is flagged and its title was already turned round. |
| `GET`  | `/api/stream/title-swap` | - | `{"stations":[...],"swapped":bool}`: the stream URLs flagged as sending their title "Title - Artist", and whether the station playing now is one of them. |
| `POST` | `/api/stream/title-swap` | `{"swap":true}` | Flag (or with `false` unflag) a station whose live title arrives as "Title - Artist" instead of "Artist - Title". Without `"station":"<stream url>"` it acts on the station playing now (`409` when nothing is). The flag is kept per station in `/mnt/nv/streborn/title-swap.json` and applies to everything that shows the title: the speaker display, the app, Recently played and the phone remote. Answers `{"ok":true,"station":"...","swapped":bool,"title":"<live title now>"}`. |

## Volume, power and source

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/box/volume` | - | Returns `{"value":N,"target":N,"muted":bool}` (0-100). |
| `PUT`  | `/api/box/volume` | `{"value":N}` | Set the absolute volume (0-100). |
| `GET`  | `/api/box/max-volume` | - | The speaker's volume ceiling: `{"supported":true,"volume":N}`, `0` means off. This is the "Max volume" setting of the desktop app; read it from here so every remote shows the same value. |
| `POST` | `/api/box/max-volume` | `{"volume":N}` | Set the ceiling (1-100) or switch it off (`0`). Stored on the speaker, so write it once when a slider is released, not while it moves. |
| `GET`  | `/api/box/start-volume` | - | The level the speaker goes to when it is switched on and starts playing: `{"supported":true,"volume":N}`, `0` means off. |
| `POST` | `/api/box/start-volume` | `{"volume":N}` | Set the start level (1-100) or switch it off (`0`). Stored on the speaker; write it once per change. |
| `POST` | `/api/box/power` | `{"on":true}` / `{"on":false}` | Power on, or send to standby. |
| `PUT`  | `/api/box/source` | `{"source":"AUX"}` | `AUX`, `BLUETOOTH` or `STANDBY`. |

For a relative change ("volume up 5"), read `GET /api/box/volume`, add to
`value`, and `PUT` the result.

## Presets

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/presets` | - | The STR preset store (what the twelve slots point at). |
| `PUT`  | `/api/presets` | `[{...},...]` | Write a whole set (at most 12, slots 1-12) in one go; slots the set does not name are left alone. |
| `PUT` / `DELETE` | `/api/presets/<slot>` | a preset / - | Save or clear slot `1`-`12`. |
| `POST` | `/api/box/preset-move` | `{"from":N,"to":M}` | Move the station on slot `N` to slot `M` (1-12, also between the rows), replacing what `M` held. Deliberately outside the `/api/presets/` prefix: an agent that predates it then answers a plain 404, which nothing else does. |
| `POST` | `/api/box/preset-copy` | `{"targets":["<ip>"]}` or `{"all":true}` | Copy this speaker's keys to other STR speakers, the way the desktop app's Transfer does: this agent writes the set onto each target (`PUT /api/presets`, key by key on an older target), syncs its hardware keys and, when a Spotify key goes along, hands on this speaker's Spotify login. Both rows go to a target that keeps two. Targets must be in this speaker's peer list (`/api/peers`; `all` means every peer that answered the last sweep). Answers `200` with `{"keys","rows","done","total","results":[{"name","ip","ok","copied","row1Only","notCopied","spotify","code","conflict","error"}]}` once every target is done; `code` is `already-on-slot` (with `conflict` `{name,slot}`, nothing written there), `unreachable`, `refused` or `unknown-speaker`. `409` with `code` `busy`, `no-presets` or `no-targets`. LAN only. |
| `POST` | `/api/box/save-playing` | `{"slot":N}` | Keep what the speaker plays on slot `N` (1-12): the playing music-library folder, the library song, a copy of the key that plays, or the station itself. Answers `{"slot","kind","name","unchanged"}`; `422` with a `code` when nothing that can be kept plays (Spotify and Pandora/iHeartRadio/Deezer go through `PUT /api/presets/<slot>`), and the same `409 already-on-slot` as a save. |
| `PATCH` | `/api/presets/<slot>` | `{"name":"..."}` | Rename slot `1`-`12`. Only the name changes; the station, its artwork and a Spotify key's playlist stay as they are. Names longer than 64 characters are shortened, an empty one is refused (422), an empty key answers 404. |
| `GET`  | `/api/box/presets` | - | The hardware preset buttons as the box reports them (always 1-6). |
| `POST` | `/api/box/presets/recall` | `{"slot":N}` | Recall hardware preset `N` (1-6). This presses the speaker's own key, so the second row is not reachable here; use `/api/play/<slot>`. |

`POST /api/play/<slot>` is usually what you want to start a preset from
automation; `presets/recall` mirrors a physical button press.

A station lives on one key at a time. Saving one that is already on another
key is refused with `409` and `{"code":"already-on-slot","slot":N,"name":"..."}`,
because clearing that other key without being asked is how presets got lost.
`POST /api/box/preset-move` is the explicit way to do it: it writes the new key
and frees the old one in a single store write. The rule spans both rows, so the
`slot` in that answer can be `7`-`12`.

### The second row (slots 7-12)

Slots `7`-`12` are a second row of six presets: slot `6+n` is preset key `n`
pressed twice quickly. The agent stores them exactly like `1`-`6` and every
preset endpoint above, the alarms and `/api/play/<slot>` accept them. They
never reach the speaker's own preset list (its keys are 1 to 6), so
`/api/box/presets` and `/api/box/presets/recall` stay at 1-6, and a row-2 slot
plays the way `/api/play/<slot>` plays any slot (`/stream/7`-`/stream/12`,
`/spotify/stream-7.ogg`-`stream-12.ogg`). An agent that has the second row
reports `"presetRows":"2"` in `/api/agent/version`; an older one has no such
key and answers `400` for slot 7 and up.

On the speaker itself, tapping key `n` and then holding it down (press it a
second time within a moment and keep it down until the display says
`SAVED 2X n`) stores what played just before the tap on slot `6+n` and plays
it. The tap still starts key `n`'s own station for a moment; that cannot be
prevented. Key `n` (row 1) is never changed by this gesture, neither by STR
nor by the speaker's own hold-to-store. Radio, Spotify, a library song or
folder and the speaker's own services are kept the way a plain hold keeps
them. If nothing that can be kept played (standby, AUX, Bluetooth), nothing
is stored and the display says `NOT SAVED`. If the item already sits on
another slot it is not stored a second time: the display says where it sits
(`ON KEY m` or `ON 2X m`) and that slot plays. A key combination (for example
1 and volume down) is never this gesture. In a zone or stereo pair the gesture
needs the speaker's key trace, as the double press does. There is no endpoint
for the gesture; its result is an ordinary preset on slot `6+n`.

## Pandora stations

Only on a speaker where Pandora is enabled (a US speaker, or the opt-in) and
an account is linked; see
[`streaming/us-services.md`](streaming/us-services.md). The speaker's own
Pandora client plays the station, STR only hands it the item.

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/pandora/stations` | - | The linked account's stations, read from the speaker: `{"status":"READY","stations":[{"name","source","location","type","itemName","art","presetable"}]}`. The account itself is never part of the answer. |
| `POST` | `/api/pandora/play` | `{"location":"...","name":"...","type":"stationurl"}` | Start one. `location` comes from the list; `name` and `type` are optional. |

Both answer `409` with `{"code":"service-off"}` when Pandora is not enabled on
the speaker and `{"code":"not-linked"}` when it lists no Pandora account, and
`502` with a short reason when the speaker refuses (`navigate-failed`,
`select-failed`, or `sources-unreadable` when its source list cannot be read).
To keep a station on a key, start it and then save the key
with `PUT /api/presets/<slot>` and `{"type":"native","name":"..."}`: the agent
takes the item from what the speaker plays.

### Unlinking a Pandora or iHeartRadio account

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `DELETE` | `/api/pandora/account` | - | Remove the Pandora account from the speaker and from STR. |
| `DELETE` | `/api/iheart/account` | - | The same for iHeartRadio. |

Both need the service enabled on the speaker (`409` otherwise, as above). The
call asks the speaker to remove the account and then re-reads its source list
to confirm, so it takes **1 to 5 seconds**. Do not retry while one is still
in flight; wait for its answer.

| Status | Body | Meaning |
| ------ | ---- | ------- |
| `200` | `{"ok":true}` | The account is gone, also when it already was. |
| `502` | `{"code":"remove-refused","error":"..."}` | The speaker refused the removal. |
| `502` | `{"code":"still-linked","error":"..."}` | The speaker still lists the account as ready afterwards. |
| `500` | `{"code":"clear-failed","error":"..."}` | STR could not drop its own copy of the account. |
| `503` | `{"code":"remove-busy","error":"..."}` | Another removal of the same account was still running when this request gave up waiting (after 15 seconds). |

A repeated `DELETE` is safe. Requests that arrive while a removal runs do not
send a second one to the speaker: they wait and get the same answer as the
first. A request after the account is gone answers `200` without touching the
speaker. The `error` text is a short English sentence for people; branch on
`code`. It never carries the speaker's raw reply, which stays in the agent
log.

## Deezer

The speaker's own Deezer client ([`streaming/deezer.md`](streaming/deezer.md)).
A new Deezer login is not possible without the Bose cloud (A2 is closed), so
STR only keeps the login a speaker already had and hands it back when the
firmware lost it. No switch is needed: Deezer is offered on every speaker.
LAN only, like the Pandora calls; `POST` needs a JSON content type.

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`    | `/api/deezer/account` | - | Where Deezer stands: `{"state","detail","account","stuck","rejected","source":{"status","hasAccount"},"login":{...}}`. `state` is `connected`, `connecting` (STR handed the kept login back less than 90 s ago and the speaker is not `READY` yet), `unavailable` (the speaker holds an account it is not logged in with), `not-connected` or `unknown` (the source list could not be read); an older agent also answered `failed`. `stuck`: the speaker's last Deezer account call answered `1024` or not at all, its Deezer client hangs until a power cycle. `rejected`: the firmware's last relogin retry says Deezer refused the token (code `3330`). `account` is masked; `login` is the `deezer_login` debug section (never the login itself), with `lastAttempt` and `lastRetry` (`{"code","meaning","at"}`). Reads the speaker's `/sources` once. |
| `POST`   | `/api/deezer/account` | ignored | `410 Gone`, `{"code":"new-login-unsupported","error":"..."}`. The e-mail and password sign-in of earlier versions; refused so an older app cannot hang the speaker's Deezer client again. The body is never read or handed on. |
| `POST`   | `/api/deezer/recheck` | `{}` | The card's Check again. When STR keeps a Deezer login and the speaker lists `DEEZER` without a working account, removes the speaker's account (`removeMusicServiceAccount`) and sends it `<updates deviceID=".."><sourcesUpdated/></updates>` on `:8090/notification`; the firmware re-reads `/full` and creates the account again with the kept token. Answers like `GET`, plus `"recreate":{"ran","skipped","error"}` (`skipped`: `no-kept-login`, `not-listed`, `ready`, `unreadable`, `busy`, `ran-recently`; at most one run a minute). The agent runs the same step once after it starts. A speaker that keeps a login then also offers it to the speakers of the home that need one, and a speaker that needs one asks them to offer (`offer-request`, below). |
| `GET`    | `/api/deezer/login-need` | - | Agent to agent: whether this speaker needs a Deezer login, secret-free. `{"needs":true,"key","accountHash"}` (`key`: this run's X25519 share key; `accountHash`: SHA-256 over `str-deezer-share-v1`, the key and the lowercased account) or `{"needs":false,"why"}` (`kept`, `ready`, `not-listed`, `no-account`, `unreadable`). Never the account in clear. Reads the speaker's `/sources` once. |
| `POST`   | `/api/deezer/kept-login` | `{"sealed":{"key","nonce","box"}}` | Agent to agent, write-only: another speaker's kept Deezer login, sealed to the share key. Taken only from loopback or a speaker in this agent's peer roster, only for an account this speaker lists without a working login, and never when it keeps a login already; then kept and handed to the firmware (the recreate, trigger `peer`). Answers `{"status":"accepted"}` or `{"status":"refused","reason"}` (`403` `unknown-caller`, `409` for the state reasons, `400` `undecryptable`/`malformed`/`bad-request`). Never echoes the login. Untested on hardware ([`streaming/deezer.md`](streaming/deezer.md)). |
| `POST`   | `/api/deezer/offer-request` | `{}` (not read) | Agent to agent: a speaker that needs a Deezer login (its Check again) asks this one to offer its kept login. Taken only from loopback or a speaker in the peer roster, at most once per requester every 3 minutes. `202` `{"status":"offering"}`, then this speaker runs its normal offer (need signal, hash match, sealing) towards that requester alone; else `{"status":"refused","reason"}`: `403` `unknown-caller`, `429` `rate-limited`, `409` `already-offered`, `no-kept-login` or `own-login-rejected`. No secret in the request or the answer. |
| `DELETE` | `/api/deezer/account` | - | Removes the account from the speaker (`removeMusicServiceAccount`) and forgets STR's kept copy of the login. The same answers and the same one-at-a-time rule as the Pandora removal above; `200` adds `"status"`. A removed Deezer account cannot be linked again without Bose. |
| `POST`   | `/api/deezer/play` | `{"url":"https://www.deezer.com/en/album/123","name":"..."}` | Plays a Deezer playlist, album or track link on the speaker's own Deezer client. A canonical `deezer.com` link is read as it is; a share link (`link.deezer.com`, `deezer.page.link`) is followed through Deezer's own hosts only. `name` is optional (else Deezer's public API names it). Answers `{"ok":true,"kind","id","name"}`; `409` `deezer-not-linked` or `native-service-unavailable`, `422` `deezer-link-unresolved`, `502` `select-failed` or `sources-unreadable`. |

To keep a Deezer item on a key, start it and save the key with
`PUT /api/presets/<slot>` and `{"type":"native","name":"..."}`, or hold the
key on the speaker: Deezer is a native service like Pandora, so the agent
takes the item from what the speaker plays.

## Favourites

The starred stations the desktop app keeps and the phone remote shows. The
desktop app owns the list: it pushes the whole list to every speaker whenever
it changes, and it learns a removal, addition or new order made elsewhere only
from the speaker's notes.

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`    | `/api/favorites` | - | The list, in the order the user put it: `[{"stationuuid","name","url","url_resolved","codec","bitrate","favicon","country","homepage","source","mime","durationSec","artist","album","location","itemType"}]`. A favourite's key is `stationuuid`, else `name\|url`. A starred music-library track (`source` `"upnp"`) may carry `artist` and `album`, at most 200 characters each. A Pandora station has `source` `"pandora"`, no address, and plays from `location` and `itemType` through `POST /api/pandora/play`; its key is `pandora:<location>`. |
| `DELETE` | `/api/favorites?id=<key>` | - | Remove one favourite. This is the call for a remote app. It writes a removal note, hands the removal on to every other STR speaker, and the desktop app takes the favourite off its own list within seconds. |
| `POST`   | `/api/favorites` | one favourite: `{"stationuuid","name","url","url_resolved",...}` | Add one favourite. This is the call for a remote app. `name` and an http(s) `url` or `url_resolved` are required; a Pandora station instead carries `"source":"pandora"`, its `location` and `itemType` from `GET /api/pandora/stations`, and its cover as `favicon`. It stores the favourite, writes an addition note, hands it on to every other STR speaker, and the desktop app stars it within seconds. Answers `{"ok":true,"added":<bool>,"id":"<key>"}`; `added` is false when the speaker already had it. |
| `POST`   | `/api/favorites/order` | `{"ids":["<key>",...]}` | Put the favourites in a new order. This is the call for a remote app. Keys the speaker does not know are skipped, favourites the list leaves out follow in their old order. It stores the order, writes an order note, hands it on to every other STR speaker, and the desktop app takes the order over within seconds. Answers `{"ok":true,"count":<n>,"at":<ms>}`. |
| `GET`    | `/api/favorites?removed=1` | - | The notes: `{"removed":[{"id","at"}],"added":[{"id","at","fav"}],"order":{"at","ids"}}`. An addition note lasts until the desktop app has taken the favourite over; `order` is `null` unless an order set outside the desktop app is still to be taken over. |
| `PUT`    | `/api/favorites` | the whole list | The desktop app's sync, with `?orderAt=<ms>`, the time of its own last order. Do not use it to add, remove or move a favourite: a changed list writes no note, so the desktop app undoes it with its next push and the other speakers never hear of it. While an order note is newer than `orderAt`, the noted order is kept over the pushed one. |

## Alarm clock

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/alarms` | - | The alarm document, its `rev`, plus a read-only `status` block. |
| `PUT`  | `/api/alarms` | the whole document, ideally with the `rev` it was read at | Replaces it wholesale. With `rev`, only while that is still the stored revision, otherwise `409`. LAN only. |

An alarm is
`{"id","enabled","name","hour","minute","days","slot","volume","autoOff","snooze"}`:
`days` are weekdays with `0` = Sunday, `slot` is a preset 1-12, `volume` is
1-100 or `0` to leave the speaker's own level alone, `autoOff` switches the
speaker off that many minutes after the alarm starts (1-720, or `0` for never),
and `snooze` is how many minutes a snoozed alarm stays quiet (`5`, `9`, `10` or
`15`; absent or `0` means 9). Snooze itself is a key action
(`PUT /api/keyactions` with `{"keys":{"aux":"snooze"}}`, thumbs keys or `aux`).
The document also carries one IANA `zone` for the speaker; an empty zone means
UTC. At most 8 alarms, and an alarm with no days is rejected rather than treated
as "every day".

An alarm plays on its own speaker only. It does not re-form a permanent group,
and `autoOff` switches off only that speaker, not the group.

A rejected `PUT` answers `400` with the reason as plain text, meant to be shown
to the user as it stands. The `status` block reports `clockTrusted`,
`zoneResolved`, `nextFire`, `nextAlarmId`, `lastFire` and, while an alarm rings
or is snoozed, `ring` (`{"id","ringing","snoozedUntil","snoozes"}`); it is
ignored on the way in. `nextFire` comes from the speaker, so it is the honest answer to "when
will this actually go off", including its zone and its clock.

**Revisions: do not undo another app's change.** Every app that edits the
alarms (STR's phone page, a third-party remote) sends the WHOLE document, so an
app that saves an old copy silently undoes whatever another app changed since
it read it: an alarm switched off from one app came back on when the phone page
saved its copy (discussion #1918). `rev` prevents that:

- `GET` answers `"rev":<n>`, a number that goes up by one with every save
  (`0` for a document not saved since revisions exist). It survives an agent
  restart.
- Send that `rev` back with the `PUT`. If another app saved in between, the
  speaker stores nothing and answers `409` with the current document (same
  shape as `GET`, with its current `rev`). Apply your user's one change to
  that document (find alarms by `id`, never by position) and `PUT` again with
  the new `rev`.
- A `PUT` without `rev` replaces the document whatever is stored, the way it
  always did, so older apps keep working. A `PUT` that sends `rev` must handle
  `409`. Note that `0` is a real revision, not "none".
- A successful `PUT` answers the stored document with its new `rev`; keep that
  one for the next save rather than the one you sent.

An agent from before revisions answers no `rev` and ignores one sent to it.

```bash
curl -s $BOX/api/alarms
curl -s -X PUT $BOX/api/alarms -H 'Content-Type: application/json' -d '{
  "rev":4,
  "zone":"Europe/Berlin",
  "alarms":[{"id":"weekdays","enabled":true,"hour":6,"minute":30,
             "days":[1,2,3,4,5],"slot":3,"volume":25,"autoOff":60,"snooze":9}]}'
```

See [AUTOMATION.md](AUTOMATION.md) for what a fire actually does and how the
clock is handled.
## Sleep timer

| Method   | Path | Body | Notes |
| -------- | ---- | ---- | ----- |
| `GET`    | `/api/box/sleep` | - | `{"active":false}`, or `{"active":true,"remainingSec":N,"group":bool}` while armed. |
| `POST`   | `/api/box/sleep` | `{"minutes":N,"group":bool}` | Arms the timer, replacing one that is already running. `minutes` is 1-720; `0` or less cancels. `group: true` switches off the whole group, not only this speaker. |
| `DELETE` | `/api/box/sleep` | - | Cancels. |

The timer lives in the agent's memory only, so a reboot or an agent update
cancels it. When it fires the speaker goes to standby, and with `group: true` every
other member of its group too. A speaker that is already in standby is left
alone.

## Groups

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`    | `/api/box/zone` | - | The live zone, plus `remembered` when none is live, `permanent` when the saved group is the durable kind, and `setAside` (`{"master","name","members":[...]}`) while a temporary group holds a permanent one aside. |
| `POST`   | `/api/box/zone` | the whole group | Forms or REPLACES the group. Send the full member list every time. |
| `DELETE` | `/api/box/zone` | - | Takes the live group apart. The saved group survives; add `?forget=1` to delete that too. |
| `GET`    | `/api/box/zone/volume` | - | `{"grouped","stereo","members":[...],"average"}`. A member that did not answer reports `volume: -1` rather than disappearing. |
| `POST`   | `/api/box/zone/volume` | `{"value":N}` or `{"ip":"...","value":N}` | Every member, or one of them. |

The form body is
`{"master":{"deviceID","ip"},"slaves":[{"deviceID","ip"}],"mode","permanent","defineOnly","stereo","name"}`.

### Stereo pair names

The speakers hold a stereo pair's name, in the same pair record the Bose app
reads and edits. Every client sees the same name that way, so set it there:

- `name` in the form body (with `"stereo":true`) names a new pair. Left out or
  blank, the pair is called `Stereo pair`.
- `POST /api/box/zone/stereo-name` with `{"name":"..."}` renames a live pair.
  Send it to BOTH members; each one holds its own copy of the record.
- `GET /api/box/zone` reports the name as `stereo.name`, and
  `GET /api/box/zone/volume` as `stereoName`.
- `DELETE /api/box/zone?stereo=1` undoes the pair, and the name goes with it.
  A pair formed again from the same two speakers starts as `Stereo pair`.

The desktop app shows the speakers' name. It only falls back to a name of its
own when the speakers carry the default, and it never writes that name back over
`Stereo pair` (#1579).

Two fields decide more than they look like they do.

**`permanent` is not carried forward.** The document is stored exactly as sent.
A POST without `permanent` on top of a durable group does not delete that group
(agent v1.4.0 and later): the master's agent sets the durable group aside, the
new group is an ordinary one, and the next `DELETE` without `forget` brings the
durable group back as it was. So a client that adds or removes ONE member and
leaves `permanent` out gets a change that lasts only until the group ends. To
make the change itself durable, read `permanent` from `GET /api/box/zone` first
and send it back. A POST with `"permanent":true` replaces the durable group and
drops anything set aside; `DELETE ?forget=1` deletes both. Only one group is
kept aside. Since agent v1.6.5 `GET` reports it as `setAside`, so a client can
name the group a `permanent:true` form would replace; older agents do not report
it. Agents before v1.4.0 overwrite the durable
group instead. (Found in a third-party client, 2026-09-14, where adding a
speaker silently demoted the saved group.)

**A 200 is not a success.** The dissolve answers
`{"ok":false,"remaining":N,"error":"..."}` when the speaker took the request and
kept the group anyway, and `{"ok":true,"nothing":true}` when there was no group
to take apart. Both arrive as HTTP 200. Treating either as success is how an app
shows a green tick over a group that is still playing.

### Volume keys of a permanent group's main speaker

In a permanent group the volume keys on the main speaker, and the IR remote
pointed at it, turn the whole group up and down (agent after v1.8.0). The
agent on the main speaker sees the firmware's volume event and moves every
member that is in the live zone by the same number of steps, from the level
that member had when the gesture began, clamped to 0..100. That is the group
slider's rule, so the balance between the speakers stays and a member that ran
into 0 or 100 gets its spacing back when the key goes the other way. There is
no setting for it: it belongs to permanent groups, and to nothing else. A group
that is not permanent, a stereo pair, and a temporary group on top of a
permanent one behave as before.

Only a key press on the main speaker counts. A level that arrives any other
way stays with the main speaker and changes the balance: `PUT /api/box/volume`
(the app's and the phone page's slider for that speaker), the group volume
routes above (they write every member themselves), the Spotify app's volume
(the engine already mirrors it to the members), an alarm, the start volume, the
volume ceiling and STR's own muted wakes. A member's own keys stay local too,
and so does mute.

How the agent knows it was a key depends on the speaker. Where the firmware's
key trace names the volume keys (seen on the Portable), a volume event is passed
on only when the trace shows a key press on the speaker or its remote with it;
a program writing to the Bose port (`:8090/volume`, or a `VOLUME_UP` sent to
`:8090/key`) is then left alone. Where the trace never names the volume key
(the SoundTouch 10 and the other sm2 boards), the agent passes on every volume
event that none of STR's own writers explains, so there a third-party write to
the Bose port moves the group like the key does. Home automation that should
move only the main speaker uses `PUT /api/box/volume`, on every model. The
`group_volume_follow` section of the diagnostic bundle says which of the two a
speaker decides by.

A member is written through its own STR agent, which applies that speaker's
volume ceiling; a member without STR is written on its Bose port. A stored
member that is asleep, was taken out or is muted is left as it is. A held key
is passed on as it goes, one request per member at a time, always with the
newest level.

## Agent and history

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`    | `/api/agent/version` | - | `{"version","build"}`, plus `friendlyName` and `model` when the agent knows them, `"presetRows":"2"` on an agent with the second preset row (slots 7-12), `"spotifyLiked":"true"` on an agent whose Spotify engine takes `POST /api/spotify/liked`, and `"spotifyPlayModes":"true"` on one that takes `POST /api/spotify/playmode`. The cheapest "is this really an STR agent" probe: require `version` in the BODY, an open port and a 200 alone prove nothing. |
| `PUT`    | `/api/box/bass` | `{"value":N}` | Range as the speaker reports it; not every model has bass control. |
| `GET`    | `/api/recent` | - | Recently played, newest first: `ts`, `source`, `cardKey`, `cardName`, `cardArt`, `cardURL`, `track`, `account`, `homepage`, and for a music-library entry (`source` `upnp`) `mime` and `durationSec`. |
| `DELETE` | `/api/recent` | - | Clears the history. Answers `{"ok":true,"removed":N}`. |

### Replaying a history entry

How an entry is started again depends on its `source` and `cardKey`:

| Entry | Request |
| ----- | ------- |
| `source` `radio` | `POST /api/play` with `{"url":cardURL,"title":cardName,"icon":cardArt}`. |
| `source` `upnp`, `cardKey` starts with `queue:` (an album or folder) | `POST /api/queue/replay-card` with `{"key":cardKey,"name":cardName,"art":cardArt}`. The speaker plays the whole folder again. `cardURL` is only its first track. |
| `source` `upnp`, any other `cardKey` (one track) | `POST /api/play` with `{"url":cardURL,"title":cardName,"icon":cardArt,"mime":mime,"duration_sec":durationSec}`. |

A music-library play whose media server is switched off is refused with
`502` and
`{"code":"library-server-offline","error":"<sentence>","offline":true,"server":"<name>"}`.
Nothing is started and nothing is added to the history; show `error` or your
own text for `code`. A folder replay that cannot find its media server at all
answers `200` with `"offline":true` and `"server"` instead of `"status":"ok"`,
so check the body, not only the status.

An agent up to v1.6.0 took a `upnp` entry sent to `/api/play` without `mime`
for a radio station and filed it under `radio`. Later agents recognise the
file, but only `/api/queue/replay-card` plays more than the first track of
an album.

## Music library and a USB stick in the speaker

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/library/servers` | - | The media servers registered on this speaker, `[{"udn","name"}]`. While a USB stick is in the speaker, the last entry is the stick: `{"udn":"uuid:str-usb-<id>","name":"USB stick <label> (<speaker>)","kind":"usb","location":"http://<host>/usb/device.xml","speaker":"<speaker>","label":"<label>"}`, with `location` built from the address you used (`:8888` or `:17008`); `speaker` is the speaker's own name and `label` the stick's FAT volume label, each left out (and out of `name`) when not known. A stick that is not served has no `udn` and carries `"refusal"` instead: `install-stick`, `not-fat32`, `big-clusters`, `unreadable` or `unsupported-model`. Opening this list is also what lets the speaker mount a stick nothing mounted yet (read-only). |
| `GET`  | `/api/library/browse?udn=<udn>&id=<container>&start=<n>` | - | One page of a folder of a registered server or of the stick: `{"server","folders":[{"id","title"}],"tracks":[{"title","url","art","mime",...}],"total","start","count"}`, or `{"offline":true,"server":"<name>"}`. `id` empty is the top level. Play a track with `POST /api/play` and its `url` and `mime`. |

The stick itself is a UPnP ContentDirectory (LAN only, no SSDP
announcement), so any DLNA client can browse it at its `location`:

| Method | Path | Notes |
| ------ | ---- | ----- |
| `GET`  | `/usb/device.xml` | Device description (`MediaServer:1`). `404` when no music stick is served. |
| `GET`  | `/usb/cds.xml` | ContentDirectory service description. |
| `POST` | `/usb/control` | SOAP: `Browse` (`BrowseDirectChildren`, `BrowseMetadata`), `GetSystemUpdateID`, `GetSearchCapabilities`, `GetSortCapabilities`. `Search` answers UPnP error 401; an unknown object 701. Object ids are the stick-relative paths, `0` is the top level. |
| `GET`  | `/usb/file/<path>` | A file, only `.mp3 .flac .m4a .aac .ogg .oga .wav .wma`, with Range support. The URL ends in the file's real name, which the speaker's renderer needs. |
| `GET`  | `/usb/art/<path>` | A folder's cover: `cover`, `folder`, `front` or `albumart` as `.jpg`, `.jpeg` or `.png`. |

A stick URL played on the speaker that holds the stick is fetched by it over
`127.0.0.1:8888`; the agent rewrites its own LAN address there by itself.

The speaker plays FLAC and WAV up to 48 kHz only. A FLAC or WAV on the stick
above that is listed, but not handed to the speaker:

- `POST /api/play` with such a file, and `POST /api/play/<slot>` on a key
  that holds one, answer `422`
  `{"error":"This speaker cannot play this file: it has a sample rate above 48 kHz.","code":"library-file-hires","sampleRate":96000,"title":"..."}`
  (plus `"slot"` for a key). Nothing is sent to the speaker.
- A folder (`POST /api/queue`, `POST /api/library/play-folder`, a folder
  card, a folder key) steps over such tracks. The `/api/queue` answer
  carries `"skippedHiRes":<n>` and marks each one `"skipped":true` in
  `items`; `play-folder` and `replay-card` carry `"skippedHiRes":<n>`. A
  folder of nothing else answers `422` with the same `code` and
  `"folder":true`.
- When the speaker refuses to decode a stick file anyway
  (`AUDIO_ERROR_DECODER`), `GET /api/queue` carries
  `"usbRefused":{"title":"...","reason":"decoder","at":"<RFC 3339>"}` while
  that file is still the last play and no folder runs.

## Wi-Fi
LAN-only, like every other call here, and refused from outside the private
address ranges.

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`    | `/api/box/wlan/networks` | - | The networks the speaker has stored (names in clear), the current one, `wired`, and two previews: `forgetOthers` (`removable`, `code`) and `forgetAll` (`{"allowed":bool,"code":"..."}`, see `DELETE /api/box/wlan`). |
| `PUT`    | `/api/box/wlan` | `{"ssid":"...","password":"..."}` | Move the speaker to another network. Optional `hidden`, `open`, `force`, `forgetOthers`. On a cabled speaker the network is only stored (`"status":"stored"`). A speaker that is already on that network with the password a confirmed switch recorded answers `"status":"unchanged"` with `"unchanged":true` and leaves its connection alone. |
| `DELETE` | `/api/box/wlan` | `{"reboot":false}` (optional) | Forget the Wi-Fi altogether, for a speaker that should run on its network cable only (#969). See below. |
| `POST`   | `/api/box/wlan/forget-others` | `{"reboot":false}` (optional) | Forget every stored network but the one the speaker is on. Wi-Fi speakers only. |
| `PUT`    | `/api/box/wlan/copy` | `{"ssid":"...","password":"..."}` | Give STR its own copy of the network the speaker is ALREADY on, store-only: writes `wlan-creds` (and the supersede baseline) and nothing else, never the radio, the firmware's store, `wpa_supplicant.conf`, the apply marker or a restart. See below. |

`PUT /api/box/wlan/copy` fills in a missing copy, it never replaces one. It
answers `200` with `{"status":"stored-copy","stored":true,"evidence":"..."}`
only where the agent itself reports `wlanCreds: "missing"` (a coprocessor
chassis without STR's copy), and `409` with `{"code":"..."}` otherwise:
`already-has-copy`, `retired` (the owner moved the speaker outside STR),
`not-needed` (the chassis keeps its Wi-Fi by itself), `speaker-in-setup`,
`no-firmware-profile` (the firmware reports no stored Wi-Fi profile at all, the
one state in which run.sh would push the copy at an online speaker),
`other-network` (the speaker names, or stores, a different network) or `busy`
(a Wi-Fi change is running). `400` with `"code":"invalid"` for an empty SSID,
a password shorter than 8 or longer than 64 characters, or a line break in
either. The copy is used exactly like any other: by the offline rescue when the
speaker has no lease 90 s into a boot. An agent older than the endpoint answers
the path with its phone page, so a client must check for the JSON status.

`DELETE /api/box/wlan` only runs when the speaker is on a network cable AND the
request reached it over that cable: its default route must leave through a
live cable, and the address the request arrived on must belong to the cable
interface. Without Wi-Fi a speaker that has no cable drops off the network for
good, so everything else is refused with HTTP `409` and
`{"status":"refused","code":"..."}`:

| Code | Meaning |
| ---- | ------- |
| `not-wired` | The speaker is not on a network cable. |
| `reached-over-wifi` | It is cabled, but this request came in over Wi-Fi. Call its cable address. |
| `chassis-unsupported` | A BCO chassis (Portable, ST30 scm, ST20 spotty): its Wi-Fi chip doubles as `eth0`, so a cable cannot be told apart from Wi-Fi, and its network list (BoseApp's `AirplayConfiguration.xml` slots) has no verified removal path. Always refused there. |

On the wpa chassis (sm2: ST10, ST20/ST30 sm2, ...) an accepted call clears the
same stores a Wi-Fi change writes, and nothing else: every `<profile>` in the
firmware's `NetworkProfiles.xml` (the rest of the file stays byte for byte),
`/etc/wpa_supplicant.conf` rewritten in full with its global lines and no
network block, the networks of the running `wpa_supplicant`, and STR's own
records (`wlan-creds`, the intended-network record, the one-shot apply
marker), so no boot path puts the network back. The answer counts what went
(`firmwareRemoved`, `runningRemoved`, `confRewritten`, `strRecordsRemoved`,
`removedTags`, never a network name). When the firmware store changed, the
speaker restarts once (`"rebooting":true`) because its network manager reads
that store at start only; pass `{"reboot":false}` to restart it yourself
later. An unrecognised store shape answers `500` with
`"code":"unexpected-store"` and changes nothing. A USB stick that still
carries `wlan.conf` sets the network up again on the next boot, so take it
out first.

## Display

These only do something on a speaker whose display STR can draw on (the
SoundTouch 20, 30, Portable and Wave, see `docs/FIRMWARE-NOTES.md`);
elsewhere they answer without touching anything.

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `POST` | `/api/box/identify` | - | "Find this speaker": the display shows `HELLO` and the speaker's name with pulsing rings for 5 s. Always 200: `{"shown":true}`, or `{"shown":false,"reason":"no display"}` on a speaker without a supported display (`"busy"` while a game holds it). Shown even when the notices below are switched off. |
| `GET`  | `/api/box/display-notices` | - | `{"enabled":bool,"supported":bool}`: STR's short notices on the display (a saved key, an empty key, keys restored after a reboot). On by default; `supported` says whether this speaker's display can show them. |
| `POST` / `PUT` | `/api/box/display-notices` | `{"enabled":false}` | Switch the notices off (or on) for this speaker. Persisted in `/mnt/nv/streborn/display-notices`. |

## Examples

```bash
BOX=http://192.0.2.10:8888

# Turn on and set the volume to 20
curl -s -X POST "$BOX/api/box/power"  -d '{"on":true}'
curl -s -X PUT  "$BOX/api/box/volume" -d '{"value":20}'

# Read the current volume
curl -s "$BOX/api/box/volume"          # -> {"value":20,"target":20,"muted":false}

# Start preset 1, then pause
curl -s -X POST "$BOX/api/play/1"
curl -s -X POST "$BOX/api/pause"

# Give key 1 a shorter name
curl -s -X PATCH "$BOX/api/presets/1" -d '{"name":"Radio Paradise"}'

# Play an arbitrary internet radio stream
curl -s -X POST "$BOX/api/play" -d '{"url":"https://example.com/stream.mp3"}'

# Send to standby
curl -s -X POST "$BOX/api/box/power" -d '{"on":false}'
```

## Apple Shortcuts and Apple Watch

A shortcut in Apple's Shortcuts app is enough to control a speaker from the
iPhone, the home screen or an Apple Watch, with no extra app. A user shared
the setup in discussion #1224 (iOS 27, a first-generation SoundTouch 20).

Each shortcut is two actions:

1. **Text**, holding the request body.
2. **Get Contents of URL** (German: "Inhalte von URL abrufen"), with the
   method set to `POST` (`PUT` for the volume), the request body set to `Text` (or `JSON`), and the
   Text from step 1 as its content.

What to put in them:

| Shortcut | URL | Body |
|---|---|---|
| Play preset 1 | `http://<speaker-ip>:8888/api/play/1` | none |
| Power on / standby | `http://<speaker-ip>:8888/api/box/power` | `{"on":true}` / `{"on":false}` |
| Volume to 20 | `http://<speaker-ip>:8888/api/box/volume` | `{"value":20}` |
| Power button (toggles) | `http://<speaker-ip>:8090/key` | `<key state="press" sender="Gabbo">POWER</key>` |

The power-button variant goes to the speaker's own Bose API and behaves like
the button: it switches on when the speaker is off and off when it is on.
When you simulate a preset key that way (`PRESET_1` to `PRESET_6`), send a
second request with `state="release"` right after the `press`, or the speaker
reads it as a long press and stores the current station on that key. The same
goes for `VOLUME_UP` and `VOLUME_DOWN`, and there a missing release is louder:
the speaker holds the key down and keeps turning the volume up. The STR
routes `/api/play/<slot>` and `/api/box/volume` need no release at all.

Shortcuts made on the iPhone show up on a paired Apple Watch on their own.
Give the speaker a fixed address in your router, or the shortcuts stop working
when it gets a new one.

## The speaker's own Bose API still works

STR does not block the speaker's native Bose HTTP API on port **8090**, so
existing setups that call it directly keep working alongside STR:

- `POST http://<speaker-ip>:8090/key` with a `press`/`release` body for
  `POWER`, `PRESET_1`..`PRESET_6`, `PLAY_PAUSE`, `VOLUME_UP`, `VOLUME_DOWN`.
- `POST http://<speaker-ip>:8090/volume` with `<volume>NN</volume>`.

The STR API on `:8888` is the more stable and documented surface and adds
things the Bose cloud used to do (arbitrary stream URLs, the STR preset
store), so new integrations should prefer it.

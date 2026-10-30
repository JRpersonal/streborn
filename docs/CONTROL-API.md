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
| `GET`  | `/api/stream/title` | - | Live radio title of the stream STR is proxying: `{"title":"Artist - Title","swapped":bool}`. `title` is `""` when the station sends none. `swapped` says the station below is flagged and its title was already turned round. |
| `GET`  | `/api/stream/title-swap` | - | `{"stations":[...],"swapped":bool}`: the stream URLs flagged as sending their title "Title - Artist", and whether the station playing now is one of them. |
| `POST` | `/api/stream/title-swap` | `{"swap":true}` | Flag (or with `false` unflag) a station whose live title arrives as "Title - Artist" instead of "Artist - Title". Without `"station":"<stream url>"` it acts on the station playing now (`409` when nothing is). The flag is kept per station in `/mnt/nv/streborn/title-swap.json` and applies to everything that shows the title: the speaker display, the app, Recently played and the phone remote. Answers `{"ok":true,"station":"...","swapped":bool,"title":"<live title now>"}`. |

## Volume, power and source

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/box/volume` | - | Returns `{"value":N,"target":N,"muted":bool}` (0-100). |
| `PUT`  | `/api/box/volume` | `{"value":N}` | Set the absolute volume (0-100). |
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
| `POST` | `/api/box/save-playing` | `{"slot":N}` | Keep what the speaker plays on slot `N` (1-12): the playing music-library folder, the library song, a copy of the key that plays, or the station itself. Answers `{"slot","kind","name","unchanged"}`; `422` with a `code` when nothing that can be kept plays (Spotify and Pandora/iHeartRadio go through `PUT /api/presets/<slot>`), and the same `409 already-on-slot` as a save. |
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
`502` with the firmware's message when the speaker refuses (`navigate-failed`,
`select-failed`). To keep a station on a key, start it and then save the key
with `PUT /api/presets/<slot>` and `{"type":"native","name":"..."}`: the agent
takes the item from what the speaker plays.

## Favourites

The starred stations the desktop app keeps and the phone remote shows. The
desktop app owns the list: it pushes the whole list to every speaker whenever
it changes, and it learns a removal made elsewhere only from the speaker's
removal notes.

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`    | `/api/favorites` | - | The list: `[{"stationuuid","name","url","url_resolved","codec","bitrate","favicon","country","homepage","source","mime","durationSec"}]`. A favourite's key is `stationuuid`, else `name\|url`. |
| `DELETE` | `/api/favorites?id=<key>` | - | Remove one favourite. This is the call for a remote app. It writes a removal note, hands the removal on to every other STR speaker, and the desktop app takes the favourite off its own list within seconds. |
| `GET`    | `/api/favorites?removed=1` | - | The removal notes: `{"removed":[{"id","at"}]}`. |
| `PUT`    | `/api/favorites` | the whole list | The desktop app's sync. Do not use it to remove a favourite: a shorter list writes no removal note, so the desktop app puts the favourite back with its next push and the other speakers never hear of it. |

## Alarm clock

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/alarms` | - | The alarm document plus a read-only `status` block. |
| `PUT`  | `/api/alarms` | the whole document | Replaces it wholesale. LAN only. |

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

```bash
curl -s $BOX/api/alarms
curl -s -X PUT $BOX/api/alarms -H 'Content-Type: application/json' -d '{
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
| `GET`    | `/api/box/zone` | - | The live zone, plus `remembered` when none is live and `permanent` when the saved group is the durable kind. |
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

**`permanent` is not carried forward.** The document is stored exactly as sent,
so a POST that omits it turns a durable group into an ordinary one: it stops
re-forming when the master plays, and the next `DELETE` clears it for good. A
client that adds or removes ONE member must read `permanent` from
`GET /api/box/zone` first and send it back. (Found in a third-party client,
2026-09-14, where adding a speaker silently demoted the saved group.)

**A 200 is not a success.** The dissolve answers
`{"ok":false,"remaining":N,"error":"..."}` when the speaker took the request and
kept the group anyway, and `{"ok":true,"nothing":true}` when there was no group
to take apart. Both arrive as HTTP 200. Treating either as success is how an app
shows a green tick over a group that is still playing.

## Agent and history

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`    | `/api/agent/version` | - | `{"version","build"}`, plus `friendlyName` and `model` when the agent knows them, and `"presetRows":"2"` on an agent with the second preset row (slots 7-12). The cheapest "is this really an STR agent" probe: require `version` in the BODY, an open port and a 200 alone prove nothing. |
| `PUT`    | `/api/box/bass` | `{"value":N}` | Range as the speaker reports it; not every model has bass control. |
| `GET`    | `/api/recent` | - | Recently played, newest first: `ts`, `source`, `cardKey`, `cardName`, `cardArt`, `cardURL`, `track`, `account`, `homepage`. |
| `DELETE` | `/api/recent` | - | Clears the history. Answers `{"ok":true,"removed":N}`. |

## Wi-Fi

LAN-only, like every other call here, and refused from outside the private
address ranges.

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`    | `/api/box/wlan/networks` | - | The networks the speaker has stored (names in clear), the current one, `wired`, and two previews: `forgetOthers` (`removable`, `code`) and `forgetAll` (`{"allowed":bool,"code":"..."}`, see `DELETE /api/box/wlan`). |
| `PUT`    | `/api/box/wlan` | `{"ssid":"...","password":"..."}` | Move the speaker to another network. Optional `hidden`, `open`, `force`, `forgetOthers`. On a cabled speaker the network is only stored (`"status":"stored"`). |
| `DELETE` | `/api/box/wlan` | `{"reboot":false}` (optional) | Forget the Wi-Fi altogether, for a speaker that should run on its network cable only (#969). See below. |
| `POST`   | `/api/box/wlan/forget-others` | `{"reboot":false}` (optional) | Forget every stored network but the one the speaker is on. Wi-Fi speakers only. |

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

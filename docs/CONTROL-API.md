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
| `POST` | `/api/play` | `{"url":"https://...","title":"...","mime":"audio/mpeg"}` | Play any stream URL. `title`, `icon`, `mime` are optional. |
| `POST` | `/api/play/<slot>` | - | Play STR preset slot `1`-`6`. |
| `POST` | `/api/pause` | - | Pause. |
| `POST` | `/api/resume` | - | Resume. |
| `POST` | `/api/stop` | - | Stop. |
| `POST` | `/api/next` | - | Next track (within a folder / playlist). |
| `POST` | `/api/prev` | - | Previous track. |

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
| `GET`  | `/api/presets` | - | The STR preset store (what the six slots point at). |
| `POST` | `/api/box/preset-move` | `{"from":N,"to":M}` | Move the station on key `N` to key `M` (1-6), replacing what `M` held. Deliberately outside the `/api/presets/` prefix: an agent that predates it then answers a plain 404, which nothing else does. |
| `PATCH` | `/api/presets/<slot>` | `{"name":"..."}` | Rename key `1`-`6`. Only the name changes; the station, its artwork and a Spotify key's playlist stay as they are. Names longer than 64 characters are shortened, an empty one is refused (422), an empty key answers 404. |
| `GET`  | `/api/box/presets` | - | The hardware preset buttons as the box reports them. |
| `POST` | `/api/box/presets/recall` | `{"slot":N}` | Recall hardware preset `N` (1-6). |

`POST /api/play/<slot>` is usually what you want to start a preset from
automation; `presets/recall` mirrors a physical button press.

A station lives on one key at a time. Saving one that is already on another
key is refused with `409` and `{"code":"already-on-slot","slot":N,"name":"..."}`,
because clearing that other key without being asked is how presets got lost.
`POST /api/box/preset-move` is the explicit way to do it: it writes the new key
and frees the old one in a single store write.

## Alarm clock

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/alarms` | - | The alarm document plus a read-only `status` block. |
| `PUT`  | `/api/alarms` | the whole document | Replaces it wholesale. LAN only. |

An alarm is
`{"id","enabled","name","hour","minute","days","slot","volume","autoOff"}`:
`days` are weekdays with `0` = Sunday, `slot` is a preset 1-6, `volume` is
1-100 or `0` to leave the speaker's own level alone, and `autoOff` switches the
speaker off that many minutes after the alarm starts (1-720, or `0` for never).
The document also carries one IANA `zone` for the speaker; an empty zone means
UTC. At most 8 alarms, and an alarm with no days is rejected rather than treated
as "every day".

An alarm plays on its own speaker only. It does not re-form a permanent group,
and `autoOff` switches off only that speaker, not the group.

A rejected `PUT` answers `400` with the reason as plain text, meant to be shown
to the user as it stands. The `status` block reports `clockTrusted`,
`zoneResolved`, `nextFire`, `nextAlarmId` and `lastFire`; it is ignored on the
way in. `nextFire` comes from the speaker, so it is the honest answer to "when
will this actually go off", including its zone and its clock.

```bash
curl -s $BOX/api/alarms
curl -s -X PUT $BOX/api/alarms -H 'Content-Type: application/json' -d '{
  "zone":"Europe/Berlin",
  "alarms":[{"id":"weekdays","enabled":true,"hour":6,"minute":30,
             "days":[1,2,3,4,5],"slot":3,"volume":25,"autoOff":60}]}'
```

See [AUTOMATION.md](AUTOMATION.md) for what a fire actually does and how the
clock is handled.
## Groups

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`    | `/api/box/zone` | - | The live zone, plus `remembered` when none is live and `permanent` when the saved group is the durable kind. |
| `POST`   | `/api/box/zone` | the whole group | Forms or REPLACES the group. Send the full member list every time. |
| `DELETE` | `/api/box/zone` | - | Takes the live group apart. The saved group survives; add `?forget=1` to delete that too. |
| `GET`    | `/api/box/zone/volume` | - | `{"grouped","stereo","members":[...],"average"}`. A member that did not answer reports `volume: -1` rather than disappearing. |
| `POST`   | `/api/box/zone/volume` | `{"value":N}` or `{"ip":"...","value":N}` | Every member, or one of them. |

The form body is
`{"master":{"deviceID","ip"},"slaves":[{"deviceID","ip"}],"mode","permanent","defineOnly","stereo"}`.

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
| `GET`    | `/api/agent/version` | - | `{"version","build"}`, plus `friendlyName` and `model` when the agent knows them. The cheapest "is this really an STR agent" probe: require `version` in the BODY, an open port and a 200 alone prove nothing. |
| `PUT`    | `/api/box/bass` | `{"value":N}` | Range as the speaker reports it; not every model has bass control. |
| `GET`    | `/api/recent` | - | Recently played, newest first: `ts`, `source`, `cardKey`, `cardName`, `cardArt`, `cardURL`, `track`, `account`, `homepage`. |
| `DELETE` | `/api/recent` | - | Clears the history. Answers `{"ok":true,"removed":N}`. |

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

## The speaker's own Bose API still works

STR does not block the speaker's native Bose HTTP API on port **8090**, so
existing setups that call it directly keep working alongside STR:

- `POST http://<speaker-ip>:8090/key` with a `press`/`release` body for
  `POWER`, `PRESET_1`..`PRESET_6`, `PLAY_PAUSE`, `VOLUME_UP`, `VOLUME_DOWN`.
- `POST http://<speaker-ip>:8090/volume` with `<volume>NN</volume>`.

The STR API on `:8888` is the more stable and documented surface and adds
things the Bose cloud used to do (arbitrary stream URLs, the STR preset
store), so new integrations should prefer it.

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
| `GET`  | `/api/box/presets` | - | The hardware preset buttons as the box reports them. |
| `POST` | `/api/box/presets/recall` | `{"slot":N}` | Recall hardware preset `N` (1-6). |

`POST /api/play/<slot>` is usually what you want to start a preset from
automation; `presets/recall` mirrors a physical button press.

## Alarm clock

| Method | Path | Body | Notes |
| ------ | ---- | ---- | ----- |
| `GET`  | `/api/alarms` | - | The alarm document plus a read-only `status` block. |
| `PUT`  | `/api/alarms` | the whole document | Replaces it wholesale. LAN only. |

An alarm is `{"id","enabled","name","hour","minute","days","slot","volume"}`:
`days` are weekdays with `0` = Sunday, `slot` is a preset 1-6, and `volume` is
1-100 or `0` to leave the speaker's own level alone. The document also carries
one IANA `zone` for the speaker; an empty zone means UTC. At most 8 alarms, and
an alarm with no days is rejected rather than treated as "every day".

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
             "days":[1,2,3,4,5],"slot":3,"volume":25}]}'
```

See [AUTOMATION.md](AUTOMATION.md) for what a fire actually does and how the
clock is handled.

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

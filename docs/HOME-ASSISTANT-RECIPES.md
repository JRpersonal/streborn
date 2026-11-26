# Home Assistant recipes for STR speakers

Copy-and-paste configuration for a Home Assistant (HA) instance that drives STR
speakers through STR's local REST API ([`CONTROL-API.md`](./CONTROL-API.md)).
No custom component and no HACS are needed, everything below is plain YAML plus
two small Python scripts.

[`HOME-ASSISTANT.md`](./HOME-ASSISTANT.md) explains which local surfaces exist.
This page is the worked setup on top of it:

1. a sensor that knows every speaker, its presets and what is playing,
2. one dropdown per speaker to start a preset or switch the speaker off,
3. the same presets as switches in Apple Home (through HA's HomeKit Bridge),
4. spoken announcements that interrupt the music and resume it, with a voice
   that is generated locally, so no text leaves your network.

Tested with Home Assistant 2026.11 in Docker and STR 1.9.x on six speakers
(SoundTouch 10, 30 and Portable). Announcements were verified on a SoundTouch
Portable. Two parts need an agent newer than 1.9.2: the now-playing JSON
(`GET /api/nowplaying`) and the clip length in the announcement's answer
(`durationMs`). Both recipes fall back to the older behaviour on an older agent.
Replace the example addresses `192.0.2.x` with your own.

## Before you start

- **Give every speaker a fixed IP address** in your router. The recipes address
  speakers by IP.
- **The API port differs per model.** On many speakers the firmware keeps
  `:8888` closed to the network, and STR makes the same API reachable on
  `:17008` instead (seen on SoundTouch 20, 30 and Portable); other speakers
  answer on `:8888` directly. Try `:17008` first, then `:8888`. The sensor
  script below does exactly that and remembers the port that answered, so
  nothing else has to care. Details in
  [`CONTROL-API.md`](./CONTROL-API.md#which-port).
- **Traffic flows both ways.** HA calls the speaker, and for an announcement the
  speaker's agent fetches the audio clip from HA. HA must therefore be reachable
  from the speaker's network over plain `http://` on its LAN address.
- **`command_line` and `rest_command` need one HA restart** the first time you
  add them. A YAML reload is not enough for a platform that was never loaded.

## 1. The speaker sensor

Save this as `/config/str_speakers.py` and enter your speakers. The keys
(`living_room`, ...) become the names used everywhere else, so keep them
lowercase without spaces.

```python
#!/usr/bin/env python3
"""State and presets of STR speakers for a Home Assistant command_line sensor.

Output: {"reachable": 2, "speakers": {"living_room": {"address": "ip:port",
         "playing": "Radio One", "presets": {"1": "Radio One", ...}}, ...}}
"playing" is "Off" (standby), the name of the playing preset, or "" (something else).
"""
import json
import re
import urllib.request

SPEAKERS = {
    "living_room": "192.0.2.21",
    "kitchen": "192.0.2.22",
}
PORTS = (17008, 8888)


def get(address, path):
    with urllib.request.urlopen("http://%s%s" % (address, path), timeout=3) as response:
        return response.read().decode("utf-8", "replace")


def speaker(ip):
    for port in PORTS:
        address = "%s:%d" % (ip, port)
        try:
            raw = json.loads(get(address, "/api/presets"))
        except Exception:
            continue
        if not isinstance(raw, list):
            continue
        presets = {str(p["slot"]): str(p.get("name") or "Preset %s" % p["slot"]).strip()
                   for p in sorted(raw, key=lambda p: p.get("slot", 0)) if p.get("slot")}
        playing = ""
        try:
            # JSON with the key that plays (agents newer than 1.9.2).
            now = json.loads(get(address, "/api/nowplaying"))
            if now.get("source") == "STANDBY":
                playing = "Off"
            elif str(now.get("presetSlot", "")) in presets:
                playing = presets[str(now["presetSlot"])]
        except Exception:
            # An older agent: read the speaker's XML and match the preset by name.
            try:
                status = get(address, "/api/status")
                source = re.search(r'<nowPlaying[^>]*source="([^"]*)"', status)
                name = re.search(r"<itemName>([^<]*)</itemName>", status)
                if source and source.group(1) == "STANDBY":
                    playing = "Off"
                elif name and name.group(1).strip() in presets.values():
                    playing = name.group(1).strip()
            except Exception:
                pass
        return {"address": address, "playing": playing, "presets": presets}
    return None


speakers = {}
for key, ip in SPEAKERS.items():
    data = speaker(ip)
    if data:
        speakers[key] = data
print(json.dumps({"reachable": len(speakers), "speakers": speakers}))
```

Notes on what it relies on:

- `GET /api/presets` returns a JSON list; each entry has `slot` (1-12) and
  `name`. Empty slots are simply missing.
- `GET /api/nowplaying` returns what plays as JSON, among others `source`
  (`STANDBY` when the speaker is off), `title`, `artist` and `presetSlot`, the
  STR key that holds what plays. A field the speaker does not report is left
  out. An agent that has it reports `"nowPlayingJSON":"true"` in
  `GET /api/agent/version`.
- An older agent answers that call with an error, and the script falls back to
  `GET /api/status`, the speaker's now-playing **XML**. It has no preset slot,
  so the playing preset is found by comparing `itemName` with the preset names,
  and two presets with the same name cannot be told apart.
- A speaker that does not answer is left out. The sensor state is the number of
  speakers that answered.

`configuration.yaml`:

```yaml
command_line:
  - sensor:
      name: STR speakers
      unique_id: str_speakers
      command: "python3 /config/str_speakers.py"
      scan_interval: 60
      command_timeout: 50
      value_template: "{{ value_json.reachable }}"
      json_attributes:
        - speakers
```

After the restart, `sensor.str_speakers` carries everything in its `speakers`
attribute.

## 2. Commands

`configuration.yaml`. `address` is the `ip:port` the sensor found.

```yaml
rest_command:
  str_play_preset:
    url: "http://{{ address }}/api/play/{{ slot }}"
    method: post
    timeout: 20
  str_power_off:
    url: "http://{{ address }}/api/box/power"
    method: post
    content_type: "application/json"
    payload: '{"on": false}'
    timeout: 20
  # The speaker fetches the clip itself, interrupts the music and resumes it.
  str_announce:
    url: "http://{{ address }}/api/announce"
    method: post
    content_type: "application/json"
    payload: >-
      {"url": {{ url | tojson }}, "volume": {{ volume | int }}}
    timeout: 30
```

Use `/api/play/<slot>` for presets, not `/api/box/presets/recall`: the latter
presses the hardware key and only reaches slots 1-6.

## 3. A preset dropdown per speaker

`scripts.yaml`:

```yaml
str_preset:
  alias: STR play preset
  description: Plays the preset with this name on a speaker; "Off" sends it to standby.
  mode: parallel
  fields:
    speaker:
      description: Key of the speaker, as in str_speakers.py
    name:
      description: Preset name as stored on the speaker, or Off
  sequence:
    - variables:
        data: "{{ (state_attr('sensor.str_speakers', 'speakers') or {}).get(speaker) or {} }}"
        slot: "{{ (data.get('presets') or {}).items() | selectattr(1, 'eq', name) | map(attribute=0) | first | default('') }}"
    - choose:
        - conditions: "{{ name == 'Off' and data.get('address') is not none }}"
          sequence:
            - action: rest_command.str_power_off
              data:
                address: "{{ data.address }}"
        - conditions: "{{ slot != '' }}"
          sequence:
            - action: rest_command.str_play_preset
              data:
                address: "{{ data.address }}"
                slot: "{{ slot }}"
    - delay: "00:00:04"
    - action: homeassistant.update_entity
      target:
        entity_id: sensor.str_speakers
```

`configuration.yaml`, one block per speaker (replace `living_room` four times):

```yaml
template:
  - select:
      - name: Music living room
        unique_id: music_living_room
        icon: mdi:radio
        state: >-
          {{ ((state_attr('sensor.str_speakers', 'speakers') or {}).get('living_room') or {}).get('playing') or 'Other' }}
        options: >-
          {{ ['Off'] + (((state_attr('sensor.str_speakers', 'speakers') or {}).get('living_room') or {}).get('presets') or {}).values() | list + ['Other'] }}
        availability: "{{ 'living_room' in (state_attr('sensor.str_speakers', 'speakers') or {}) }}"
        select_option:
          - action: script.str_preset
            data:
              speaker: living_room
              name: "{{ option }}"
```

The dropdown lists `Off`, the speaker's presets by name, and `Other` (shown
while Bluetooth, AUX, Spotify or anything that is not a preset plays; choosing
it does nothing). Renaming or moving a preset in STR shows up in HA within a
minute, without touching the configuration.

## 4. Presets in Apple Home

HA's **HomeKit Bridge** integration exposes a `select` entity as one accessory
with a switch per option. Add the `select.music_*` entities to the bridge
(Settings, Devices & services, HomeKit Bridge, Configure, include the entities)
and each speaker appears in Apple Home as a group of switches: one per preset,
plus `Off`. They work in scenes, automations and with Siri.

The bridge reads the option list only when it loads. Reload it when presets
change or a speaker comes back, otherwise Apple Home keeps the old switches:

```yaml
- id: str_homekit_refresh
  alias: "STR: refresh presets in Apple Home"
  mode: single
  max_exceeded: silent
  triggers:
    - trigger: state
      entity_id:
        - select.music_living_room
        - select.music_kitchen
      attribute: options
      for: "00:02:00"
    - trigger: state
      entity_id:
        - select.music_living_room
        - select.music_kitchen
      from: unavailable
      for: "00:02:00"
  actions:
    - action: homekit.reload
```

A speaker that was unreachable when the bridge loaded has no accessory until
this reload ran.

## 5. Announcements

`POST /api/announce` takes either `text` or `url`, plus an optional `volume`
(1-100):

| Field | Meaning |
|---|---|
| `text` + `lang` | STR turns the text into speech with Google Translate TTS. Needs no setup, but the text goes to Google and the voice is robotic. Practical limit about 200 characters. |
| `url` | Any audio file the speaker's agent can fetch (`http` or `https`, at most 8 MB). MP3 is best: STR then adds the silent lead-in that keeps the first word from being cut off. |
| `volume` | Volume for the clip. The previous volume is restored afterwards. |

The call answers as soon as the speaker's agent has fetched the clip, with
`{"ok":true,"queued":true,"bytes":N,"durationMs":N}`; it does not wait for the
clip to finish. `durationMs` is how long the clip plays, its short silent
lead-in included. It is missing when the length cannot be told (a clip that is
not MP3, or an agent from 1.9.2 or older). What was playing resumes afterwards,
and a speaker in standby is woken for the clip and sent back to standby.

The recipes here only use `url`, with speech generated inside your network.

### 5.1 A local voice: Piper

[Piper](https://github.com/rhasspy/piper) is a local neural text-to-speech
engine. On Home Assistant OS install the **Piper** add-on. With Docker, add the
container:

```yaml
services:
  piper:
    image: rhasspy/wyoming-piper:latest
    container_name: piper
    restart: unless-stopped
    command: --voice en_US-lessac-medium
    ports:
      - "127.0.0.1:10200:10200"   # use "10200:10200" if HA runs on another host
    volumes:
      - ./piper:/data
```

The voice model is downloaded on first start. Voices and samples:
<https://rhasspy.github.io/piper-samples/>. Then in HA add the **Wyoming
Protocol** integration with the host and port `10200`. It creates `tts.piper`.

Choosing the quality level matters on small hardware. Measured on an Intel
Celeron N3150 (a 2015 NAS CPU) with the German `thorsten` voice:

| Voice | Time to generate | For speech of |
|---|---|---|
| `medium` | 2.4 s | 3.7 s |
| `high` | 20 s | 4.2 s |

So run `medium` as the live voice and use `high` only for clips rendered in
advance (5.3).

### 5.2 Speak any text

HA can render a text to an MP3 and hand back its address. Create a long-lived
access token (your HA profile, Security) and store it in `secrets.yaml`:

```yaml
ha_api_bearer: "Bearer <your long-lived token>"
```

`configuration.yaml`, in the `rest_command:` block from step 2:

```yaml
  ha_tts_url:
    url: "http://localhost:8123/api/tts_get_url"
    method: post
    content_type: "application/json"
    headers:
      authorization: !secret ha_api_bearer
    payload: >-
      {"engine_id": "tts.piper", "message": {{ text | tojson }}, "options": {"preferred_format": "mp3"}}
    timeout: 60
```

The answer contains `path` (`/api/tts_proxy/<random>.mp3`) and `url`. The
script below builds the address from `path` and HA's LAN address, which works
even when HA's internal URL setting is empty or points at a name the speaker
cannot resolve.

`scripts.yaml` (replace `192.0.2.10:8123` with your HA's LAN address):

```yaml
announce:
  alias: Announce
  description: >-
    Speaks on STR speakers. Either a clip rendered in advance (field clip) or
    any text (field text, generated locally). Only between 7:00 and 22:00
    unless at_night is true.
  icon: mdi:bullhorn
  mode: queued
  max: 10
  fields:
    clip:
      description: Name of a prepared clip, e.g. dishwasher_done
    text:
      description: Free text, when there is no prepared clip
    speakers:
      description: List of speaker keys
    volume:
      description: 1-100, default 30
    at_night:
      description: true = also between 22:00 and 7:00
  sequence:
    - condition: template
      value_template: "{{ at_night | default(false) or 7 <= now().hour < 22 }}"
    - choose:
        - conditions: "{{ clip is defined and clip }}"
          sequence:
            - action: script.announce_play
              data:
                url: "http://192.0.2.10:8123/local/announcements/{{ clip }}.mp3"
                speakers: "{{ speakers | default(['living_room']) }}"
                volume: "{{ volume | default(30) }}"
        - conditions: "{{ text is defined and text }}"
          sequence:
            - action: rest_command.ha_tts_url
              data:
                text: "{{ text }}"
              response_variable: answer
            - condition: template
              value_template: "{{ answer.status == 200 and answer.content.path is defined }}"
            - action: script.announce_play
              data:
                url: "http://192.0.2.10:8123{{ answer.content.path }}"
                speakers: "{{ speakers | default(['living_room']) }}"
                volume: "{{ volume | default(30) }}"

announce_play:
  alias: Announce (play clip)
  mode: queued
  max: 10
  fields:
    url:
      description: Address of the MP3, reachable from the speaker
    speakers:
      description: List of speaker keys
    volume:
      description: 1-100
  sequence:
    - repeat:
        for_each: "{{ speakers }}"
        sequence:
          - variables:
              address: "{{ ((state_attr('sensor.str_speakers', 'speakers') or {}).get(repeat.item) or {}).get('address') }}"
          - if:
              - condition: template
                value_template: "{{ address not in (none, '', 'None') }}"
            then:
              - action: rest_command.str_announce
                continue_on_error: true
                response_variable: answer
                data:
                  address: "{{ address }}"
                  url: "{{ url }}"
                  volume: "{{ volume }}"
              # Wait until this speaker is done before the next one starts:
              # the clip's length plus 2 s for the resume, 8 s when the answer
              # carries no durationMs, nothing when the call failed.
              - delay:
                  milliseconds: >-
                    {% if answer is defined and answer.status == 200 %}{{ ((answer.content.durationMs | default(8000)) | int) + 2000 }}{% else %}0{% endif %}
```

The speakers speak one after the other, each once the previous one is done, so
the same sentence does not overlap from two rooms. A speaker that is offline is
skipped without failing the script. Add your own
presence check to the first condition if announcements should stay silent while
nobody is home.

Use it anywhere:

```yaml
- action: script.announce
  data:
    text: "The washing machine is done."
    speakers: [kitchen, living_room]
```

### 5.3 Clips rendered in advance

For sentences that never change, render them once with the best voice and let
the speaker fetch the finished file. That is instant, sounds better than the
live voice on slow hardware, and keeps working while Piper is down. Files in
`/config/www/` are served by HA at `/local/` without a login.

`/config/announcements.json`, name and sentence:

```json
{
  "dishwasher_done": "The dishwasher is done.",
  "washer_done": "The washing machine is done.",
  "window_open_rain": "It is starting to rain. Please close the windows.",
  "bathroom_air": "Someone took a shower. Please open the bathroom window.",
  "weather_warning": "Attention, there is a weather warning for today.",
  "doorbell": "Someone is at the door.",
  "good_night": "Good night. I am switching everything off."
}
```

`/config/announcements_render.py` talks to a Piper instance directly (Wyoming
protocol) and converts the result to MP3 with `ffmpeg`. It only renders what is
new or changed and removes clips whose entry is gone.

```python
#!/usr/bin/env python3
"""Render announcements.json to MP3 clips in www/announcements/ with Piper.

  python3 announcements_render.py [port]     # Wyoming port of Piper, default 10200

FFMPEG may name another ffmpeg command. When the script runs on a Docker host
without ffmpeg, use the one inside the HA container:
  FFMPEG="docker exec homeassistant ffmpeg" CONFIG_IN_CONTAINER=/config python3 ...
"""
import hashlib
import json
import os
import shlex
import socket
import subprocess
import sys
import wave

HERE = os.path.dirname(os.path.abspath(__file__))
TARGET = os.path.join(HERE, "www", "announcements")
PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 10200
FFMPEG = shlex.split(os.environ.get("FFMPEG", "ffmpeg"))
INSIDE = os.environ.get("CONFIG_IN_CONTAINER")  # path of HERE as ffmpeg sees it


def speak(text, mp3):
    s = socket.create_connection(("127.0.0.1", PORT), timeout=600)
    f = s.makefile("rwb")
    f.write((json.dumps({"type": "synthesize", "data": {"text": text}}) + "\n").encode())
    f.flush()
    pcm, fmt = b"", None
    while True:
        line = f.readline()
        if not line:
            break
        head = json.loads(line)
        data = head.get("data") or {}
        if head.get("data_length"):
            data = json.loads(f.read(head["data_length"]))
        payload = f.read(head["payload_length"]) if head.get("payload_length") else b""
        if head["type"] == "audio-start":
            fmt = data
        elif head["type"] == "audio-chunk":
            pcm += payload
            fmt = fmt or data
        elif head["type"] == "audio-stop":
            break
    s.close()
    wav = mp3[:-4] + ".wav"
    with wave.open(wav, "wb") as w:
        w.setnchannels(fmt["channels"])
        w.setsampwidth(fmt["width"])
        w.setframerate(fmt["rate"])
        w.writeframes(pcm)
    seen = INSIDE + wav[len(HERE):] if INSIDE else wav
    subprocess.run(FFMPEG + ["-y", "-loglevel", "error", "-i", seen, "-ar", "44100",
                             "-ac", "1", "-b:a", "128k", seen[:-4] + ".mp3"], check=True)
    os.remove(wav)


os.makedirs(TARGET, exist_ok=True)
texts = json.load(open(os.path.join(HERE, "announcements.json"), encoding="utf-8"))
memo = os.path.join(TARGET, "index.json")
try:
    index = json.load(open(memo, encoding="utf-8"))
except (IOError, ValueError):
    index = {}
for name, text in texts.items():
    mark = hashlib.sha1(("%s|%s" % (PORT, text)).encode()).hexdigest()[:12]
    mp3 = os.path.join(TARGET, name + ".mp3")
    if index.get(name, {}).get("mark") == mark and os.path.exists(mp3):
        continue
    speak(text, mp3)
    index[name] = {"text": text, "mark": mark}
    json.dump(index, open(memo, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print(name, flush=True)
for name in [n for n in index if n not in texts]:
    try:
        os.remove(os.path.join(TARGET, name + ".mp3"))
    except OSError:
        pass
    del index[name]
json.dump(index, open(memo, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
```

To render with the `high` voice without slowing the live one, start a second,
temporary Piper on another port, render, and remove it:

```sh
docker run -d --name piper-high -p 127.0.0.1:10201:10200 \
    -v "$PWD/piper-high:/data" rhasspy/wyoming-piper:latest --voice en_US-lessac-high
python3 announcements_render.py 10201
docker rm -f piper-high
```

Then:

```yaml
- action: script.announce
  data:
    clip: dishwasher_done
    speakers: [kitchen]
```

Writing tips for the sentences: spell out numbers and times ("ten p.m."), keep
one thought per sentence, and start warnings with a short word such as
"Attention," so the listener is tuned in before the content comes.

## 6. Example automation

```yaml
- id: announce_weather_warning
  alias: Announce weather warning
  triggers:
    - trigger: numeric_state
      entity_id: sensor.weather_warning_level
      above: 1
  actions:
    - action: script.announce
      data:
        clip: weather_warning
        speakers: [living_room, kitchen]
```

## Things that will trip you up

- **Connection refused on `:17008` or `:8888`.** The speaker answers on the
  other one. Take the address from the sensor instead of hard-coding a port.
- **Nothing is spoken, the call returns 502.** The agent could not fetch the
  clip. Open the clip URL from another device on the speaker's network; it must
  load without a login. `https://` with a self-signed certificate and names
  that only HA's own DNS knows are the usual causes.
- **The first word is cut off.** The clip is not MP3. Convert it, STR then adds
  its silent lead-in.
- **The speakers speak one after the other, not together.** That is what
  `announce_play` does on purpose, it waits for each clip to end. For one
  house-wide announcement at the same moment, group the speakers first and
  announce on the main speaker.
- **The next speaker starts too early or waits too long.** The wait comes from
  `durationMs`. Without it (a clip that is not MP3, or an agent from 1.9.2 or
  older) the script waits a fixed 10 s. Convert clips to MP3 to get the exact
  length.
- **Apple Home shows old presets.** Reload the HomeKit Bridge (step 4).
- **The dropdown shows `Other` although a preset plays.** The speaker reports a
  different `itemName` than the preset's name, typically after renaming the
  preset in STR while it was playing. Start the preset again.
- **`/local/` files are public inside your LAN.** Do not put anything private
  into an announcement clip name or sentence that guests on your network should
  not be able to fetch.

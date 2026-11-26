# Home Assistant and STR

STR does not ship its own Home Assistant custom component, and it does not need
one. Once STR is installed, the speaker keeps the two local surfaces a home hub
already speaks:

- the native Bose SoundTouch HTTP API on port **8090** (control), and
- the UPnP AV media renderer on port **8091** (audio in).

Both stay reachable on your LAN on every model (SoundTouch 10, 20, 30 and
Portable). What the Bose cloud shutdown broke for HA-side integrations was the
account and discovery layer, not this local control, and the local control is
exactly what STR restores. So a Home Assistant setup drives an STR speaker the
same way it did before the shutdown.

## Control the speaker from Home Assistant

Two ways, use whichever fits your setup:

- **A local-API SoundTouch integration.** Home Assistant ships a Bose SoundTouch
  integration, and the community `soundtouchplus` component
  (`thlucas1/homeassistantcomponent_soundtouchplus`) extends it. Both talk to the
  speaker's own `:8090` API, which STR keeps alive, so they control an STR box
  for source, transport, volume and presets.
- **STR's own local REST API.** STR exposes a small HTTP API (`rest_command` in
  HA, or any script) for playback, volume, power, source and presets. The full
  endpoint list is in [`CONTROL-API.md`](./CONTROL-API.md).

The addresses a hub needs, and an Alexa or Google voice path on top, are in
[`ALEXA.md`](./ALEXA.md) (its "Home Assistant" section covers plain control too,
not only voice).

## Send audio, music or TTS to the speaker

The speaker's UPnP renderer on `:8091/AVTransport/Control` is the path STR itself
plays through. Home Assistant's **DLNA Digital Media Renderer** integration
points straight at it and can push an audio URL or a TTS announcement to the
speaker. The exact SOAP calls, the required DIDL-Lite metadata, STR's one-line
API for the same, and the caveats (HTTP only, no seek) are in
[`AUTOMATION.md`](./AUTOMATION.md).

The old Bose notification endpoint (`POST :8090/speaker`) is dead since the
shutdown; use the UPnP path above, or STR's announcement below, instead.

## Recipes for STR's own API

The examples below use `192.0.2.10` and `192.0.2.11` as speaker addresses;
put in yours (the ST Reborn app shows them in the speaker list).

### Which port

STR's API answers on `:17008` on some speakers and on `:8888` on others,
depending on the chassis (the Portable and some ST20 and ST30 only let
`:17008` through). Try `:17008` first, then `:8888`, and keep the one where
`GET /api/agent/version` answers with a `version` in its body. Details in
[`CONTROL-API.md`](./CONTROL-API.md#which-port).

### What is playing, as a sensor

`GET /api/nowplaying` answers with JSON (`/api/status` is the speaker's own XML
and is meant for the STR apps). A RESTful sensor turns it into a state plus
attributes:

```yaml
rest:
  - resource: "http://192.0.2.10:17008/api/nowplaying"
    scan_interval: 10
    sensor:
      - name: "Kitchen speaker"
        value_template: "{{ value_json.playStatus | default('unknown') }}"
        json_attributes:
          - source
          - playStatus
          - title
          - artist
          - album
          - station
          - art
          - presetSlot
          - muted
```

`presetSlot` is the STR key (1 to 12) that plays, `title` is the track or,
without one, the station's name. A field the speaker does not report is simply
missing, so the attribute is absent rather than empty. The agent caches the
answer for 2 seconds and shares it with every other client, so a poll costs the
speaker at most one read per 2 seconds; there is still no reason to poll faster
than every few seconds. An agent that has this endpoint reports
`"nowPlayingJSON":"true"` in `GET /api/agent/version`.

### Announcements, one speaker after the other

`POST /api/announce` speaks a text (or plays a clip), then puts back whatever
was playing. It answers as soon as the audio is fetched, before the clip has
played, with `{"ok":true,"queued":true,"bytes":N,"durationMs":N}`.
`durationMs` is how long the clip plays. It is missing when the length cannot
be told (a clip that is not MP3, or an older agent), so give the wait a
fallback:

```yaml
rest_command:
  str_announce:
    url: "http://{{ host }}/api/announce"
    method: post
    content_type: "application/json"
    payload: "{{ body | to_json }}"

script:
  announce_around_the_house:
    fields:
      message:
        description: "What to say"
    sequence:
      - repeat:
          for_each:
            - "192.0.2.10:17008"
            - "192.0.2.11:8888"
          sequence:
            - action: rest_command.str_announce
              data:
                host: "{{ repeat.item }}"
                body:
                  text: "{{ message }}"
                  lang: "en"
                  volume: 30
              response_variable: answer
            # The clip's length plus 2 s for buffering and the resume;
            # 8 s when the answer carries no durationMs.
            - delay:
                milliseconds: "{{ (answer.content.durationMs | default(8000)) | int + 2000 }}"
```

The text goes to Google's TTS to be spoken (see
[`announce-tts.md`](./announce-tts.md)). To keep it in your house, or to use a
voice of your own, let a local TTS render the clip, for example Piper in Home
Assistant, and send its address as `url` instead of `text`:

```yaml
                body:
                  url: "http://192.0.2.5:8123/path/to/clip.mp3"
                  volume: 30
```

The agent fetches that file itself (http, or https with a valid certificate; at
most 8 MB), so it must be reachable from the speaker. An MP3 clip gets a
`durationMs`; another format (WAV) plays fine but without one, and the
fallback delay applies. The full request reference is the `/api/announce` row
in [`CONTROL-API.md`](./CONTROL-API.md#announcements).

## The other direction: a speaker press driving Home Assistant

A remote-control key (Back, Forward, Thumbs up, Thumbs down, Play/Pause, each
its own trigger), the power button, a preset or an AUX change can fire an
outgoing HTTP webhook, a UDP packet, or Wake-on-LAN, so a button on the speaker
can trigger a Home Assistant scene. See the smart-home triggers in
[`AUTOMATION.md`](./AUTOMATION.md) and the app's automation settings.

## A note on certainty

These are the local surfaces and the integrations that target them; STR keeps
them working, and the recipes above are what STR uses internally. I have not
certified every Home Assistant integration version end to end, so if you wire up
`soundtouchplus`, the core SoundTouch integration or the DLNA renderer against an
STR box, please report what worked in a GitHub discussion. The same goes for the
YAML recipes: the STR endpoints behind them are tested, the Home Assistant side
may need a tweak for your version. Others are asking the
same thing, and confirmed setups make this page better for the next person.

# Announcements / TTS (#125)

STR can play a short spoken announcement on a speaker, interrupting whatever is
playing and resuming it afterwards (the cloud-free replacement for the firmware's
`/speaker` notification endpoint, which is dead post-shutdown). It is exposed both
in the desktop app (Speaker Settings, the **Announcements** section, one of the
advanced options carrying the "Playground" badge) and as an HTTP endpoint for
home automation.

## How it works

```
desktop app / curl ──POST /api/announce {text|url, lang, volume}──▶ stick agent
   (via boxDo: self-heals :8888 <-> :17008, BCO boxes only answer :17008)
        agent: fetch TTS audio ──▶ snapshot now-playing + volume
                                ──▶ stop the current source
                                ──▶ load the clip, set the volume, play
                                ──▶ wait for it to finish, then resume
```

Order and resume rules (`runAnnouncement`, #1359):

- **Stop first, then volume, then play.** The running source is silenced
  before the announcement volume is set, so it never plays at that volume: a
  UPnP stream gets a transport Stop, a native radio station
  (`LOCAL_INTERNET_RADIO`) goes through the same native-station handover every
  other play uses (the speaker's own STOP key, then a check that it let go),
  Bluetooth/AUX get the STOP key. A clip push refused in the wrong transport
  state is cleared and retried once. If the clip does not start, the volume and
  the previous source are restored.
- **Resume only what was playing.** After the clip the transport is stopped and
  the volume restored, then the previous source comes back only if it was in
  `PLAY_STATE` or `BUFFERING_STATE`:
  - native radio station: the speaker's own `/select` with the
    `/station?data=...` item it reported (the same call the app's preset recall
    uses); if the speaker refuses, the stream that descriptor names is pushed
    over UPnP;
  - UPnP stream: pushed again with the wrong-state repair, using the art and
    mime of the last play when it is the same stream;
  - anything else (Bluetooth, AUX, other speaker-owned sources): left alone.
- A speaker that was in standby is woken for the clip and put back to standby.

Silent lead-in (#826): the speaker opens its amplifier a few hundred
milliseconds after a UPnP clip starts (it did the same with Bose's cloud-era
notifications), which swallowed the first word. The agent therefore puts
~800 ms of silent MP3 frames in front of the clip (`withSilentLeadIn`,
`internal/webui/announce_leadin.go`, `announceLeadIn`). The frames copy the
clip's own first frame header (version, sample rate, channel mode, bitrate)
with all-zero side info, so they decode to digital silence and the stream stays
one uniform MP3; a leading ID3v2 tag stays first. This covers TTS text and MP3
`url` clips. A `url` clip that is not recognisably MP3 (Ogg, WAV, a single stray
sync word) is served unchanged. Padding the text with unspoken characters is no
longer needed.

- Endpoint: `internal/webui/announce.go` (`handleAnnounce`, `handleAnnounceAudio`).
- Desktop call: `desktop-app/announce.go` (`SendAnnounce`), routed through `boxDo`
  so it self-heals across `:8888` / `:17008` like every other box call. A raw POST
  pinned to `:8888` fails on BCO/Portable speakers (only the REDIRECTed `:17008`
  answers) — see the `:8888`/`:17008` note in `docs/ARCHITECTURE.md`.

## The HTTP answer and how long the clip plays

The full request reference is the `/api/announce` row in
[`CONTROL-API.md`](CONTROL-API.md#announcements). The agent fetches the audio
first, so a bad URL or a TTS failure is still reported (`502`), then answers at
once and plays the announcement in the background:

```json
{"ok":true,"queued":true,"bytes":16800,"durationMs":4200}
```

`durationMs` is how long the clip plays, the silent lead-in included. The
agent reads it off the MP3 frame headers of the audio it already holds
(`internal/webui/announce_duration.go`, `mp3DurationMs`): each Layer III frame
decodes to a fixed number of samples at the rate its header names, so the
frames are walked header to header and summed. TTS text arrives as several MP3
chunks back to back; the walk passes from one to the next and over any ID3 tag
between them, so the answer is their sum. The walk reads headers only, never
audio, and is capped for the speaker's weak CPU (past 32768 frames the rest is
extrapolated, and junk between frames is searched through for at most 64 KB in
total). A clip that is not mostly MP3 frames (Ogg, WAV, anything else) has no
`durationMs`: the field is left out rather than guessed. The same length drives
the agent's own wait before it resumes what was playing.

An automation that wants to wait for the end waits `durationMs` plus a couple
of seconds for the speaker's buffering and the resume.

## TTS engine: Google Translate TTS

Speech is synthesised by **Google Translate's keyless TTS** endpoint
(`https://translate.google.com/translate_tts`, `internal/webui/announce.go`
`googleTTSURL`). It needs no API key.

- **Language**: the `lang` field maps to Google's `tl` parameter (default `en`).
  The app offers a voice-language dropdown next to the announcement field.
- **Length**: Google TTS has a practical ~200-character per-request limit. The
  agent chunks longer text at `ttsChunkLimit` (180) and concatenates the audio;
  the app input is capped at 200 characters.

## A voice of your own: a local TTS through `url`

The agent has no setting for another TTS engine, and does not need one. Any
TTS that can produce an audio file can drive an announcement: render the clip
on a machine in your LAN (for example with [Piper](https://github.com/rhasspy/piper),
or the TTS of Home Assistant), make it reachable over `http`, and pass its
address as `url` instead of `text`:

```bash
curl -s -X POST "http://192.0.2.10:8888/api/announce" \
     -d '{"url":"http://192.0.2.5:5000/clips/dinner.mp3","volume":30}'
```

The agent fetches the file itself (at most 8 MB) and plays it the same way as
TTS text, with the same interrupt and resume. The text then never leaves your
LAN. MP3 gets the silent lead-in and a `durationMs` in the answer; another
format (WAV, Ogg) plays unchanged, without either.

## Privacy

The announcement **text is sent to Google** to generate the audio. Nothing else
is shared (no account, no identifiers). This is disclosed in the app right under
the announcement field (`settingsView.announcePrivacy`). Users who want gap-free
privacy simply do not use announcements.

## Legal / licensing

- The Google Translate TTS endpoint is **unofficial and undocumented**: it is a
  ToS gray area and may rate-limit or change without notice. For a free,
  non-commercial community tool the risk is low, but it is disclosed here and in
  the UI rather than hidden.
- There is **no bundling/redistribution** concern: the audio is fetched per use
  and never shipped with STR, so no third-party audio is redistributed.
- A fully private option exists today without anything on the speaker: a
  local TTS renders the clip and its address goes in as `url` (see "A voice of
  your own" above). An on-device TTS (e.g. piper/espeak) on the speaker itself
  would need its quality and on-box footprint evaluated first.

## Notes / known issues

- Longer text has been less reliable than short text on some boxes (#125); the
  200-character cap keeps announcements in safe territory while that is improved.
- `url` may be used instead of `text` to play any reachable audio file (http, or
  https which the agent fetches and re-serves) as the announcement.

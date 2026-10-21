# Announcements / TTS (#125)

STR can play a short spoken announcement on a speaker, interrupting whatever is
playing and resuming it afterwards (the cloud-free replacement for the firmware's
`/speaker` notification endpoint, which is dead post-shutdown). It is exposed both
in the desktop app (Speaker Settings, the **Announcements** section, one of the
advanced options carrying the "Playground" badge) and as an HTTP endpoint for
home automation.

## How it works

```
desktop app / curl ──POST /api/announce {text, lang, volume}──▶ stick agent
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

## TTS engine: Google Translate TTS

Speech is synthesised by **Google Translate's keyless TTS** endpoint
(`https://translate.google.com/translate_tts`, `internal/webui/announce.go`
`googleTTSURL`). It needs no API key.

- **Language**: the `lang` field maps to Google's `tl` parameter (default `en`).
  The app offers a voice-language dropdown next to the announcement field.
- **Length**: Google TTS has a practical ~200-character per-request limit. The
  agent chunks longer text at `ttsChunkLimit` (180) and concatenates the audio;
  the app input is capped at 200 characters.

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
- A future, fully-private option would be an on-device TTS (e.g. piper/espeak)
  so no text leaves the LAN; quality and on-box footprint would need evaluating.

## Notes / known issues

- Longer text has been less reliable than short text on some boxes (#125); the
  200-character cap keeps announcements in safe territory while that is improved.
- `url` may be used instead of `text` to play any reachable audio file (http, or
  https which the agent fetches and re-serves) as the announcement.

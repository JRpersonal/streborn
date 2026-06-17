# Announcements / TTS (#125)

STR can play a short spoken announcement on a speaker, interrupting whatever is
playing and resuming it afterwards (the cloud-free replacement for the firmware's
`/speaker` notification endpoint, which is dead post-shutdown). It is exposed both
in the desktop app (Speaker Settings, the **Announcements (beta)** section) and as
an HTTP endpoint for home automation.

## How it works

```
desktop app / curl ──POST /api/announce {text, lang, volume}──▶ stick agent
   (via boxDo: self-heals :8888 <-> :17008, BCO boxes only answer :17008)
        agent: fetch TTS audio ──▶ snapshot now-playing + volume
                                ──▶ UPnP play the announcement
                                ──▶ wait for it to finish, then resume
```

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

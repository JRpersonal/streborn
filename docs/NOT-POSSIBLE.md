# Asked often, not possible

A few wishes come up again and again, and I can't build them into STR.
This page collects them in one place, each with the reason and the
thread where I decided it. The issue and discussion templates link here,
so please keep this the only full copy of the list.

Only items with a clear, documented decision belong here. Wishes that
are possible but not built yet live in the
[backlog (#390)](https://github.com/JRpersonal/streborn/issues/390) and
in the [Ideas](https://github.com/JRpersonal/streborn/discussions/categories/ideas?discussions_q=category%3AIdeas+sort%3Atop)
category, where you can vote for them.

## Music services

- **Amazon Music.** Amazon encrypts its stream with DRM that only its own
  licensed apps can unlock, and there is no open client to build on. The
  old Amazon source on the SoundTouch ran through the Bose cloud, which is
  gone. Play it from the Amazon Music app on your phone over Bluetooth.
  Decided in [#1104](https://github.com/JRpersonal/streborn/discussions/1104),
  asked again in [#1126](https://github.com/JRpersonal/streborn/discussions/1126),
  [#1270](https://github.com/JRpersonal/streborn/discussions/1270),
  [#1335](https://github.com/JRpersonal/streborn/discussions/1335) and
  [#1493](https://github.com/JRpersonal/streborn/discussions/1493).
- **Apple Music.** Apple encrypts its stream the same way, and there is
  no open client either. Use the Apple Music app over Bluetooth, or
  AirPlay on speakers that have it. Decided in
  [#1354](https://github.com/JRpersonal/streborn/discussions/1354).
- **Spotify in lossless quality.** Spotify serves open clients like
  go-librespot only its standard stream. Lossless stays inside Spotify's
  own apps. With Premium, STR plays up to 320 kbit/s. Decided in
  [#839](https://github.com/JRpersonal/streborn/discussions/839).

## Bose cloud features

- **The old Bose Alexa skill.** It ran from Alexa through the Bose cloud
  to the speaker, and both ends are switched off for good. A new cloud
  skill would need a server, an account and a door into your home, so I
  won't build one. Home Assistant on your own network can offer the
  speakers to Alexa, see [docs/ALEXA.md](ALEXA.md). Local voice control
  is open for votes in
  [#1124](https://github.com/JRpersonal/streborn/discussions/1124).
- **Cloud parts of the Bose app.** The Bose account, station browsing
  and saving presets in the Bose app ran on Bose's servers, which are
  gone. Power, volume and Wi-Fi setup still work in the Bose app. Your
  stations and keys 1 to 6 live in the STR app. See the FAQ entry "Can I
  still use the Bose app and other apps?" on
  [st-reborn.de](https://st-reborn.de/en/#faq).

## Speakers that are not SoundTouch

- **Bose Music speakers** (Smart Soundbar, Soundbar 500, 700 and 900,
  Home Speaker 300 and 500, Portable Smart Speaker). They belong to the
  newer Bose Music line, run on the Bose Music app and were not affected
  by the SoundTouch shutdown, so there is nothing for STR to bring back.
  STR is for SoundTouch speakers only, see
  [docs/MODELS.md](MODELS.md). A SoundTouch 300 soundbar is a
  SoundTouch and is supported.

## Adding an entry

Add a line only after the decision is written down in a public thread
or the website FAQ. Give the reason in one or two plain sentences, say
what works instead, and link the thread. Keep the short lists in
`.github/ISSUE_TEMPLATE/bug_report.yml`,
`.github/DISCUSSION_TEMPLATE/q-a.yml` and
`.github/DISCUSSION_TEMPLATE/ideas.yml` to the most asked items.

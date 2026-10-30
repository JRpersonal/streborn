# Acknowledgements and third-party software

STR stands on other people's open source. Thank you.

- **[go-librespot](https://github.com/devgianlu/go-librespot)** by devgianlu, **GPL-3.0**, the Spotify Connect client that powers STR's Spotify support. It ships as a **separate binary** that STR runs as a child process and talks to over a local API/pipe; it is not linked into STR, so STR's own Apache-2.0 code and the GPL-3.0 binary are merely aggregated, each under its own license. STR builds it from a small fork, [JRpersonal/go-librespot](https://github.com/JRpersonal/go-librespot) (also GPL-3.0), that adds a raw-Ogg passthrough mode; that change is offered back upstream ([devgianlu/go-librespot#316](https://github.com/devgianlu/go-librespot/pull/316)). The full source of the bundled build is public there.
- **[radio-browser.info](https://www.radio-browser.info/)**, the community-run radio station directory STR searches, with no key and no account.
- **[Octicons](https://github.com/primer/octicons)** by GitHub (MIT), a couple of UI icons.
- **[Bose-SoundTouch](https://github.com/gesellix/Bose-SoundTouch)** by gesellix, the independent cloud emulation whose documented account and BMX service schema STR's native source registration is built on: the numeric `sourceproviderid` catalogue, the `<devices>` block the firmware requires before it accepts an account at all, and the finding that device-local slots such as `UPNP` and `STORED_MUSIC_MEDIA_RENDERER` must never be served to a speaker as account sources. Sponsor: [github.com/sponsors/gesellix](https://github.com/sponsors/gesellix).
- **[bosesoundtouchapi](https://github.com/thlucas1/bosesoundtouchapi)** by thlucas1, the most complete public reference for the speaker's own REST API on port 8090.
- **[SixBack](https://github.com/tostmann/SixBack)** by Dirk Tostmann, an independent post-shutdown revival project; its documented source-registration behaviour helped separate "source not registered" from "source has no account behind it".
- **[soundcork](https://github.com/deborahgu/soundcork)**, the proxied API specification for the Bose cloud calls, assembled from real speaker traffic.
- **[libsoundtouch](https://github.com/CharlesBlonde/libsoundtouch)** by CharlesBlonde, early documentation of the speaker protocol and the WebSocket event bus.
- **[BoseSoundtouch](https://github.com/TimoGo/BoseSoundtouch)** by TimoGo, documented the TAP CLI sequence for Wi-Fi provisioning, including the `network` prefix the Bose service manual leaves out.
- **[Soundtouch-without-the-app](https://github.com/bosefirmware/Soundtouch-without-the-app)** and the wider **[bosefirmware](https://github.com/bosefirmware)** archive, cloud-free operation of the speakers, and the firmware images that made it possible to tell the chassis generations apart.
- The wider community that documented the SoundTouch TAP CLI and firmware behaviour after the cloud shutdown, whose findings STR builds on.

Bundled components keep their own licenses; STR's own code is Apache-2.0. The full list of what is distributed is in [THIRD-PARTY-NOTICES.md](../THIRD-PARTY-NOTICES.md).

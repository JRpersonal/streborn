# Streaming services: what STR can and cannot bring to a SoundTouch

One page per service. Each one follows the same shape: what the speaker
firmware does with the service, every route to the speaker with its
evidence, the tests that settle the open points, and a "Summary for
planning" with a verdict. This page collects the verdicts. All of them
were last checked on 2026-11-23.

## Built

| Service | How | Page |
|---|---|---|
| Spotify | STR runs go-librespot on the speaker as a Spotify Connect receiver | [spotify.md](spotify.md) |
| Pandora, iHeartRadio | The speaker's own clients, logins served back by STR's cloud stand-in | [us-services.md](us-services.md), [pandora.md](pandora.md) |
| Deezer | Kept logins play; a new login is impossible without Bose | [deezer.md](deezer.md) |
| Music on a USB stick | The agent serves the stick as one more media server | [usb-stick.md](usb-stick.md) |

## Evaluated 2026-11-23

| Service | Vote thread | Verdict | Why, in one line | Effort if built | Page |
|---|---|---|---|---|---|
| Tidal | #1105 (7) | Close as covered by casting; build only with TIDAL's written approval | No Tidal client in the firmware, Tidal Connect needs a device certificate, the unofficial API is against TIDAL's terms and TIDAL has not answered the request from 2026-11-18 | 6 to 8 days (route B) | [tidal.md](tidal.md) |
| Qobuz Connect | #1757 (1) | Close as covered by casting; build only with Qobuz's written approval | Technically easy (no certificate, 16/44.1 FLAC plays), but every open receiver borrows Qobuz's app credentials, and Qobuz had an open-source client shut down in 2026-09 | 8 to 12 days | [qobuz.md](qobuz.md) |
| SoundCloud | #1122 (1) | Close as covered by Bluetooth and AirPlay | The official API is open only to paid artist accounts, the private API is a terms violation, and SoundCloud now serves only HLS in fragmented MP4, which STR's proxy cannot play yet | about 7 days (official, not possible as built) | [soundcloud.md](soundcloud.md) |
| SiriusXM | #997 (2) | Close as not possible the Pandora way; build only with a US or Canadian tester | The firmware's SiriusXM source was an adapter in Bose's cloud, not a client on the speaker | 6 to 8 days (own client) | [siriusxm.md](siriusxm.md) |
| Google Cast receiver | #2013 (1) | Close as not possible | Senders check a Google device certificate, and every open receiver passes only with keys taken from other devices; the big apps also need Chrome's receiver runtime | none | [google-cast.md](google-cast.md) |
| Subsonic / Navidrome | #1751 (2), with #1754 | Build when it reaches the top of the wish list | Clean API, fits the USB-stick design (the agent as one more media server); Airsonic-Advanced already works today through its DLNA switch | 7.5 to 9 days, plus 1 to 1.5 for #1754 | [subsonic.md](subsonic.md) |

The re-check of the services closed earlier is in [siriusxm.md](siriusxm.md):
Amazon Music, Apple Music and YouTube stay closed; TuneIn stays closed as a
service, but STR already resolves TuneIn stations, and TuneIn's directory
search works without a key, so a search fallback behind radio-browser
(about a day) is possible.

## What every owner can do today

| Route | Works on | Services |
|---|---|---|
| AirPlay | SoundTouch Portable, SoundTouch 20 and 30 with the AirPlay chip (the firmware lists `AIRPLAY` as `READY`). Not the SoundTouch 10. | Anything an iPhone, iPad or Mac plays |
| Bluetooth | Every SoundTouch with Bluetooth | Anything a phone plays |
| DLNA apps (BubbleUPnP, mConnect) | Every SoundTouch: each one is a UPnP media renderer | Tidal and Qobuz from inside those apps; the speaker refuses https stream URLs, so an app must relay or send plain http |
| Music Assistant (Home Assistant) | Every SoundTouch through its DLNA provider | Many services, run on the user's own server |

## One finding worth building on its own

Several services and some radio stations now stream HLS with fragmented
MP4 (CMAF) segments. STR's stream proxy refuses those today, and a test
on 2026-11-23 showed the speaker cannot decode fragmented MP4 at all, even
joined into one file ([soundcloud.md](soundcloud.md), "Results on a real
speaker"). Unpacking the AAC frames in the proxy and re-serving them as
ADTS is about two days of work and helps radio as much as any service.

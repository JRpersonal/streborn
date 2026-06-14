# Triggering audio playback on a SoundTouch from a script

This page covers how to make a SoundTouch speaker play an audio URL (for
example an MP3 a home-automation script serves on the LAN) without the Bose
cloud. It is the reference behind questions like "how do I trigger a sound on
the speaker from my raspberrymatic / Home Assistant / shell script".

Replace `192.0.2.66` with your speaker's IP in every example.

## TL;DR

- The native Bose notification endpoint (`POST :8090/speaker`) is **dead** since
  the cloud shutdown: it needs a Bose-issued `app_key` validated against the
  offline cloud and now answers `Error value="403" ... unsupported device` on
  every model. Do not use it.
- The cloud-free way to play a URL is **UPnP AVTransport** on
  `POST :8091/AVTransport/Control`. This is the path STR uses internally, and it
  works the same on SoundTouch 10, 20, 30 and Portable.
- If STR is installed, the simplest trigger is STR's own HTTP API, which wraps
  that UPnP path with the metadata and HTTP handling the speaker needs.

## Option A: via STR's API (simplest, if STR is installed)

STR reaches the speaker on `:17008` (the chipset-whitelisted entry that is
reachable from the LAN on every variant; `:8888` is the on-box loopback).

Play any URL directly (one request):

```sh
curl -s -X POST http://192.0.2.66:17008/api/play \
  -H 'Content-Type: application/json' \
  -d '{"url":"http://192.0.2.10/sound.mp3","title":"Sound"}'
```

Or, in the desktop app, use **Speaker settings -> Play a custom URL from a
preset (Expert)** to put the URL on a preset key. Pressing that key (on the
speaker or in the app) plays it, and a script can trigger the same preset with:

```sh
curl -s -X POST http://192.0.2.66:17008/api/play/3   # preset slot 3
```

STR adds the DIDL metadata and resolves HTTPS to HTTP for the speaker (see the
caveats below), so this is the most robust trigger.

## Option B: directly via UPnP (no STR)

Two SOAP requests to `:8091/AVTransport/Control`: set the URI, then play. The
`CurrentURIMetaData` DIDL-Lite block is **required** — the SoundTouch returns
HTTP 500 if it is empty (verified live on a Portable). Title and class can be
generic; the `<res>` URL must match `CurrentURI`.

```sh
SPEAKER=192.0.2.66
URL=http://192.0.2.10/sound.mp3

# 1) Set the URI (with DIDL-Lite metadata)
curl -s "http://$SPEAKER:8091/AVTransport/Control" \
  -H 'Content-Type: text/xml; charset="utf-8"' \
  -H 'SOAPACTION: "urn:schemas-upnp-org:service:AVTransport:1#SetAVTransportURI"' \
  -d '<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:SetAVTransportURI xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID><CurrentURI>'"$URL"'</CurrentURI><CurrentURIMetaData>&lt;DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/"&gt;&lt;item id="0" parentID="-1" restricted="1"&gt;&lt;dc:title&gt;Sound&lt;/dc:title&gt;&lt;upnp:class&gt;object.item.audioItem.musicTrack&lt;/upnp:class&gt;&lt;res protocolInfo="http-get:*:audio/mpeg:*"&gt;'"$URL"'&lt;/res&gt;&lt;/item&gt;&lt;/DIDL-Lite&gt;</CurrentURIMetaData></u:SetAVTransportURI></s:Body></s:Envelope>'

# 2) Play
curl -s "http://$SPEAKER:8091/AVTransport/Control" \
  -H 'Content-Type: text/xml; charset="utf-8"' \
  -H 'SOAPACTION: "urn:schemas-upnp-org:service:AVTransport:1#Play"' \
  -d '<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:Play xmlns:u="urn:schemas-upnp-org:service:AVTransport:1"><InstanceID>0</InstanceID><Speed>1</Speed></u:Play></s:Body></s:Envelope>'
```

`Stop`, `Pause` are the same shape with the matching SOAPACTION.

### Caveats (apply to both options)

- **HTTP only.** The SoundTouch UPnP renderer rejects HTTPS streams (TLS / cert
  issues, often SOAP 402). Serve the file over plain `http://`. STR's `/api/play`
  falls back from HTTPS to HTTP automatically; a raw UPnP push does not.
- **DIDL metadata required** for the raw UPnP push (empty -> HTTP 500).
- **Replaces the current source, no auto-resume.** UPnP playback takes over the
  speaker and does not return to the previous station afterwards. The old
  interrupt-and-resume behaviour belonged to the `:8090/speaker` notification
  endpoint, which is dead. (A resume-aware TTS/announcement path through STR is
  tracked separately.)
- **State.** A raw UPnP push is most reliable when the speaker is idle /
  `INVALID_SOURCE`; from some active sources the firmware can ignore it. STR
  handles the wake/state itself.

## The dead endpoint (for reference)

`POST :8090/speaker` was the documented notification API:

```xml
<play_info><app_key>...</app_key><url>...</url><service>NOTIFICATION</service>
<message>...</message><reason>...</reason><volume>30</volume></play_info>
```

It required a Bose-issued `app_key` validated server-side and now returns
`unsupported device` on all models. There is no cloud-free workaround for this
specific endpoint; use UPnP instead.

## Per-model notes

All four models (SoundTouch 10, 20, 30, Portable) expose the same `:8090` REST
API and `:8091` UPnP AVTransport renderer, so the recipes above are identical
across them. `:8090` and `:8091` are reachable on the LAN on every variant,
including the Series-I ST10/ST20 whose Bose firewall whitelists them. The
`/speaker` notification endpoint is dead on all of them.

## Related community projects

These independent projects use the same `:8091` UPnP path cloud-free and are
useful references:

- gesellix/Bose-SoundTouch (Go, full local API + survival guide)
- thlucas1/bosesoundtouchapi (Python, REST + UPnP)
- fredgaiotti/bose-soundtouch (UPnP + a local HTTP proxy for HTTPS/token streams)
- thbaja/soundtouch-preset-bridge, sandervg/homeassistant-bose-soundtouch-bridge

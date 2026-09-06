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
preset** (one of the advanced options under the "Playground" badge) to put the
URL on a preset key. Pressing that key (on the
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
  endpoint, which is dead. For a resume-aware alternative, STR ships a separate
  announcement path (`POST :17008/api/announce`) that snapshots the current
  now-playing and volume, plays the clip, then restores them — see
  [`docs/announce-tts.md`](announce-tts.md).
- **State.** A raw UPnP push is most reliable when the speaker is idle /
  `INVALID_SOURCE`; from some active sources the firmware can ignore it. STR
  handles the wake/state itself.

## Remote and top-panel keys as triggers (webhooks)

A key on the remote or on top of the speaker can fire an outgoing trigger:
an HTTP webhook, a UDP packet, or a Wake-on-LAN magic packet. The app's
settings view configures them per key; the agent keeps the config on the
speaker (`/mnt/nv/streborn/webhooks.json`), so it keeps working without the
app. Each trigger fires at most once per 2 s.

Trigger ids, as stored in that file under `buttons`:

| Id | Key | Fires on |
|---|---|---|
| `thumbsUp`, `thumbsDown` | Thumbs up / Thumbs down on the remote | the press, additional only |
| `prev`, `next` | Back / Forward on the remote | the press, additional only (also during playback, next to the skip) |
| `playPause` | Play/Pause on the remote | the press, additional only |
| `preset1` .. `preset6` | Preset keys (remote and top panel) | the speaker's preset selection; "replace" mode withholds STR's playback |
| `aux` | AUX (top panel) | the source change to AUX |
| `power` | Power | the standby transition |
| `thumb` (top-level field) | either thumbs key | fallback when the thumbs key has no trigger of its own |

### How the lower remote keys are told apart

The speaker's WebSocket bus (gabbo, `:8080`) reports Back, Forward, Thumbs up
and Thumbs down as one identical bare `<userActivityUpdate/>` frame, so that
bus cannot distinguish them. The firmware (BoseApp) does decode the key, and
it logs the result into the speaker's syslog, a RAM-only ring buffer
(`syslogd -C512`, nothing reaches NAND). The agent raises the two key
facilities over the TAP port (`loglevel IrDevice on 4`,
`loglevel ConsoleButtons on 4`, forgotten at reboot and re-sent on every
agent start) and follows the ring with one `logread -f` child. Every press
and release then arrives as

```
[(tid):IrDevice:DEBUG]IR Key event: Key()=5, State()=0, Producer()=2
[(tid):ConsoleButtons:DEBUG]CONSOLE Key event: Key()=12, State()=1, Producer()=1
```

with `State` 0 = press, 1 = release and `Producer` 1 = top panel, 2 = IR
remote, 0 = a network client. The key numbers follow the firmware's
`KEY_VAL_*` enum: 0 PLAY, 1 PAUSE, 2 STOP, 3 PREV_TRACK, 4 NEXT_TRACK,
5 THUMBS_UP, 6 THUMBS_DOWN, 7 BOOKMARK, 8 POWER, 9 MUTE, 10 VOLUME_UP,
11 VOLUME_DOWN, 12 to 17 PRESET_1 to PRESET_6, 18 AUX_INPUT, 24 PLAY_PAUSE.
Measured on a Portable (taigan); an ST30 (mojo) lists the same facilities;
the sm2 chassis
(SoundTouch 10, rhino) stays silent on those two facilities but logs the
firmware's analytics line at its default level instead,
`buildJson(), like-pressed, THUMBS_UP, ir-remote`, once per press, which the
agent reads the same way (no release event there, so no hold timing).

The old bare-frame heuristic stays as the fallback for a speaker whose trace
is silent. The diagnostic bundle's `box_keys` section shows whether the trace
is live and the last presses the speaker decoded.

## Group keys (a saved group on a thumbs key)

A multiroom group can be saved as a template and put on the Thumbs up or
Thumbs down key of one speaker's remote (issue #863). One press forms the
group, the next press dissolves it. The Multi-Room tab of the app is where
templates are saved and bound; the Settings tab's remote key map marks a bound
thumbs key with a "G".

**What is stored, and where.** A template is a name, the main speaker, the
members (deviceID + last known address) and the permanent flag. Templates and
the key bindings (`thumbsUp` / `thumbsDown` -> template name) live in
`/mnt/nv/streborn/group-keys.json` on the speaker whose remote is pressed,
because a remote press is only seen by the speaker it points at. The file is
written only when the app saves it (`PUT /api/groupkeys`, LAN only;
`GET /api/groupkeys` reads it back). A press writes nothing to NAND.

**What a press does.** The agent reads the main speaker's live zone from that
speaker's agent (`GET /api/box/zone`) and decides:

| Live state on the main speaker | Press |
|---|---|
| The template's group is live (same main speaker, every template member in the zone; extra members allowed) | dissolve it (`DELETE /api/box/zone`) |
| The other key's template is live on a different main speaker | dissolve that one there, then form this one |
| Anything else (standalone, or a different membership on the same main speaker) | form the template (`POST /api/box/zone`, same body the app sends) |

When the main speaker was idle before the form, the press also brings its
last station back (`POST /api/box/power {"on":true}`, the same power-on
resume the phone remote uses), so the press ends in music. A permanent
template's form is only stored while the main speaker is idle; that resume is
the play that makes the existing play-triggered re-form wire the zone and wake
the stored members.

The calls go to the main speaker's agent over the LAN (`:17008`, then
`:8888`, the roster's last known port first), or over loopback when the
speaker that saw the press is the main speaker itself. So a press on a member
speaker's remote works too: the member forwards the form or dissolve to the
main speaker's agent, the same call the app makes. The speakers are addressed
at the address the peer roster holds for their id, so a template survives a
DHCP renumbering; the stored address is only used when the roster has no entry
for the id, and never when the roster says another speaker sits there now
(then the press fails with "save the group again", and saving the template in
the app refreshes the addresses).

Dissolving goes through the same path as the app's Ungroup, so it also clears
the main speaker's stored permanent group; the template itself stays on the
remote's speaker, and the next press forms it again with its permanent flag.

**Rules.** A key carries one action: a thumbs key bound to a group does not
fire its webhook. Presses that land while a form or dissolve for that key is
still running are ignored, and so is a second press within 3 s. The 2 s
webhook rule for the other keys stays as it is.

**Limits of the first cut.** No feedback layer: nothing is shown on the
display and nothing is spoken; the members joining or going quiet is the
confirmation. Members in standby are not woken by the form itself; the
existing play-triggered re-form wakes them once music starts on the main
speaker. A template names speakers by their id and last address; when the
main speaker cannot be reached, the press is logged (`group key: press
failed`, at most one WARN per minute per key) and nothing changes. The
diagnostic bundle's `group_keys` section shows the templates, the bindings,
the last action and the last error.

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

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
shutdown; use the UPnP path above instead.

## The other direction: a speaker press driving Home Assistant

A remote-control key, the power button, a preset or an AUX change can fire an
outgoing HTTP webhook, a UDP packet, or Wake-on-LAN, so a button on the speaker
can trigger a Home Assistant scene. See the smart-home triggers in
[`AUTOMATION.md`](./AUTOMATION.md) and the app's automation settings.

## A note on certainty

These are the local surfaces and the integrations that target them; STR keeps
them working, and the recipes above are what STR uses internally. I have not
certified every Home Assistant integration version end to end, so if you wire up
`soundtouchplus`, the core SoundTouch integration or the DLNA renderer against an
STR box, please report what worked in a GitHub discussion. Others are asking the
same thing, and confirmed setups make this page better for the next person.

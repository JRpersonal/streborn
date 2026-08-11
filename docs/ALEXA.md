# Voice control: Alexa, Google and Home Assistant

STR does not talk to Alexa by itself, and it will not. This page explains why,
and how to get voice control anyway with a local server you run at home.

## Why the old Alexa control is gone

The SoundTouch Alexa skill was **cloud to cloud**. Amazon never talked to your
speaker: Alexa sent the command to Bose's servers, and those servers reached
your speaker over its cloud connection. Both halves belonged to Bose and both
are switched off. There is nothing left on your network for Amazon to talk to,
so the skill cannot be revived, by STR or by anyone else.

Building a new skill would mean running a server on the public internet that
Amazon can call, with accounts, logins and a permanent bill, and an
internet-facing path into your speaker. STR is deliberately a local program with
no account and no cloud, so that is not the road it takes.

## What does work: a local server in your home

Alexa can control devices that live on your own network through a hub. You run
that hub, it holds the list of your speakers, and Alexa only ever talks to it.
[Home Assistant](https://www.home-assistant.io/) is the usual choice: free, open
source, and it runs on a Raspberry Pi, a NAS, or any machine that stays on.

```
Alexa  ->  Home Assistant (your network)  ->  ST Reborn on the speaker
```

The speakers stay exactly as they are. Home Assistant simply calls the same
addresses the ST Reborn app already uses.

This is a project for someone comfortable setting up a small server. If that is
not you, nothing is broken: the ST Reborn app and the phone remote control
everything without any of this.

## What Home Assistant needs to know

Every speaker answers on its own address. Replace `192.0.2.10` with the address
of your speaker, which the ST Reborn app shows in the speaker list, and use port
`17008` (some models answer on `8888` instead; the app shows which one it uses).

| What you want | Call |
|---|---|
| Play preset 1 to 6 | `POST http://192.0.2.10:17008/api/play/2` |
| Stop | `POST http://192.0.2.10:17008/api/stop` |
| Pause / continue | `POST .../api/pause`, `POST .../api/resume` |
| Switch the speaker off | `POST .../api/box/power` with body `{"on":false}` |
| Switch it on | `POST .../api/box/power` with body `{"on":true}` |
| Set the volume (0 to 100) | `PUT .../api/box/volume` with body `{"value":25}` |
| Read the volume | `GET .../api/box/volume` |

In Home Assistant these become `rest_command` entries, and each one can then be
exposed to Alexa. A `rest_command` for preset 2 looks like this:

```yaml
rest_command:
  kitchen_preset_2:
    url: "http://192.0.2.10:17008/api/play/2"
    method: post
```

Home Assistant also ships a Bose SoundTouch integration that finds the speakers
on its own and gives you volume, power and play/pause as a normal media player.
Use that for the basics, and the calls above for the ST Reborn presets, which
the old integration knows nothing about.

## Getting it to Alexa

Home Assistant offers more than one route, and both work with the setup above:

- **Emulated Hue** (`emulated_hue`): Home Assistant pretends to be a Philips Hue
  bridge, which Alexa discovers on the network with no account and no skill.
  On and off map to your speaker, and the brightness slider maps to the volume.
  It must listen on port 80, and Alexa remembers the address it found, so give
  the machine a fixed one.
- **Home Assistant Cloud (Nabu Casa)**: a paid subscription that connects Home
  Assistant to Alexa properly, with normal Alexa device names and routines. This
  one is a cloud service, so it is your decision, not something STR requires.

Say "Alexa, discover devices" once the bridge is up, and the speakers appear.

## What voice commands you get

Realistically: switching a speaker on and off, setting the volume, and starting a
preset ("Alexa, turn on kitchen preset 2"). Free speech like "Alexa, play NDR 2
in the kitchen" needs a real skill and is not part of this.

Two things worth knowing before you start: Alexa gets unreliable above roughly
50 exposed devices, so expose only the speakers and presets you actually use;
and a speaker in deep standby does not answer on the network, so give the ones
you want to switch on by voice a moment after a power cut.

## Trademarks

Alexa and Amazon Echo are trademarks of Amazon. Home Assistant is a trademark of
the Home Assistant project. STR is not affiliated with either, and neither
endorses it.

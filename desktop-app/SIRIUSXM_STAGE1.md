# SiriusXM Stage 1 integration

This Stage 1 integration keeps the existing, working Python helpers as the
streaming engine and lets ST Reborn manage them.

Required files on Windows:
- `sxm.py`
- `relay.py`
- Python launcher `py -3`
- `ffmpeg.exe` at `C:\ffmpeg\ffmpeg.exe` (used by the working relay)

The desktop app defaults to `sxm.py` and `relay.py` beside the executable,
then the current working directory, then `C:\ffmpeg\` when those files exist.
The paths can also be edited in the SiriusXM tab.

The Start button performs the same two launches as the working BAT:
- `py -3 sxm.py <username> <password> -p 9998`
- `py -3 relay.py`

The two processes are opened in visible command windows. STR reuses an
already-running helper if ports 9998/8000 are already listening.

The SiriusXM tab reads the station table directly from `relay.py`, so the UI
uses the same station IDs and mount names as the relay. Playing a station uses
`http://<PC-LAN-IP>:8000/<mount>` and sends that URL through STR's normal
PlayURL path with MP3 metadata.

The SiriusXM password is stored in the normal ST Reborn user config file as
Stage 1. Because the helper currently requires credentials on its command line,
the password is also visible to Windows process inspection while `sxm.py` is
running. A later stage should replace that with OS credential storage and a
native helper.

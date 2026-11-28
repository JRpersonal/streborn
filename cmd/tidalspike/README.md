# tidalspike

A throwaway measurement tool for the Tidal source decision. It is **not**
part of the agent or the desktop app, it is not built by the release
pipeline, and it is never shipped. Delete it once the questions below are
answered.

Tidal Connect is ruled out (the Tidal app only pairs with a receiver that
presents a Tidal device certificate). The remaining route is the unofficial
v1 API the Tidal apps use, as implemented by
[python-tidal](https://github.com/EbbLabs/python-tidal) (LGPL). This tool is
an independent Go reimplementation of the few calls the decision needs; no
python-tidal code is copied.

## Questions it answers

1. Does the OAuth device-code login (TV-style "enter this code at
   link.tidal.com") work, and does the refresh token keep working?
2. For each quality (LOW, HIGH, LOSSLESS, HI_RES_LOSSLESS), what does
   `playbackinfopostpaywall` deliver today: BTS JSON or DASH MPD, which
   MIME type and codec, which container on the wire (raw FLAC vs
   FLAC-in-MP4), encrypted or not?
3. Are the media URLs plain signed URLs, how long do they live, and does
   the CDN honour Range requests (seeking)?
4. Does a SoundTouch play such a URL when handed it over UPnP AVTransport
   (`:8091`) with a DIDL-Lite whose protocolInfo carries the real MIME type?

## Client id and secret

Tidal's API only talks to a registered client. The tool reads them from the
environment and **never** stores them:

- `TIDAL_CLIENT_ID`
- `TIDAL_CLIENT_SECRET`

python-tidal ships a working pair for the device ("limited input") login in
`tidalapi/session.py`, class `Config`, `__init__`: the attributes
`client_id` and `client_secret` (stored base64-obfuscated; the separate
`client_id_pkce` / `client_secret_pkce` pair belongs to the PKCE browser
login and is not what this tool uses). Take them from there for a local run.
Do not paste the values into this repository, an issue, a PR, or a log.

## Other environment variables

| Variable | Purpose |
|---|---|
| `TIDALSPIKE_TEST_SPEAKER` | Interlock for `play`: the `--speaker` IP must equal this, otherwise `play` refuses. |
| `TIDALSPIKE_TOKEN_FILE` | Override the token file path. |
| `TIDAL_CLIENT_VERSION` | Override the `x-tidal-client-version` header if Tidal starts rejecting the default. |
| `TIDALSPIKE_DEBUG` | Any value turns on debug logging. |

Tokens are saved to `<UserConfigDir>/str-tidalspike/tokens.json` (mode 0600;
on Windows `%AppData%\str-tidalspike\tokens.json`). The file holds the access
and refresh token, their expiry, the numeric user id and the country code,
nothing else. Delete it to log out.

## Command sequence

PowerShell (bash: `export VAR=...`):

```powershell
$env:TIDAL_CLIENT_ID     = '<from python-tidal, see above>'
$env:TIDAL_CLIENT_SECRET = '<from python-tidal, see above>'
$ts = "$env:TEMP\tidalspike.exe"   # outside the repo, never commit the binary
go build -o $ts ./cmd/tidalspike

& $ts login            # open the printed link, enter the code
& $ts whoami           # user id, country, subscription tier
& $ts playlists        # id, track count, name
& $ts refresh          # proves the refresh token works

# Pick a track id from the Tidal web player URL (tidal.com/browse/track/<id>).
& $ts track <id> --quality all --probe
& $ts track <id> --quality LOSSLESS --dump "$env:TEMP\tidal-manifests.txt"

# Dry run first: resolves the stream and prints the DIDL, touches no speaker.
& $ts play <id> --speaker 192.0.2.1 --dry-run

$env:TIDALSPIKE_TEST_SPEAKER = '192.0.2.1'
& $ts play <id> --speaker 192.0.2.1 --quality LOSSLESS
& $ts play <id> --speaker 192.0.2.1 --quality HIGH
```

`play` sets the speaker volume to 5 via `:8090/volume` before
`SetAVTransportURI`, re-asserts 5 once `Play` has started, then samples
`GetPositionInfo` twice eight seconds apart so you can see whether playback
actually advances. It only accepts a BTS manifest with a single unencrypted
URL; a DASH or encrypted stream is reported and refused, because that is
itself the answer (a stream proxy would be needed).

`track` prints URLs redacted (host and first path segment only). `--show-urls`
prints them in full and `--dump` writes the decoded manifests including the
signed URLs; keep both local.

## Reading the output

The `verdict:` line per quality is the summary:

- `single plain raw FLAC URL` (or `AAC-in-MP4`): the speaker can be handed
  the URL directly; a real Tidal source needs no audio proxy for that tier.
- `DASH, FLAC-in-fMP4 in N segments`: needs a segment joiner in the stream
  proxy, plus a remux to raw FLAC if the speaker rejects fragmented MP4.
- `ENCRYPTED`: no route without DRM, out of scope.

`probe:` confirms the container from the first bytes (`fLaC` vs `ftyp`) and
shows whether the CDN answers Range requests with 206. `url expiry:` shows
the expiry if the CDN token carries one; "not visible" means it has to be
measured by retrying the URL later.

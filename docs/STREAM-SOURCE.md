# LAN Stream Source

STR can browse stations from a stream server running on the local network. The
server owns its authentication and provider-specific setup. STR reads the
station list and sends the selected station's playable URL to the speaker.

Open **Listen to music** and expand **Stream source**. Enter the server's LAN
address, for example `http://192.0.2.10:8765`. Use the server computer's LAN
address rather than `localhost` or `127.0.0.1`: the speaker fetches the audio
directly, so loopback addresses would refer to the speaker itself.

## API contract

`GET {base}/api/stations` returns a JSON array or an object with a `stations`
array. Each station has an opaque `id`, a display `name`, and an optional
`stream_type`:

```json
[{"id":"station-1","name":"Example Station","stream_type":"audio"}]
```

When `stream_type` is `audio` or omitted, `GET {base}/api/stations/{id}/stream`
is the speaker-playable audio endpoint. STR returns that URL without requesting
it, so selecting a station does not open and discard a live stream session.

When `stream_type` is `json`, the same endpoint returns JSON containing `url`
or `stream_url`, and optionally `title` or `now_playing`. The returned URL must
use HTTP or HTTPS and be reachable by the speaker.

The stream server must make its audio endpoint reachable from the speaker and
keep any credentials or provider-specific logic on the server.

# LAN stream-source tab

STR can browse a separately run stream source without embedding or naming the
service behind it. The service runs on a computer, NAS, or other host on the
LAN. It keeps its own login and provider-specific code; STR only receives
station names and a URL the selected speaker can fetch.

Enter the server's LAN address in the desktop app's **Stream source** tab. For
the SiriusXM sidecar in this workspace, that is usually
`http://<computer-LAN-IP>:8091`. Use the computer's LAN IP rather than
`localhost` or `127.0.0.1`: the speaker fetches the stream URL directly and
those loopback addresses would refer to the speaker itself.

## Endpoints

`GET {base}/api/stations` returns either a JSON array or an object containing
the array:

```json
[{"id":"octane","name":"Octane"}]
```

STR requests `GET {base}/api/stations/{id}/stream`. A server may return audio
directly, or JSON containing `url` or `stream_url` and an optional `title` or
`now_playing`. The final stream URL must use HTTP or HTTPS and be reachable
from the speaker.

STR stores no account credentials and does not forward credentials to the
service or the speaker.

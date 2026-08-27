# ST Reborn native SiriusXM integration

This replaces the external SiriusXM Python helper/relay runtime with a native Go implementation.

Removed at runtime:
- Python
- sxm.py
- relay.py
- Requests
- PyCryptodome
- FFmpeg

The SiriusXM code uses only the Go standard library:
- net/http + cookiejar
- encoding/json
- crypto/aes + crypto/cipher
- URL/HLS parsing
- a native on-demand HTTP relay

The desktop app keeps the same Wails SiriusXM method names used by the Stage-1 UI, so existing generated bindings remain compatible.

The station table from the working relay.py is compiled into the application, so relay.py is no longer needed.

Port 8000 is the native relay's LAN endpoint:
  http://<PC-LAN-IP>:8000/ClassicVinyl

The app still stores the SiriusXM username/password in the existing ST Reborn config directory. The password is not compiled into the application.

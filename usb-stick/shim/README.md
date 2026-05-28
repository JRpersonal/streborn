# STR LD_PRELOAD shim

`shim.c` is a tiny `accept()`/`accept4()` interposer that runs inside
the Bose `SoftwareUpdate` daemon at runtime. When SoftwareUpdate's
listener accepts a connection on port **17008** the shim transparently
forwards the new socket to **127.0.0.1:8888** (the STR webui) using
two `pthread` byte pumps. SoftwareUpdate gets told `EAGAIN` for the
hijacked accept call so it never tries to read or respond.

## Why it exists

The BCO wifi chipset firmware on every SoundTouch model (Portable
verified, ST10/20/30 likely the same) enforces an inbound-port
whitelist below the Linux kernel. Listeners bound by non-Bose
binaries are dropped at chipset level even with empty iptables and a
correct `0.0.0.0:N LISTEN` socket. The whitelist tracks the *binary
content* of the bind owner, not the port number or process identity,
so binding STR's :8888 to any non-Bose port gets RST'd from outside.

Running `SoftwareUpdate` under `LD_PRELOAD=str-shim.so` keeps Bose's
binary as the listener (chipset stays happy) while STR effectively
owns the connection content. SoftwareUpdate is a cloud-only daemon
that has nothing to do post-shutdown anyway.

## Build

Cross-compile to `arm-linux-gnueabihf` against glibc 2.15
(Bose firmware ships glibc 2.15):

```bash
zig cc -target arm-linux-gnueabihf.2.15 -shared -fPIC -O2 \
       -ldl -lpthread \
       -o ../str-shim.so shim.c
```

Zig 0.16 has been verified. Any cross-toolchain that produces an
armhf ELF dynamically-linked against the standard libc / libpthread
will work; `arm-linux-gnueabihf-gcc` from Linaro is the fallback.

The prebuilt `../str-shim.so` in this repo is the artifact embedded
in the stick template via `usb-stick/files.go` and deployed to NAND. CI rebuilds it on every release so the
binary blob stays auditable.

## Hijack target port

The hijack port is compiled into the shim via the `HIJACK_PORT`
preprocessor constant (currently `17008`). The forward target is
`127.0.0.1:STR_TARGET_PORT` (currently `8888`). To target a different
Bose service slot, change those constants and rebuild.

## Compatibility

- Bose firmware version: `27.0.6.46330.5043500` (final cloud build)
- glibc: 2.15
- Kernel: 3.14.43
- Architecture: armv7l, armhf ABI
- Verified live on: SoundTouch Portable (taigan), 2026-05-28

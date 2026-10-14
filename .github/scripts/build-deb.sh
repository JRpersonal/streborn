#!/bin/sh
# build-deb.sh packs the Linux desktop binary into a .deb, so Debian, Ubuntu,
# Mint and Raspberry Pi OS users install ST Reborn with a double click and get
# the runtime libraries pulled in by apt instead of a missing-library error.
#
#   build-deb.sh <binary> <version> <deb-arch> <out.deb>
#
# <version> is the release tag (v1.0.13), or dev-<hash> on a dry run; <deb-arch> is
# amd64 or arm64. The app finds out it was installed this way from dpkg's own
# file list and then updates through a new .deb instead of swapping /usr/bin
# behind dpkg's back (desktop-app/linuxpkg.go).
set -eu

BIN=$1
VERSION=$2
ARCH=$3
OUT=$4

# Debian versions must start with a digit, and "~" sorts before the release it
# precedes, so v1.0.13-rc.1 becomes 1.0.13~rc.1 and stays older than 1.0.13.
# A dry run carries dev-<hash>, which becomes 0.0.0~dev-<hash> and never looks
# newer than a real release.
case "$VERSION" in
  v[0-9]*) DEBVER=$(printf '%s' "${VERSION#v}" | sed 's/-/~/') ;;
  *) DEBVER="0.0.0~${VERSION:-dev}" ;;
esac

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
here=$(cd "$(dirname "$0")/../.." && pwd)

install -D -m 755 "$BIN" "$ROOT/usr/bin/st-reborn"
install -D -m 644 "$here/packaging/linux/st-reborn.desktop" "$ROOT/usr/share/applications/st-reborn.desktop"
for s in 128 256 512; do
  install -D -m 644 "$here/packaging/linux/icons/st-reborn-$s.png" "$ROOT/usr/share/icons/hicolor/${s}x${s}/apps/st-reborn.png"
done
# The license, the attribution and the third-party notices, as the Apache
# License and the GPL engine ask, in the place Debian keeps them.
install -D -m 644 "$here/LICENSE" "$ROOT/usr/share/doc/st-reborn/copyright"
install -D -m 644 "$here/NOTICE" "$ROOT/usr/share/doc/st-reborn/NOTICE"
install -D -m 644 "$here/THIRD-PARTY-NOTICES.md" "$ROOT/usr/share/doc/st-reborn/THIRD-PARTY-NOTICES.md"

SIZE=$(du -sk "$ROOT/usr" | cut -f1)
mkdir -p "$ROOT/DEBIAN"
# libgtk-3-0 was renamed libgtk-3-0t64 in Ubuntu 24.04 and Debian trixie, so
# both names are accepted.
cat > "$ROOT/DEBIAN/control" <<CONTROL
Package: st-reborn
Version: $DEBVER
Architecture: $ARCH
Maintainer: JRpersonal <JRpersonal@users.noreply.github.com>
Installed-Size: $SIZE
Depends: libgtk-3-0 | libgtk-3-0t64, libwebkit2gtk-4.1-0, libsoup-3.0-0
Section: sound
Priority: optional
Homepage: https://st-reborn.de
Description: Revive Bose SoundTouch speakers without the Bose cloud
 ST Reborn (STR) brings internet radio, presets and remote control back to
 Bose SoundTouch speakers after the Bose cloud shutdown. Unofficial,
 community-built, not affiliated with or endorsed by Bose. SoundTouch and
 Bose are registered trademarks of Bose Corporation.
CONTROL
# Refresh the menu and icon caches on install and removal. Both tools are
# optional on minimal systems, so a missing one is not an error.
for s in postinst postrm; do
  cat > "$ROOT/DEBIAN/$s" <<'HOOK'
#!/bin/sh
set -e
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true
exit 0
HOOK
  chmod 755 "$ROOT/DEBIAN/$s"
done

dpkg-deb --root-owner-group -Zxz --build "$ROOT" "$OUT"
dpkg-deb --info "$OUT"

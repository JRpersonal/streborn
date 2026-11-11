#!/bin/sh
# build-rpm.sh packs the Linux desktop binary into an .rpm, so Fedora and
# openSUSE users install ST Reborn with dnf, zypper or a double click and get
# the runtime libraries pulled in by their package manager instead of a
# missing-library error.
#
#   build-rpm.sh <binary> <version> <amd64|arm64> <out.rpm>
#
# <version> is the release tag (v1.0.13), or dev-<hash> on a dry run. It needs
# rpmbuild, which the Ubuntu runners get from the "rpm" package; building an
# x86_64 or aarch64 package there is fine because nothing is compiled.
#
# The package runs no script on install or removal: both distributions refresh
# the menu and icon caches through their own file triggers. The app finds out
# it was installed this way by asking rpm who owns its binary and then updates
# through a new .rpm instead of swapping /usr/bin behind rpm's back
# (desktop-app/linuxpkg.go).
set -eu

BIN=$1
VERSION=$2
OUT=$4
case "$3" in
  amd64) ARCH=x86_64 ;;
  arm64) ARCH=aarch64 ;;
  *) echo "build-rpm.sh: unknown architecture $3 (amd64 or arm64)" >&2; exit 1 ;;
esac

# An RPM version must not contain "-", and "~" sorts before the release it
# precedes, so v1.0.13-rc.1 becomes 1.0.13~rc.1 and stays older than 1.0.13.
# A dry run carries dev-<hash>, which becomes 0.0.0~dev.<hash> and never looks
# newer than a real release.
case "$VERSION" in
  v[0-9]*) RPMVER=$(printf '%s' "${VERSION#v}" | sed 's/-/~/' | tr '-' '.') ;;
  *) RPMVER="0.0.0~$(printf '%s' "${VERSION:-dev}" | tr '-' '.')" ;;
esac
# The version is written into the spec below, so anything but the characters
# RPM allows there stops the build.
case "$RPMVER" in
  *[!A-Za-z0-9._+~]*) echo "build-rpm.sh: version $VERSION cannot be an RPM version" >&2; exit 1 ;;
esac

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
here=$(cd "$(dirname "$0")/../.." && pwd)
PAYLOAD="$ROOT/payload"

install -D -m 755 "$BIN" "$PAYLOAD/usr/bin/st-reborn"
install -D -m 644 "$here/packaging/linux/st-reborn.desktop" "$PAYLOAD/usr/share/applications/st-reborn.desktop"
for s in 128 256 512; do
  install -D -m 644 "$here/packaging/linux/icons/st-reborn-$s.png" "$PAYLOAD/usr/share/icons/hicolor/${s}x${s}/apps/st-reborn.png"
done
# The license, the attribution and the third-party notices, as the Apache
# License and the GPL engine ask, in the places RPM distributions keep them.
install -D -m 644 "$here/LICENSE" "$PAYLOAD/usr/share/licenses/st-reborn/LICENSE"
install -D -m 644 "$here/NOTICE" "$PAYLOAD/usr/share/doc/st-reborn/NOTICE"
install -D -m 644 "$here/THIRD-PARTY-NOTICES.md" "$PAYLOAD/usr/share/doc/st-reborn/THIRD-PARTY-NOTICES.md"

# No Requires line names a library package: Fedora and openSUSE call GTK 3,
# WebKitGTK 4.1 and libsoup 3 differently. rpmbuild reads the shared libraries
# the binary links (libwebkit2gtk-4.1.so.0 and so on) and records those, and
# each distribution resolves them to its own packages.
#
# The binary is the finished, stripped release build. __os_install_post and
# debug_package are switched off so rpmbuild does not strip or split it again:
# the package must carry the same bytes as the tar.gz and the .deb.
SPEC="$ROOT/st-reborn.spec"
cat > "$SPEC" <<SPEC
Name: st-reborn
Version: $RPMVER
Release: 1
Summary: Revive Bose SoundTouch speakers without the Bose cloud
License: Apache-2.0
URL: https://st-reborn.de
Requires: xdg-utils

%global debug_package %{nil}
%global __os_install_post %{nil}

%description
ST Reborn (STR) brings internet radio, presets and remote control back to
Bose SoundTouch speakers after the Bose cloud shutdown. Unofficial,
community-built, not affiliated with or endorsed by Bose. SoundTouch and
Bose are registered trademarks of Bose Corporation.

%install
mkdir -p %{buildroot}
cp -a "$PAYLOAD/." %{buildroot}/

%files
/usr/bin/st-reborn
/usr/share/applications/st-reborn.desktop
/usr/share/icons/hicolor/*/apps/st-reborn.png
%license /usr/share/licenses/st-reborn/LICENSE
%doc /usr/share/doc/st-reborn/NOTICE
%doc /usr/share/doc/st-reborn/THIRD-PARTY-NOTICES.md
SPEC

rpmbuild --quiet --define "_topdir $ROOT/rpmbuild" --define "_build_id_links none" \
  --target "$ARCH" -bb "$SPEC"
mkdir -p "$(dirname "$OUT")"
cp "$ROOT/rpmbuild/RPMS/$ARCH/st-reborn-$RPMVER-1.$ARCH.rpm" "$OUT"
rpm -qip "$OUT"

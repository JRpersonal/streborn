#!/bin/sh
# build-rpm.sh <Linux desktop binary> <version/tag> <amd64|arm64> <out.rpm>
# Run on either architecture with rpm-build installed. Shared-library Requires
# are generated from the ELF binary, so Fedora and openSUSE can resolve their
# own package names for GTK 3, WebKitGTK 4.1 and libsoup 3.
set -eu

if [ "$#" -ne 4 ]; then
  echo "Usage: $0 <binary> <version> <amd64|arm64> <out.rpm>" >&2
  exit 1
fi
case "$3" in
  amd64|x86_64) ARCH=x86_64 ;;
  arm64|aarch64) ARCH=aarch64 ;;
  *) echo "Unsupported RPM architecture: $3" >&2; exit 1 ;;
esac

VERSION=${2#v}
case "$VERSION" in
  [0-9]*) VERSION=$(printf '%s' "$VERSION" | sed 's/-/~/') ;;
  *) VERSION="0.0.0~${VERSION:-dev}" ;;
esac
# RPM reserves '-' as the version/release separator; remaining tag separators
# (for example dev-<commit> or rc-1) become dots after the prerelease marker.
VERSION=$(printf '%s' "$VERSION" | tr '-' '.')
# Reject spec syntax and RPM-invalid version characters supplied by a caller.
case "$VERSION" in
  *[!a-zA-Z0-9._+~]*) echo "Invalid RPM version: $VERSION" >&2; exit 1 ;;
esac

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
here=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p "$ROOT/SOURCES" "$ROOT/SPECS" "$ROOT/payload"
install -D -m 755 "$1" "$ROOT/payload/usr/bin/st-reborn"
install -D -m 644 "$here/packaging/linux/st-reborn.desktop" "$ROOT/payload/usr/share/applications/st-reborn.desktop"
for s in 128 256 512; do
  install -D -m 644 "$here/packaging/linux/icons/st-reborn-$s.png" "$ROOT/payload/usr/share/icons/hicolor/${s}x${s}/apps/st-reborn.png"
done
install -D -m 644 "$here/LICENSE" "$ROOT/payload/usr/share/licenses/st-reborn/LICENSE"
for doc in NOTICE THIRD-PARTY-NOTICES.md; do
  install -D -m 644 "$here/$doc" "$ROOT/payload/usr/share/doc/st-reborn/$doc"
done
tar -czf "$ROOT/SOURCES/payload.tar.gz" -C "$ROOT/payload" .
cat > "$ROOT/SPECS/st-reborn.spec" <<SPEC
Name: st-reborn
Version: $VERSION
Release: 1
Summary: Revive Bose SoundTouch speakers without the Bose cloud
License: Apache-2.0
URL: https://st-reborn.de
Source0: payload.tar.gz
Requires: xdg-utils
SPEC
cat >> "$ROOT/SPECS/st-reborn.spec" <<'SPEC'

# The input is an already stripped production Go/Wails binary. Keep it intact
# and avoid distro-specific debug extraction and build-id requirements.
%global debug_package %{nil}
%global __os_install_post %{nil}

%description
ST Reborn (STR) brings internet radio, presets and remote control back to
Bose SoundTouch speakers after the Bose cloud shutdown. Unofficial,
community-built, not affiliated with or endorsed by Bose.

%prep
%setup -q -c -T
tar -xzf %{SOURCE0}

%install
mkdir -p %{buildroot}
cp -a usr %{buildroot}/

%files
/usr/bin/st-reborn
/usr/share/applications/st-reborn.desktop
/usr/share/icons/hicolor/*/apps/st-reborn.png
%license /usr/share/licenses/st-reborn/LICENSE
%doc /usr/share/doc/st-reborn/NOTICE
%doc /usr/share/doc/st-reborn/THIRD-PARTY-NOTICES.md
SPEC

rpmbuild --define "_topdir $ROOT" --define '_build_id_links none' \
  --target "$ARCH" -bb "$ROOT/SPECS/st-reborn.spec"
mkdir -p "$(dirname "$4")"
cp "$ROOT/RPMS/$ARCH/st-reborn-$VERSION-1.$ARCH.rpm" "$4"
rpm -qip "$4"

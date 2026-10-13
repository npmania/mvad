#!/bin/sh
# build.sh makes dist/mvad.app and dist/mvad-$VERSION.dmg, both
# universal (arm64 and x86_64). Signing is ad hoc unless SIGN_ID names
# a Developer ID identity; with NOTARY_PROFILE, a notarytool keychain
# profile, the disk image is also notarized and stapled.
set -eu
cd "$(dirname "$0")/.."

# cgo targets the build host's macOS unless told; Go itself targets 13.
# The flags, unlike MACOSX_DEPLOYMENT_TARGET, are part of the build
# cache key.
export CGO_CFLAGS="-O2 -g -mmacosx-version-min=13.0"
export CGO_LDFLAGS="-O2 -g -mmacosx-version-min=13.0"

version=${VERSION:-$(git describe --tags --abbrev=0)}
version=${version#v}
if [ -n "${NOTARY_PROFILE:-}" ] && [ -z "${SIGN_ID:-}" ]; then
	echo 'build.sh: NOTARY_PROFILE needs SIGN_ID' >&2
	exit 1
fi
out=dist
app=$out/mvad.app
tmp=$out/tmp
dmg=$out/mvad-$version.dmg

sign() {
	if [ -n "${SIGN_ID:-}" ]; then
		codesign --force --sign "$SIGN_ID" --options runtime --timestamp "$@"
	else
		codesign --force --sign - "$@"
	fi
}

rm -rf "$app" "$tmp" "$dmg"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" "$tmp/mvad.iconset" "$tmp/dmg"

for cmd in mvad mvad-gui; do
	for arch in arm64 amd64; do
		CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -o "$tmp/$cmd-$arch" ./cmd/$cmd
	done
	lipo -create -output "$app/Contents/MacOS/$cmd" "$tmp/$cmd-arm64" "$tmp/$cmd-amd64"
done
sed "s/@VERSION@/$version/" macos/Info.plist > "$app/Contents/Info.plist"

qlmanage -t -s 1024 -o "$tmp" macos/icon.svg > /dev/null
for s in 16 32 128 256 512; do
	sips -z $s $s "$tmp/icon.svg.png" --out "$tmp/mvad.iconset/icon_${s}x$s.png" > /dev/null
	sips -z $((s * 2)) $((s * 2)) "$tmp/icon.svg.png" --out "$tmp/mvad.iconset/icon_${s}x$s@2x.png" > /dev/null
done
iconutil -c icns -o "$app/Contents/Resources/mvad.icns" "$tmp/mvad.iconset"

sign "$app/Contents/MacOS/mvad"
sign "$app"

ditto "$app" "$tmp/dmg/mvad.app"
ln -s /Applications "$tmp/dmg/Applications"
hdiutil create -quiet -volname mvad -srcfolder "$tmp/dmg" -format UDZO -o "$dmg"
if [ -n "${SIGN_ID:-}" ]; then
	sign "$dmg"
fi
if [ -n "${NOTARY_PROFILE:-}" ]; then
	xcrun notarytool submit "$dmg" --keychain-profile "$NOTARY_PROFILE" --wait
	xcrun stapler staple "$dmg"
fi
rm -rf "$tmp"
echo "$dmg"

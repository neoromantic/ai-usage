#!/bin/sh
# Build the menu bar app as a universal, signed "AI Usage.app" and zip it
# the way a release ships it. Needs macOS with Xcode or the Command Line
# Tools; runs from any folder.
#
#   sh macos/build.sh VERSION OUT_ZIP
#
# VERSION is the release tag, such as v0.3.0; the bundle's version is the
# tag without its v. The files on the way go to macos/.build.
#
# The app is signed ad hoc, unless AI_USAGE_SIGN_IDENTITY names a Developer
# ID Application identity in the keychain: then it is signed with it, with
# the hardened runtime and a secure timestamp. With AI_USAGE_NOTARY_KEY set
# too, Apple notarizes it and the ticket is stapled to it before the zip;
# see .github/scripts/notarize.sh for the key's variables.
set -eu

version=${1:?usage: build.sh VERSION OUT_ZIP}
zip=${2:?usage: build.sh VERSION OUT_ZIP}

fail() {
	echo "build.sh: $*" >&2
	exit 1
}

bundle_version=${version#v}
# The version goes into Info.plist as it is, so it may hold no markup.
case "$version" in
v[0-9]*) ;;
*) fail "version must look like v1.2.3, got $version" ;;
esac
case "$bundle_version" in
*[!A-Za-z0-9.-]*) fail "version may hold only letters, digits, dots and dashes, got $version" ;;
esac
[ "$(uname -s)" = Darwin ] || fail "the menu bar app builds only on macOS"

# OUT_ZIP and the notary key are relative to the caller's folder, not this
# one.
case "$zip" in
/*) ;;
*) zip=$PWD/$zip ;;
esac
case "${AI_USAGE_NOTARY_KEY:-}" in
"" | /*) ;;
*) export AI_USAGE_NOTARY_KEY="$PWD/$AI_USAGE_NOTARY_KEY" ;;
esac

here=$(cd "$(dirname "$0")" && pwd)
build=$here/.build
app=$build/app/AI\ Usage.app
cd "$here"

# Both CPUs build at once, each in a scratch path of its own and with its
# output in a log of its own, printed once it ends.
mkdir -p "$build"
build_arch() {
	xcrun swift build -c release --product AIUsageBar --arch "$1" --scratch-path ".build/$1" \
		>"$build/swift-$1.log" 2>&1
}
failed=
# ARCH PID: wait for the build, print its log, and note a failure.
finish_arch() {
	if wait "$2"; then status=0; else status=$?; fi
	echo "swift build for $1 exited $status:" >&2
	cat "$build/swift-$1.log" >&2
	[ "$status" -eq 0 ] || failed="$failed $1"
}
echo "building AIUsageBar for arm64 and x86_64" >&2
build_arch arm64 &
pid_arm64=$!
build_arch x86_64 &
pid_x86_64=$!
finish_arch arm64 "$pid_arm64"
finish_arch x86_64 "$pid_x86_64"
[ -z "$failed" ] || fail "swift build failed for$failed"
set --
for arch in arm64 x86_64; do
	bin=$(xcrun swift build -c release --product AIUsageBar --arch "$arch" --scratch-path ".build/$arch" --show-bin-path)
	set -- "$@" "$bin/AIUsageBar"
done

echo "drawing the icon" >&2
mkdir -p "$build/icon"
xcrun swiftc -O -sdk "$(xcrun --show-sdk-path)" Icon/AppIcon.swift -o "$build/icon/appicon" ||
	fail "the icon program does not build"
rm -rf "$build/icon/AppIcon.iconset"
"$build/icon/appicon" "$build/icon/AppIcon.iconset"

echo "assembling AI Usage.app $bundle_version" >&2
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
lipo -create -output "$app/Contents/MacOS/AIUsageBar" "$@"
iconutil -c icns -o "$app/Contents/Resources/AppIcon.icns" "$build/icon/AppIcon.iconset"
cat >"$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleDisplayName</key>
	<string>AI Usage</string>
	<key>CFBundleExecutable</key>
	<string>AIUsageBar</string>
	<key>CFBundleIconFile</key>
	<string>AppIcon</string>
	<key>CFBundleIdentifier</key>
	<string>io.github.neoromantic.ai-usage.bar</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>AI Usage</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$bundle_version</string>
	<key>CFBundleVersion</key>
	<string>$bundle_version</string>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.developer-tools</string>
	<key>LSMinimumSystemVersion</key>
	<string>14.0</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSHumanReadableCopyright</key>
	<string>Copyright © 2026 Sergey Petrov. MIT License.</string>
</dict>
</plist>
EOF
plutil -lint -s "$app/Contents/Info.plist" || fail "Info.plist is not valid"

identity=${AI_USAGE_SIGN_IDENTITY:-}
if [ -n "$identity" ]; then
	echo "signing with $identity" >&2
	codesign --force --options runtime --timestamp --sign "$identity" "$app"
else
	codesign --force --sign - "$app"
fi
codesign --verify --deep --strict "$app" || fail "the signature does not verify"
if [ -n "${AI_USAGE_NOTARY_KEY:-}" ]; then
	[ -n "$identity" ] || fail "notarizing needs AI_USAGE_SIGN_IDENTITY"
	sh "$here/../.github/scripts/notarize.sh" "$app"
	xcrun stapler staple "$app" || fail "the ticket does not staple"
fi

mkdir -p "$(dirname "$zip")"
rm -f "$zip"
ditto -c -k --norsrc --keepParent "$app" "$zip"
echo "wrote $zip" >&2

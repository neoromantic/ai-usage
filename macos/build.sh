#!/bin/sh
# Build the menu bar app as a universal, ad-hoc signed "AI Usage.app" and
# zip it the way a release ships it. Needs macOS with Xcode or the Command
# Line Tools; runs from any folder.
#
#   sh macos/build.sh VERSION OUT_ZIP
#
# VERSION is the release tag, such as v0.3.0; the bundle's version is the
# tag without its v. The files on the way go to macos/.build.
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

# OUT_ZIP is relative to the caller's folder, not this one.
case "$zip" in
/*) ;;
*) zip=$PWD/$zip ;;
esac

here=$(cd "$(dirname "$0")" && pwd)
build=$here/.build
app=$build/app/AI\ Usage.app
cd "$here"

set --
for arch in arm64 x86_64; do
	echo "building AIUsageBar for $arch" >&2
	xcrun swift build -c release --product AIUsageBar --arch "$arch" --scratch-path ".build/$arch" ||
		fail "swift build failed for $arch"
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

codesign --force --sign - "$app"
codesign --verify --deep --strict "$app" || fail "the signature does not verify"

mkdir -p "$(dirname "$zip")"
rm -f "$zip"
ditto -c -k --norsrc --keepParent "$app" "$zip"
echo "wrote $zip" >&2

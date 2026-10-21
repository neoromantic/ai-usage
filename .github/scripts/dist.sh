#!/bin/sh
# Build the release files: one binary per OS and CPU, named the way
# selfupdate and the install scripts look for them, the macOS menu bar app,
# and checksums.txt. Run it from the repository's root.
#
#   .github/scripts/dist.sh VERSION [OUTDIR]
#
# AI_USAGE_RELAY_URL, when set, becomes the binaries' default relay.
# TARGETS narrows the build, for example TARGETS=linux/amd64. The target
# darwin/app is the menu bar app, ai-usage_darwin_app.zip, which builds only
# on macOS; elsewhere it is skipped.
#
# AI_USAGE_SIGN_IDENTITY, a Developer ID Application identity in the
# keychain, signs the macOS binaries and the app with the hardened runtime,
# instead of ad hoc. With AI_USAGE_NOTARY_KEY too, Apple notarizes them all
# before the checksums are taken, and the app gets its ticket stapled; see
# .github/scripts/notarize.sh and macos/build.sh. The release does both.
# The app builds and is notarized in the background, at the same time as the
# binaries.
#
# Release files already in OUTDIR for targets this run does not build stay,
# and checksums.txt covers them too: the release's Linux job builds the Linux
# and Windows binaries, and the macOS job builds the rest beside them.
set -eu

version=${1:?usage: dist.sh VERSION [OUTDIR]}
out=${2:-dist}
relay=${AI_USAGE_RELAY_URL:-}
identity=${AI_USAGE_SIGN_IDENTITY:-}
targets=${TARGETS:-darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/app}

fail() {
	echo "dist.sh: $*" >&2
	exit 1
}

# A version that is not vX.Y.Z still builds, but that binary never updates itself.
case "$version" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) fail "version must look like v1.2.3, got $version" ;;
esac
# The URL goes inside -ldflags, where a space or a quote would split it.
case "$relay" in
*[[:space:]\"\']*) fail "AI_USAGE_RELAY_URL must not contain spaces or quotes" ;;
"" | https://*) ;;
*) fail "AI_USAGE_RELAY_URL must start with https://" ;;
esac
if [ -n "${AI_USAGE_NOTARY_KEY:-}" ] && [ -z "$identity" ]; then
	fail "notarizing needs AI_USAGE_SIGN_IDENTITY"
fi

mkdir -p "$out"
rm -f "$out/checksums.txt"

# The menu bar app builds, and is notarized, in the background while the
# binaries build and are notarized, with its output in a log printed when it
# ends. A failure on the way stops it.
tmp=$(mktemp -d)
app_pid=
# PID and every process under it, such as swift build's compilers.
kill_tree() {
	for child in $(pgrep -P "$1"); do
		kill_tree "$child"
	done
	kill "$1" 2>/dev/null || true
}
cleanup() {
	if [ -n "$app_pid" ]; then
		kill_tree "$app_pid"
		echo "dist.sh: stopped the menu bar app's build; its output so far:" >&2
		cat "$tmp/app.log" >&2
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT
case " $targets " in
*" darwin/app "*)
	if [ "$(uname -s)" = Darwin ]; then
		echo "building ai-usage_darwin_app.zip in the background" >&2
		rm -f "$out/ai-usage_darwin_app.zip"
		# The full path, since build.sh need not work in this folder.
		sh macos/build.sh "$version" "$(cd "$out" && pwd)/ai-usage_darwin_app.zip" >"$tmp/app.log" 2>&1 &
		app_pid=$!
	else
		echo "skipping ai-usage_darwin_app.zip: the menu bar app builds only on macOS" >&2
	fi
	;;
esac

# The positional parameters collect the signed binaries.
set --
for target in $targets; do
	if [ "$target" = darwin/app ]; then
		continue
	fi
	goos=${target%/*}
	goarch=${target#*/}
	name=ai-usage_${goos}_${goarch}
	if [ "$goos" = windows ]; then
		name=$name.exe
	fi
	echo "building $name" >&2
	CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
		-ldflags "-s -w -X main.version=$version -X main.defaultRelay=$relay" \
		-o "$out/$name" ./cmd/ai-usage
	# One identifier for every release and CPU: with the team, it is what
	# launchd on macOS 27 is expected to pin the launch agent to, so updates
	# should keep the pin.
	if [ "$goos" = darwin ] && [ -n "$identity" ]; then
		codesign --force --options runtime --timestamp --identifier io.github.neoromantic.ai-usage \
			--sign "$identity" "$out/$name"
		codesign --verify --strict "$out/$name" || fail "the signature of $name does not verify"
		set -- "$@" "$out/$name"
	fi
done
if [ "$#" -gt 0 ] && [ -n "${AI_USAGE_NOTARY_KEY:-}" ]; then
	sh .github/scripts/notarize.sh "$@"
fi

if [ -n "$app_pid" ]; then
	echo "waiting for ai-usage_darwin_app.zip" >&2
	pid=$app_pid
	app_pid=
	if wait "$pid"; then status=0; else status=$?; fi
	echo "macos/build.sh exited $status:" >&2
	cat "$tmp/app.log" >&2
	[ "$status" -eq 0 ] || fail "the menu bar app did not build"
fi

# Taken last, when every file is signed and notarized.
# "sha256  file" lines, which selfupdate and both installers read.
cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum ai-usage_* >checksums.txt
else
	shasum -a 256 ai-usage_* >checksums.txt
fi
cat checksums.txt >&2

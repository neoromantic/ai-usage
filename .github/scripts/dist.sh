#!/bin/sh
# Build the release files: one binary per OS and CPU, named the way
# selfupdate and the install scripts look for them, and checksums.txt.
#
#   .github/scripts/dist.sh VERSION [OUTDIR]
#
# AI_USAGE_RELAY_URL, when set, becomes the binaries' default relay.
# TARGETS narrows the build, for example TARGETS=linux/amd64.
set -eu

version=${1:?usage: dist.sh VERSION [OUTDIR]}
out=${2:-dist}
relay=${AI_USAGE_RELAY_URL:-}
targets=${TARGETS:-darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64}

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

mkdir -p "$out"
rm -f "$out"/ai-usage_* "$out/checksums.txt"
for target in $targets; do
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
done

# "sha256  file" lines, which selfupdate and both installers read.
cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum ai-usage_* >checksums.txt
else
	shasum -a 256 ai-usage_* >checksums.txt
fi
cat checksums.txt >&2

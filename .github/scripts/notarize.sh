#!/bin/sh
# Send signed files to Apple's notary service in one zip and wait for its
# verdict. A bundle, such as the menu bar app, can then be stapled; a bare
# binary cannot, and Gatekeeper looks its ticket up online instead.
#
#   .github/scripts/notarize.sh FILE...
#
# It authenticates with an App Store Connect API key: AI_USAGE_NOTARY_KEY is
# the path to its .p8 file, AI_USAGE_NOTARY_KEY_ID its ID, and
# AI_USAGE_NOTARY_ISSUER its issuer, which a team key needs. It fails, with
# Apple's log, unless Apple accepts every file.
set -eu

fail() {
	echo "notarize.sh: $*" >&2
	exit 1
}

[ "$#" -gt 0 ] || fail "usage: notarize.sh FILE..."
key=${AI_USAGE_NOTARY_KEY:-}
key_id=${AI_USAGE_NOTARY_KEY_ID:-}
[ -f "$key" ] || fail "AI_USAGE_NOTARY_KEY must name the API key's .p8 file"
[ -n "$key_id" ] || fail "AI_USAGE_NOTARY_KEY_ID must hold the API key's ID"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/files"
for f in "$@"; do
	[ -e "$f" ] || fail "$f does not exist"
	ditto "$f" "$tmp/files/$(basename "$f")"
done
ditto -c -k --sequesterRsrc "$tmp/files" "$tmp/submit.zip"
echo "notarizing $*" >&2
set -- --key "$key" --key-id "$key_id"
if [ -n "${AI_USAGE_NOTARY_ISSUER:-}" ]; then
	set -- "$@" --issuer "$AI_USAGE_NOTARY_ISSUER"
fi

# notarytool can exit 0 for a submission Apple rejected, so the status
# decides.
xcrun notarytool submit "$tmp/submit.zip" "$@" --wait --timeout 30m --output-format json >"$tmp/result.json" ||
	echo "notarize.sh: notarytool submit failed" >&2
id=$(plutil -extract id raw -o - "$tmp/result.json" 2>/dev/null) || id=
status=$(plutil -extract status raw -o - "$tmp/result.json" 2>/dev/null) || status=
echo "submission ${id:-unknown}: ${status:-no status}" >&2
if [ "$status" != Accepted ]; then
	cat "$tmp/result.json" >&2
	[ -z "$id" ] || xcrun notarytool log "$id" "$@" >&2 || true
	fail "Apple did not accept the files"
fi

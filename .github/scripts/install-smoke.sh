#!/bin/sh
# Install a locally built release through the install script, the way a person
# would, and check the result. CI runs it on macOS, Linux, and Windows.
#
#   .github/scripts/install-smoke.sh
#
# It builds this machine's release file, serves it on 127.0.0.1, and points the
# installer there instead of GitHub. The build is a development version, and
# AI_USAGE_NO_SCHEDULE is set, so the first run neither registers with the
# system scheduler nor updates itself. On Windows it then registers the task
# with `schedule install`, checks it, and removes it. Work files go under
# $RUNNER_TEMP.
set -eu

fail() {
	echo "install-smoke: $*" >&2
	exit 1
}

root=$(cd "$(dirname "$0")/../.." && pwd)
base=${RUNNER_TEMP:?RUNNER_TEMP must name a scratch directory}
# Git Bash on Windows: hand native programs D:/a/... rather than /d/a/...
if command -v cygpath >/dev/null 2>&1; then
	root=$(cygpath -m "$root")
	base=$(cygpath -m "$base")
fi
work=$base/install-smoke
port=${PORT:-8765}
rm -rf "$work"
mkdir -p "$work"
cd "$root"

goos=$(go env GOOS)
goarch=$(go env GOARCH)
exe=$(go env GOEXE)
version=v0.0.0-smoke
TARGETS="$goos/$goarch" sh .github/scripts/dist.sh "$version" "$work/dist"
built="$work/dist/ai-usage_${goos}_${goarch}$exe"

go build -o "$work/serve$exe" .github/scripts/serve.go
"$work/serve$exe" "127.0.0.1:$port" "$work/dist" &
server=$!
trap 'kill "$server" 2>/dev/null || true' EXIT
up=0
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
	if curl -fs -o /dev/null "http://127.0.0.1:$port/checksums.txt"; then
		up=1
		break
	fi
	sleep 1
done
[ "$up" = 1 ] || fail "the local server did not start"

export AI_USAGE_NO_SCHEDULE=1
key=$(AI_USAGE_HOME="$work/other" "$built" team key)
export AI_USAGE_DOWNLOAD_URL="http://127.0.0.1:$port"
export AI_USAGE_HOME="$work/home"
export AI_USAGE_BIN_DIR="$work/bin"
# Nothing listens there: the first run must record the relay error and succeed.
export AI_USAGE_RELAY=http://127.0.0.1:9
export AI_USAGE_TEAM_KEY="$key"

if [ "$goos" = windows ]; then
	# The documented `irm | iex` path under Windows PowerShell 5.1, then an
	# upgrade over it under PowerShell 7.
	powershell -NoProfile -Command 'Get-Content -Raw install.ps1 | Invoke-Expression'
	pwsh -NoProfile -ExecutionPolicy Bypass -File install.ps1
	bin="$work/bin/ai-usage.exe"
	# $env: is PowerShell's to expand.
	# shellcheck disable=SC2016
	pwsh -NoProfile -Command 'if (([Environment]::GetEnvironmentVariable("Path", "User") -split ";") -notcontains $env:AI_USAGE_BIN_DIR) { exit 1 }' ||
		fail "the install folder is not on the user PATH"
else
	# Under sudo the installer stops before it downloads or writes anything.
	mkdir -p "$work/fake-root"
	printf '#!/bin/sh\necho 0\n' >"$work/fake-root/id"
	chmod +x "$work/fake-root/id"
	if PATH="$work/fake-root:$PATH" SUDO_USER=someone sh install.sh 2>"$work/sudo.err"; then
		fail "the installer ran under sudo"
	fi
	grep -q "without sudo" "$work/sudo.err" || fail "the installer did not say why it refused sudo"
	[ ! -e "$work/bin" ] || fail "the installer wrote files under sudo"

	sh install.sh
	# Running it again upgrades in place.
	sh install.sh
	bin="$work/bin/ai-usage"
fi

unset AI_USAGE_RELAY AI_USAGE_TEAM_KEY
[ "$("$bin" version)" = "$version" ] || fail "installed binary reports $("$bin" version), want $version"
[ "$("$bin" team key)" = "$key" ] || fail "the installer did not join the team"
[ "$("$bin" relay show)" = http://127.0.0.1:9 ] || fail "the installer did not save the relay"
"$bin" status
"$bin" report --json >"$work/report.json"
grep -q '"schema_version": 2' "$work/report.json" || fail "report is not schema version 2"

if [ "$goos" = windows ]; then
	# Register with the real Task Scheduler from the task definition, check
	# that it runs on battery, and remove it. Git Bash would turn /Query
	# into a path without MSYS_NO_PATHCONV.
	"$bin" schedule install
	"$bin" schedule status | grep -q '^registered: ' || fail "the task does not run this binary and state folder"
	MSYS_NO_PATHCONV=1 schtasks /Query /TN ai-usage /XML >"$work/task.xml"
	cat "$work/task.xml"
	grep -q '<DisallowStartIfOnBatteries>false' "$work/task.xml" || fail "the task starts only on AC power"
	grep -q '<StopIfGoingOnBatteries>false' "$work/task.xml" || fail "the task stops on battery"
	"$bin" schedule remove
	if MSYS_NO_PATHCONV=1 schtasks /Query /TN ai-usage >/dev/null 2>&1; then
		fail "schedule remove left the task"
	fi
fi
echo "install-smoke: ok" >&2

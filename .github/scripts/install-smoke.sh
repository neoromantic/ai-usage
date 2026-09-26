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
# with `schedule install`, checks it, and removes it. On macOS and Linux it
# then installs into homes of its own under $RUNNER_TEMP, to check which
# folder the binary goes to and which shell profile puts it on PATH. Work
# files go under $RUNNER_TEMP.
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

# The collector also reads the harness homes these name, so a local run
# with a fake HOME would still read the real ones.
unset CLAUDE_CONFIG_DIR CODEX_HOME GROK_HOME HERMES_HOME
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
grep -Eq '"schema_version": [0-9]+,' "$work/report.json" || fail "report --json has no schema_version"

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
else
	# Without AI_USAGE_BIN_DIR the installer picks the folder and may edit a
	# shell profile. Each case gets a home of its own and only the system's
	# folders on PATH, so no real profile or earlier install is involved.
	unset AI_USAGE_BIN_DIR ZDOTDIR XDG_CONFIG_HOME
	sys=/usr/bin:/bin:/usr/sbin:/sbin

	# A folder already on PATH is used, and an upgrade stays in it even when
	# a folder that comes first appears on PATH.
	h=$work/on-path
	mkdir -p "$h/bin" "$h/.local/bin"
	HOME=$h PATH=$h/bin:$sys SHELL=/bin/bash sh install.sh
	HOME=$h PATH=$h/.local/bin:$h/bin:$sys SHELL=/bin/bash sh install.sh
	[ -x "$h/bin/ai-usage" ] || fail "the installer did not use the folder on PATH"
	[ ! -e "$h/.local/bin/ai-usage" ] || fail "the upgrade installed a second copy"
	if [ -e "$h/.bash_profile" ] || [ -e "$h/.bashrc" ]; then
		fail "the installer edited a profile with the folder already on PATH"
	fi

	# Another program named ai-usage, in a tool's folder that comes first, is
	# left alone. The binary goes into the usual folder, and the installer
	# says that the other one still runs by name.
	h=$work/other-program
	mkdir -p "$h/.cargo/bin" "$h/.local/bin"
	printf '#!/bin/sh\necho other\n' >"$h/.cargo/bin/ai-usage"
	chmod +x "$h/.cargo/bin/ai-usage"
	HOME=$h PATH=$h/.cargo/bin:$h/.local/bin:$sys SHELL=/bin/bash sh install.sh 2>"$work/other-program.err"
	[ "$("$h/.cargo/bin/ai-usage")" = other ] || fail "the installer replaced another program named ai-usage"
	[ -x "$h/.local/bin/ai-usage" ] || fail "the installer did not install into ~/.local/bin beside another program"
	grep -q 'comes first on PATH' "$work/other-program.err" || fail "the installer did not say another ai-usage comes first on PATH"

	# A link that comes first but leads to the copy just installed runs that
	# copy, so there is nothing to say.
	h=$work/link-first
	mkdir -p "$h/links" "$h/.local/bin"
	ln -s "$h/.local/bin/ai-usage" "$h/links/ai-usage"
	HOME=$h PATH=$h/links:$h/.local/bin:$sys SHELL=/bin/zsh sh install.sh 2>"$work/link-first.err"
	if grep -q 'comes first on PATH' "$work/link-first.err"; then
		fail "the installer said a link to the copy just installed comes first on PATH"
	fi

	# A link, such as Homebrew's, is not replaced, even one to this program.
	h=$work/link
	mkdir -p "$h/brew/bin"
	ln -s "$built" "$h/brew/bin/ai-usage"
	HOME=$h PATH=$h/brew/bin:$sys SHELL=/bin/zsh sh install.sh
	[ -L "$h/brew/bin/ai-usage" ] || fail "the installer replaced a link named ai-usage"
	[ -x "$h/.local/bin/ai-usage" ] || fail "the installer did not install into ~/.local/bin beside a link"

	# With none on PATH, the binary goes into ~/.local/bin, and the login
	# shell's profile puts that on PATH once, however often the installer
	# runs. A bash login shell on macOS reads the first profile that exists.
	for shell in bash zsh; do
		h=$work/$shell
		mkdir -p "$h"
		case $goos/$shell in
		darwin/bash)
			: >"$h/.profile"
			profile=$h/.profile
			;;
		*/bash) profile=$h/.bashrc ;;
		*) profile=$h/.zshrc ;;
		esac
		HOME=$h PATH=$sys SHELL=/bin/$shell sh install.sh
		HOME=$h PATH=$sys SHELL=/bin/$shell sh install.sh
		[ "$(grep -c ai-usage "$profile")" = 1 ] || fail "$profile does not have one ai-usage block"
		found=$(HOME=$h PATH=$sys sh -c '. "$1" && command -v ai-usage' sh "$profile" || true)
		[ "$found" = "$h/.local/bin/ai-usage" ] || fail "$profile puts $found on PATH, not ~/.local/bin/ai-usage"
	done
	[ ! -e "$work/bash/.bash_profile" ] || fail "the installer made a .bash_profile that hides .profile"

	# AI_USAGE_NO_MODIFY_PATH leaves profiles alone and says what to add.
	h=$work/no-modify
	mkdir -p "$h"
	HOME=$h PATH=$sys SHELL=/bin/zsh AI_USAGE_NO_MODIFY_PATH=1 sh install.sh 2>"$work/no-modify.err"
	[ -x "$h/.local/bin/ai-usage" ] || fail "the installer did not fall back to ~/.local/bin"
	[ ! -e "$h/.zshrc" ] || fail "AI_USAGE_NO_MODIFY_PATH did not leave the profile alone"
	grep -q 'export PATH=' "$work/no-modify.err" || fail "the installer did not say how to put ~/.local/bin on PATH"

	# So does a shell whose profile the installer does not know, and root
	# under sudo, whose HOME can be the person's.
	h=$work/tcsh
	mkdir -p "$h"
	HOME=$h PATH=$sys SHELL=/bin/tcsh sh install.sh 2>"$work/tcsh.err"
	[ ! -e "$h/.profile" ] || fail "the installer wrote ~/.profile, which tcsh does not read"
	grep -q 'export PATH=' "$work/tcsh.err" || fail "the installer did not say what to add for tcsh"
	h=$work/sudo-home
	mkdir -p "$h"
	HOME=$h PATH=$work/fake-root:$sys SUDO_USER=someone AI_USAGE_ALLOW_ROOT=1 SHELL=/bin/fish sh install.sh 2>"$work/sudo-home.err"
	[ ! -e "$h/.config/fish" ] || fail "the installer edited a profile as root under sudo"
	grep -q 'export PATH=' "$work/sudo-home.err" || fail "the installer did not say what to add under sudo"
fi
echo "install-smoke: ok" >&2

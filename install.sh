#!/bin/sh
# Install the latest ai-usage release on macOS or Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh | sh
#
# Environment, all optional:
#   AI_USAGE_BIN_DIR         where the binary goes (default: see below)
#   AI_USAGE_NAME            this device's name in the team (default: the host name)
#   AI_USAGE_RELAY           relay URL to save before the first run
#   AI_USAGE_TEAM_KEY        team key to join before the first run
#   AI_USAGE_DOWNLOAD_URL    where release files are fetched from (mirrors, tests)
#   AI_USAGE_ALLOW_ROOT      set to install for root from a sudo shell
#   AI_USAGE_NO_MODIFY_PATH  set to leave shell profiles alone
#   AI_USAGE_NO_APP          set to skip the menu bar app on macOS
#
# The script downloads the release file for this OS and CPU, checks it against
# the release's checksums.txt, installs it, and runs it once. That first run
# registers the collector with the system scheduler (cron on Linux, launchd
# on macOS). On macOS 14 and later, the script then installs the menu bar
# app from the same release into ~/Applications/AI Usage.app and opens it;
# the app is for the person at the screen, so root never gets it. Running the
# script again upgrades in place.
#
# The binary goes where an earlier install is, else into the first of
# ~/.local/bin, ~/bin, /opt/homebrew/bin, and /usr/local/bin that is on PATH,
# writable, and holds no other program named ai-usage. With none, it goes
# into ~/.local/bin, and a marked block in the login shell's profile puts
# that folder on PATH for new terminals.

# Everything is inside main so a partly downloaded script does nothing.
main() {
	set -eu

	# Under sudo the first run would register root's schedule and collect
	# root's usage, and could leave root-owned files in this person's home.
	sudo_user=${SUDO_USER:-${DOAS_USER:-}}
	under_sudo=
	if [ "$(id -u)" = 0 ] && [ -n "$sudo_user" ] && [ "$sudo_user" != root ]; then
		[ -n "${AI_USAGE_ALLOW_ROOT:-}" ] || fail "run the installer as $sudo_user, without sudo; it installs for the user who runs it (AI_USAGE_ALLOW_ROOT=1 installs for root)"
		under_sudo=1
	fi

	repo="neoromantic/ai-usage"
	base="${AI_USAGE_DOWNLOAD_URL:-https://github.com/$repo/releases/latest/download}"
	base="${base%/}"
	bin_dir="${AI_USAGE_BIN_DIR:-$(find_bin_dir)}"
	bin="$bin_dir/ai-usage"
	# find_bin_dir falls back to ~/.local/bin even when another program by
	# this name is there; that one is not the installer's to replace.
	if [ -z "${AI_USAGE_BIN_DIR:-}" ] && taken "$bin"; then
		fail "$bin is another program or a link; set AI_USAGE_BIN_DIR to install ai-usage elsewhere"
	fi

	os=$(detect_os)
	arch=$(detect_arch "$os")
	asset="ai-usage_${os}_${arch}"
	app_asset=ai-usage_darwin_app.zip

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t ai-usage)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	say "downloading $asset"
	fetch "$base/$asset" "$tmp/$asset"
	fetch "$base/checksums.txt" "$tmp/checksums.txt"

	want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print tolower($1); exit }' "$tmp/checksums.txt")
	[ -n "$want" ] || fail "checksums.txt does not list $asset"
	got=$(sha256 "$tmp/$asset")
	[ "$got" = "$want" ] || fail "$asset does not match its checksum (got $got, want $want)"

	# The menu bar app comes now, from the release the binary does: a
	# release published during the first run would not match these
	# checksums. It is installed after that run. The subshells keep a failure
	# to the app.
	if [ "$os" = darwin ] && [ -z "${AI_USAGE_NO_APP:-}" ] && [ "$(id -u)" != 0 ]; then
		(fetch_app) || say "the menu bar app is not installed; ai-usage works without it"
	fi

	mkdir -p "$bin_dir"
	# Copy beside the target and rename, so a scheduled run never sees half a file.
	cp "$tmp/$asset" "$bin_dir/.ai-usage.new.$$"
	chmod 755 "$bin_dir/.ai-usage.new.$$"
	mv -f "$bin_dir/.ai-usage.new.$$" "$bin"
	version=$("$bin" version) || fail "$bin is installed but does not run"
	say "installed $version to $bin"

	if [ -n "${AI_USAGE_NAME:-}" ]; then
		"$bin" name set "$AI_USAGE_NAME"
	fi
	if [ -n "${AI_USAGE_RELAY:-}" ]; then
		"$bin" relay set "$AI_USAGE_RELAY"
	fi
	if [ -n "${AI_USAGE_TEAM_KEY:-}" ]; then
		# The key goes through stdin so it never shows in the process list.
		printf '%s\n' "$AI_USAGE_TEAM_KEY" | "$bin" team join
	fi

	say "first run: collecting and registering with the system scheduler"
	# Input that is not a terminal prints the report rather than open the
	# interactive view, which would hold the installer until it is closed.
	if ! "$bin" </dev/null; then
		fail "the first run failed; the binary is installed, run $bin to retry"
	fi

	# After the first run, so the app finds a report and the launch agent
	# that names the binary.
	if [ -f "$tmp/$app_asset" ]; then
		(install_app) || say "the menu bar app is not installed; ai-usage works without it"
	fi

	# find_bin_dir picks a folder on PATH or ~/.local/bin, so only
	# ~/.local/bin can be off PATH here, unless AI_USAGE_BIN_DIR named another.
	# Under sudo HOME can still be the person's, whose profile root must not write.
	if on_path "$bin_dir"; then
		# A copy the installer could not replace, or would not, still runs
		# when ai-usage is typed if its folder comes first. A link to the
		# copy just installed is that copy, which -ef sees.
		found=$(command -v ai-usage || true)
		# POSIX has had -ef since 2024; older shellcheck does not know that.
		# shellcheck disable=SC3013
		if [ ! "$found" -ef "$bin" ]; then
			say "$found comes first on PATH and is not the copy just installed; remove it or put $bin_dir first"
		fi
	elif [ -z "${AI_USAGE_BIN_DIR:-}${AI_USAGE_NO_MODIFY_PATH:-}$under_sudo" ] && add_to_path "$os"; then
		say "a new terminal finds ai-usage by name; in this one, run $bin"
	else
		say "$bin_dir is not on PATH; add it to your shell profile:
  export PATH=\"$bin_dir:\$PATH\""
	fi
}

# fetch_app downloads the menu bar app of the release in $tmp/checksums.txt
# to $tmp/$app_asset and checks it, on macOS 14 and later. main runs it and
# install_app in subshells, where set -e is off and fail ends only the
# subshell, so each step checks its own result.
fetch_app() {
	macos=$(sw_vers -productVersion 2>/dev/null || true)
	major=${macos%%.*}
	case $major in
	'' | *[!0-9]*) fail "cannot tell this Mac's macOS version" ;;
	esac
	if [ "$major" -lt 14 ]; then
		say "the menu bar app needs macOS 14 or later; this Mac has $macos"
		return 0
	fi
	want=$(awk -v f="$app_asset" '$2 == f || $2 == "*" f { print tolower($1); exit }' "$tmp/checksums.txt")
	if [ -z "$want" ]; then
		say "this release has no menu bar app"
		return 0
	fi
	say "downloading $app_asset"
	fetch "$base/$app_asset" "$tmp/app.part"
	got=$(sha256 "$tmp/app.part")
	[ "$got" = "$want" ] || fail "$app_asset does not match its checksum (got $got, want $want)"
	mv "$tmp/app.part" "$tmp/$app_asset" || fail "cannot write $tmp/$app_asset"
}

# install_app puts the menu bar app fetch_app downloaded in ~/Applications,
# in place of an earlier copy, and opens it when someone is logged in at the
# screen.
install_app() {
	ditto -x -k "$tmp/$app_asset" "$tmp/app" || fail "cannot unpack $app_asset"
	[ -d "$tmp/app/AI Usage.app" ] || fail "$app_asset has no AI Usage.app"

	apps=$HOME/Applications
	app="$apps/AI Usage.app"
	mkdir -p "$apps" || fail "cannot create $apps"
	# Moved beside the old copy first, so renames swap the two, and the old
	# copy goes back when the new one cannot take its place. Self-update
	# stages under names of its own; what a stopped install left under these
	# goes now.
	rm -rf "$apps"/.ai-usage-install-*
	new="$apps/.ai-usage-install-$$"
	old="$apps/.ai-usage-install-old-$$"
	mv "$tmp/app/AI Usage.app" "$new" || fail "cannot write into $apps"
	if [ -e "$app" ] && ! mv "$app" "$old"; then
		rm -rf "$new"
		fail "cannot replace $app"
	fi
	if ! mv "$new" "$app"; then
		if [ -e "$old" ]; then
			mv "$old" "$app"
		fi
		rm -rf "$new"
		fail "cannot replace $app"
	fi
	rm -rf "$old"
	app_version=$(plutil -extract CFBundleShortVersionString raw -o - "$app/Contents/Info.plist" 2>/dev/null || echo "of unknown version")
	say "installed the menu bar app, AI Usage $app_version, to $app"

	# A copy that runs keeps its old code; quit it, and wait until it has
	# gone, since the new one quits when it finds another running.
	uid=$(id -u)
	if pkill -x -U "$uid" AIUsageBar; then
		for _ in 1 2 3 4 5 6 7 8 9 10; do
			pgrep -x -U "$uid" AIUsageBar >/dev/null || break
			sleep 1
		done
	fi
	# Over SSH with no one logged in at the screen, there is nowhere to open it.
	if ! launchctl print "gui/$uid" >/dev/null 2>&1; then
		say "log in at the screen and open $app once; from then on it starts at login"
	elif open "$app"; then
		say "the app is in the menu bar; it starts at login and updates with ai-usage"
	else
		say "could not open $app; open it in Finder"
	fi
}

# find_bin_dir prints the folder to install into. An upgrade replaces the
# earlier binary where it is, so the scheduler entry keeps pointing at it.
# A new install goes into a folder on PATH that is meant for a person's own
# programs; a tool's own folder, such as ~/.cargo/bin or a node version
# manager's, belongs to that tool. Neither replaces another program named
# ai-usage, or a link such as Homebrew's or a version manager's shim.
find_bin_dir() {
	home=${HOME:?HOME is not set}
	for f in "$home/.local/bin/ai-usage" "$(command -v ai-usage || true)"; do
		case $f in /*) ;; *) continue ;; esac
		if ours "$f" && [ -w "${f%/*}" ]; then
			echo "${f%/*}"
			return
		fi
	done
	for d in "$home/.local/bin" "$home/bin" /opt/homebrew/bin /usr/local/bin; do
		if on_path "$d" && [ -d "$d" ] && [ -w "$d" ] && ! taken "$d/ai-usage"; then
			echo "$d"
			return
		fi
	done
	echo "$home/.local/bin"
}

# ours reports whether $1 is a plain file of this program: Go builds the
# module path into the binary. A link is left to whatever made it.
ours() {
	[ -f "$1" ] && [ ! -L "$1" ] && grep -q 'github.com/neoromantic/ai-usage' "$1" 2>/dev/null
}

# taken reports whether $1 holds something the installer must not replace.
taken() {
	{ [ -e "$1" ] || [ -L "$1" ]; } && ! ours "$1"
}

on_path() {
	case ":${PATH:-}:" in
	*":$1:"*) return 0 ;;
	esac
	return 1
}

# add_to_path puts ~/.local/bin on PATH in the profile of the login shell,
# once. The block names $HOME, not this home's path, so a profile shared
# between machines still works. It fails for a shell whose profile it does
# not know, such as tcsh or nushell, which never read ~/.profile.
add_to_path() {
	mark="# Added by the ai-usage installer"
	line="export PATH=\"\$HOME/.local/bin:\$PATH\""
	shell=${SHELL:-}
	case ${shell##*/} in
	zsh) profile="${ZDOTDIR:-$HOME}/.zshrc" ;;
	bash)
		profile="$HOME/.bashrc"
		if [ "$1" = darwin ]; then
			# A macOS terminal opens a login shell, and a bash login shell
			# reads only the first of these that exists.
			profile="$HOME/.bash_profile"
			for f in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
				if [ -e "$f" ]; then
					profile=$f
					break
				fi
			done
		fi
		;;
	fish)
		profile="${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/ai-usage.fish"
		line="contains -- \$HOME/.local/bin \$PATH; or set -gx PATH \$HOME/.local/bin \$PATH"
		;;
	sh | dash | ash | ksh | mksh) profile="$HOME/.profile" ;;
	*) return 1 ;;
	esac

	if grep -qsF "$mark" "$profile"; then
		say "$profile already puts $HOME/.local/bin on PATH"
		return
	fi
	mkdir -p "${profile%/*}" || return
	if [ -s "$profile" ]; then
		echo >>"$profile" || return
	fi
	printf '%s\n' "$mark" "$line" >>"$profile" || return
	say "added $HOME/.local/bin to PATH in $profile"
}

say() { printf 'ai-usage install: %s\n' "$*" >&2; }

fail() {
	say "$*"
	exit 1
}

detect_os() {
	case "$(uname -s)" in
	Darwin) echo darwin ;;
	Linux) echo linux ;;
	MINGW* | MSYS* | CYGWIN* | Windows_NT) fail "on Windows, use install.ps1 from PowerShell" ;;
	*) fail "unsupported OS: $(uname -s)" ;;
	esac
}

detect_arch() {
	m=$(uname -m)
	# A shell under Rosetta reports x86_64 on Apple silicon; the native build is better.
	if [ "$1" = darwin ] && [ "$m" = x86_64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
		m=arm64
	fi
	case "$m" in
	x86_64 | amd64) echo amd64 ;;
	arm64 | aarch64 | armv8*) echo arm64 ;;
	*) fail "unsupported CPU: $m (releases are built for amd64 and arm64)" ;;
	esac
}

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1" || fail "download failed: $1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1" || fail "download failed: $1"
	else
		fail "curl or wget is required"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sum=$(sha256sum "$1")
	elif command -v shasum >/dev/null 2>&1; then
		sum=$(shasum -a 256 "$1")
	elif command -v openssl >/dev/null 2>&1; then
		sum=$(openssl dgst -sha256 -r "$1")
	else
		fail "sha256sum, shasum, or openssl is required to check the download"
	fi
	echo "${sum%% *}" | tr 'A-F' 'a-f'
}

main "$@"

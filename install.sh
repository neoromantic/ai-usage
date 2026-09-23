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
#
# The script downloads the release file for this OS and CPU, checks it against
# the release's checksums.txt, installs it, and runs it once. That first run
# registers the collector with the system scheduler (cron on Linux, launchd
# on macOS). Running the script again upgrades in place.
#
# The binary goes where an earlier install is, else into the first of
# ~/.local/bin, ~/bin, /opt/homebrew/bin, and /usr/local/bin that is on PATH
# and writable. With none, it goes into ~/.local/bin, and a marked block in
# the login shell's profile puts that folder on PATH for new terminals.

# Everything is inside main so a partly downloaded script does nothing.
main() {
	set -eu

	# Under sudo the first run would register root's schedule and collect
	# root's usage, and could leave root-owned files in this person's home.
	sudo_user=${SUDO_USER:-${DOAS_USER:-}}
	if [ "$(id -u)" = 0 ] && [ -n "$sudo_user" ] && [ "$sudo_user" != root ] && [ -z "${AI_USAGE_ALLOW_ROOT:-}" ]; then
		fail "run the installer as $sudo_user, without sudo; it installs for the user who runs it (AI_USAGE_ALLOW_ROOT=1 installs for root)"
	fi

	repo="neoromantic/ai-usage"
	base="${AI_USAGE_DOWNLOAD_URL:-https://github.com/$repo/releases/latest/download}"
	base="${base%/}"
	bin_dir="${AI_USAGE_BIN_DIR:-$(find_bin_dir)}"

	os=$(detect_os)
	arch=$(detect_arch "$os")
	asset="ai-usage_${os}_${arch}"

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

	mkdir -p "$bin_dir"
	bin="$bin_dir/ai-usage"
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
	if ! "$bin"; then
		fail "the first run failed; the binary is installed, run $bin to retry"
	fi

	# find_bin_dir picks a folder on PATH or ~/.local/bin, so only
	# ~/.local/bin can be off PATH here, unless AI_USAGE_BIN_DIR named another.
	if on_path "$bin_dir"; then
		:
	elif [ -z "${AI_USAGE_BIN_DIR:-}${AI_USAGE_NO_MODIFY_PATH:-}" ] && add_to_path "$os"; then
		say "a new terminal finds ai-usage by name; in this one, run $bin"
	else
		say "$bin_dir is not on PATH; add it to your shell profile:
  export PATH=\"$bin_dir:\$PATH\""
	fi
}

# find_bin_dir prints the folder to install into. An upgrade replaces the
# earlier binary where it is, so there is never a second copy and the
# scheduler entry keeps pointing at it. A new install goes into a folder on
# PATH that is meant for a person's own programs; a tool's own folder, such
# as ~/.cargo/bin or a node version manager's, belongs to that tool.
find_bin_dir() {
	home=${HOME:?HOME is not set}
	for f in "$home/.local/bin/ai-usage" "$(command -v ai-usage || true)"; do
		case $f in /*) ;; *) continue ;; esac
		if [ -f "$f" ] && [ -w "${f%/*}" ]; then
			echo "${f%/*}"
			return
		fi
	done
	for d in "$home/.local/bin" "$home/bin" /opt/homebrew/bin /usr/local/bin; do
		if on_path "$d" && [ -d "$d" ] && [ -w "$d" ]; then
			echo "$d"
			return
		fi
	done
	echo "$home/.local/bin"
}

on_path() {
	case ":${PATH:-}:" in
	*":$1:"*) return 0 ;;
	esac
	return 1
}

# add_to_path puts ~/.local/bin on PATH in the profile of the login shell,
# once. The block names $HOME, not this home's path, so a profile shared
# between machines still works.
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
	*) profile="$HOME/.profile" ;;
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

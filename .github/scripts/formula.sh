#!/bin/sh
# Print the Homebrew formula of a release, for the tap
# neoromantic/homebrew-tap, which tap.sh updates after each release.
#
#   .github/scripts/formula.sh TAG CHECKSUMS [BASE_URL]
#
# CHECKSUMS is the release's checksums.txt. BASE_URL is where the release
# files are, by default the release's download folder on GitHub; CI passes a
# file:// URL to install a local build. Only a full release of v0.8.0 or
# later gets a formula: an older binary does not know it is installed by
# Homebrew and would replace itself inside brew's folders.
set -eu

tag=${1:?usage: formula.sh TAG CHECKSUMS [BASE_URL]}
sums=${2:?usage: formula.sh TAG CHECKSUMS [BASE_URL]}
default=https://github.com/neoromantic/ai-usage/releases/download/$tag
base=${3:-$default}
base=${base%/}

fail() {
	echo "formula.sh: $*" >&2
	exit 1
}

case $tag in
*-*) fail "$tag is a prerelease; Homebrew gets full releases only" ;;
v[0-9]*.[0-9]*.[0-9]*) ;;
*) fail "the tag must look like v1.2.3, got $tag" ;;
esac
version=${tag#v}
major=${version%%.*}
rest=${version#*.}
minor=${rest%%.*}
patch=${rest#*.}
for n in "$major" "$minor" "$patch"; do
	case $n in
	'' | *[!0-9]*) fail "the tag must look like v1.2.3, got $tag" ;;
	esac
done
if [ "$major" -eq 0 ] && [ "$minor" -lt 8 ]; then
	fail "$tag is older than v0.8.0, the first release that knows Homebrew installed it"
fi
[ -r "$sums" ] || fail "cannot read $sums"

# sha prints the checksum of release file $1, from "sha256  file" lines.
sha() {
	s=$(awk -v f="$1" '$2 == f || $2 == "*" f { print tolower($1); exit }' "$sums")
	case $s in
	'' | *[!0-9a-f]*) fail "$sums does not list $1" ;;
	esac
	[ "${#s}" -eq 64 ] || fail "$sums has no sha256 for $1"
	echo "$s"
}
darwin_arm64=$(sha ai-usage_darwin_arm64)
darwin_amd64=$(sha ai-usage_darwin_amd64)
linux_arm64=$(sha ai-usage_linux_arm64)
linux_amd64=$(sha ai-usage_linux_amd64)

cat <<EOF
class AiUsage < Formula
  desc "Quota left and token use for Claude Code, Codex, and Grok, per machine and team"
  homepage "https://github.com/neoromantic/ai-usage"
EOF
# Homebrew reads the version from a GitHub release URL, and audit --strict
# calls a version it can read redundant. From any other URL it would read a
# wrong one, such as 64 from ai-usage_darwin_arm64. brew style wants it
# before license.
if [ "$base" != "$default" ]; then
	echo "  version \"$version\""
fi
cat <<EOF
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "$base/ai-usage_darwin_arm64"
      sha256 "$darwin_arm64"
    end
    on_intel do
      url "$base/ai-usage_darwin_amd64"
      sha256 "$darwin_amd64"
    end
  end
  on_linux do
    on_arm do
      url "$base/ai-usage_linux_arm64"
      sha256 "$linux_arm64"
    end
    on_intel do
      url "$base/ai-usage_linux_amd64"
      sha256 "$linux_amd64"
    end
  end
EOF
# Quoted, so the shell leaves Ruby's #{} and the backticks alone. The test
# runs only version: a bare ai-usage would collect and register a schedule.
#
# On a Linux whose glibc or libstdc++ is older than on Homebrew's own build
# machines, Ubuntu 24.04's, Homebrew gives every formula its glibc and gcc
# as implicit dependencies, bottle or not: some 650 MB with binutils and the
# rest. Homebrew has no setting for a formula to say it needs neither, so
# the formula leaves out the method that adds them; the binary is static Go.
# It takes any arguments so that a change to the method cannot break the
# formula, and CI's Linux install fails if the dependencies come back.
cat <<'EOF'

  # A static binary: no Homebrew glibc or gcc on an older Linux.
  def add_global_deps_to_spec(*); end

  def install
    bin.install Dir["ai-usage_*"].first => "ai-usage"
  end

  def caveats
    <<~EOS
      To join a team, run `ai-usage team join` first and paste the key.
      Then run `ai-usage` once: it collects and registers a 15-minute run
      with launchd or cron. If you once ran `ai-usage schedule remove`,
      run `ai-usage schedule install` instead.
      On macOS 14 or later, `ai-usage app install` adds the menu bar app.
      Update with `brew upgrade neoromantic/tap/ai-usage` or `ai-usage update`.
      If you installed with the script before, remove that copy, such as
        rm ~/.local/bin/ai-usage
      If brew could not link ai-usage over an old copy in its bin folder:
        brew link --overwrite neoromantic/tap/ai-usage
      To remove it, run `ai-usage uninstall` first, then
        brew uninstall neoromantic/tap/ai-usage
      See https://github.com/neoromantic/ai-usage#uninstall
    EOS
  end

  test do
    assert_equal "v#{version}", shell_output("#{bin}/ai-usage version").strip
  end
end
EOF

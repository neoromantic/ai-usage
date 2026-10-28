#!/bin/sh
# Run the checks a change needs, no more, and keep what they showed.
#
#   scripts/verify.sh                the checks this branch's changes need
#   scripts/verify.sh --push         the same at CI's depth, before a push
#   scripts/verify.sh --plan         say which checks the changes need; run none
#   scripts/verify.sh --doctor       say which tools are missing or at the wrong version
#   scripts/verify.sh STEP...        run only these steps, whatever changed
#   scripts/verify.sh --base REF     changes since REF rather than origin/main
#
# The changes are the branch's commits since it left the base, with what is
# staged, unstaged, and untracked. The steps:
#
#   go       gofmt, go vet, go build, go test; go test reruns only packages
#            whose code or inputs changed, and a TestRun failure in
#            internal/tui, which depends on timing, is run once more
#   lint     staticcheck and deadcode at CI's versions: this OS, or with
#            --push every release OS
#   shell    sh -n and shellcheck on the shell scripts, a parse of install.ps1
#   page     the demo reports printed at 80 and 110 columns, and the
#            interactive view driven in a tmux of its own, every screen kept
#   stack    a relay on 127.0.0.1 and two made-up devices in homes of their
#            own: a team key, a join, a collect on each, and the first's
#            report lists both
#   plugin   the BB plugin in bb/: bun install, its types, its tests, which
#            read docs/demo, and bb plugin build
#   app      the menu bar app's checks, and a build of it (macOS)
#   pictures the app's pictures of the team demo, every tab and state in
#            light and dark; with --push, of the solo demo too (macOS)
#   build    the release build of the app bundle, macos/build.sh (macOS; --push)
#   install  .github/scripts/install-smoke.sh, the installer against a local
#            release (--push)
#   brew     the Homebrew smoke: a formula of local files installs, passes
#            brew test, says Homebrew updates it, and passes brew style and
#            audit (macOS, where CI no longer runs it; --push)
#
# CI runs no macOS job, so on a Mac, --push is the macOS gate: go test on
# darwin, the app's checks and release build, the installer, and Homebrew.
#
# Nothing touches this machine's ai-usage, its harnesses, or a real relay:
# page reads docs/demo, stack runs in homes under the evidence folder with a
# PATH of /usr/bin:/bin, and app renders without running ai-usage. What each
# step showed stays in .verify/<time>/, and .verify/latest points at the
# newest run.

# The steps run as step_$name, which shellcheck cannot follow. Older
# versions, as on CI's runners, call that SC2317 rather than SC2329.
# shellcheck disable=SC2317,SC2329
set -eu
# No build a step runs sends usage stats, whatever the version it reports.
export AI_USAGE_TELEMETRY=0

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

mode=changes
push=
base_ref=origin/main
only=
while [ $# -gt 0 ]; do
	case $1 in
	--push) push=1 ;;
	--plan) mode=plan ;;
	--doctor) mode=doctor ;;
	--base)
		[ $# -ge 2 ] || { echo "verify: --base needs a ref" >&2; exit 2; }
		base_ref=$2
		shift
		;;
	go | lint | shell | page | stack | app | pictures | build | install | brew) only="$only $1" ;;
	-h | --help)
		sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "verify: unknown argument $1; see scripts/verify.sh --help" >&2
		exit 2
		;;
	esac
	shift
done

goos=$(go env GOOS)
gobin=$(go env GOPATH)/bin

# Versions CI pins, read from its workflow so the two cannot drift.
pin() {
	sed -n "s|.*go install $1@\(v[0-9.]*\).*|\1|p" .github/workflows/ci.yml | head -n 1
}

has() { command -v "$1" >/dev/null 2>&1; }

doctor() {
	bad=0
	want_go=$(sed -n 's/^go \([0-9.]*\)$/\1/p' go.mod)
	printf 'go          %s (go.mod wants %s or later)\n' "$(go env GOVERSION)" "$want_go"
	sc=$(pin honnef.co/go/tools/cmd/staticcheck)
	dc=$(pin golang.org/x/tools/cmd/deadcode)
	if [ -x "$gobin/staticcheck" ] && "$gobin/staticcheck" -version | grep -q "(${sc#v})"; then
		echo "staticcheck $sc"
	else
		echo "staticcheck missing or not $sc: go install honnef.co/go/tools/cmd/staticcheck@$sc"
		bad=1
	fi
	if [ -x "$gobin/deadcode" ] && go version -m "$gobin/deadcode" | grep -q "golang.org/x/tools[[:space:]]*$dc"; then
		echo "deadcode    $dc"
	else
		echo "deadcode    missing or not $dc: go install golang.org/x/tools/cmd/deadcode@$dc"
		bad=1
	fi
	for tool in shellcheck tmux jq bun; do
		if has "$tool"; then printf '%-11s ok\n' "$tool"; else printf '%-11s missing\n' "$tool"; bad=1; fi
	done
	if [ "$goos" = darwin ]; then
		if xcrun --find swift >/dev/null 2>&1; then echo "swift       ok"; else echo "swift       missing: install Xcode or the Command Line Tools"; bad=1; fi
		if ! has brew; then
			echo "brew        missing: the brew step needs Homebrew"
			bad=1
		elif [ -e "$(brew --prefix)/opt/ai-usage" ]; then
			echo "brew        has ai-usage installed, which stops install and brew: brew uninstall ai-usage first"
		else
			echo "brew        ok"
		fi
	else
		echo "swift       not on $goos: app and build run only on macOS"
	fi
	if has pwsh; then echo "pwsh        ok"; else echo "pwsh        missing: install.ps1 is parsed in CI only"; fi
	if git rev-parse -q --verify "$base_ref^{commit}" >/dev/null; then
		echo "base        $base_ref at $(git rev-parse --short "$base_ref")"
	else
		echo "base        $base_ref not found: git fetch, or pass --base"
		bad=1
	fi
	return $bad
}

if [ "$mode" = doctor ]; then
	doctor
	exit $?
fi

# What changed, and which steps that needs, with the reason for each.
changed=
reasons=
want() {
	case " $want_steps " in *" $1 "*) ;; *) want_steps="$want_steps $1" ;; esac
	case "$reasons" in *"[$1]"*) ;; *) reasons="$reasons
  [$1] $2" ;; esac
}
want_steps=
if [ -n "$only" ]; then
	for s in $only; do want "$s" "asked for"; done
else
	git rev-parse -q --verify "$base_ref^{commit}" >/dev/null ||
		{ echo "verify: no $base_ref; git fetch, or pass --base REF" >&2; exit 2; }
	base=$(git merge-base "$base_ref" HEAD)
	changed=$({
		git diff --name-only "$base"
		git ls-files --others --exclude-standard
	} | sort -u)
	for f in $changed; do
		case $f in
		*_test.go | */testdata/*) want go "$f" ;;
		*.go | go.mod | go.sum)
			want go "$f"
			want lint "$f"
			want page "$f"
			want stack "$f"
			;;
		esac
		case $f in
		macos/*) want app "$f" ;;
		# The app reads the report's JSON, and its checks read the demo's.
		internal/view/* | internal/snapshot/* | docs/demo/*.json) want app "$f" ;;
		esac
		case $f in
		macos/Sources/AIUsageBar/* | macos/Sources/AIUsageKit/* | docs/demo/*.json) want pictures "$f" ;;
		esac
		case $f in docs/demo/*.json) want page "$f" ;; esac
		# The BB plugin reads the report's JSON and bundles the team demo.
		case $f in bb/* | internal/view/* | internal/snapshot/* | docs/demo/*.json) want plugin "$f" ;; esac
		case $f in *.sh | install.ps1) want shell "$f" ;; esac
		if [ -n "$push" ]; then
			case $f in macos/*) want build "$f" ;; esac
			# The installer puts the app in place on macOS, and the first run
			# registers with launchd, so the app and that code count too.
			case $f in
			install.sh | install.ps1 | .github/scripts/* | macos/* | internal/fsutil/* | \
				internal/selfupdate/* | internal/schedule/* | cmd/ai-usage/update.go | \
				cmd/ai-usage/schedule.go | cmd/ai-usage/app.go)
				want install "$f"
				;;
			esac
			# What Homebrew installs, and how the binary knows it did.
			case $f in
			.github/scripts/formula.sh | .github/scripts/dist.sh | internal/fsutil/* | \
				internal/selfupdate/* | cmd/ai-usage/brew.go | cmd/ai-usage/update.go | cmd/ai-usage/report.go)
				want brew "$f"
				;;
			esac
		fi
	done
fi

# Steps in the order that fails cheapest first.
steps=
for s in shell go lint page stack plugin app pictures build install brew; do
	case " $want_steps " in *" $s "*) steps="$steps $s" ;; esac
done

if [ -z "$only" ]; then
	n=$(printf '%s\n' "$changed" | grep -c . || true)
	depth=
	[ -z "$push" ] || depth=", at the depth CI checks"
	echo "verify: $n changed files since $base_ref$depth"
fi
if [ -z "$steps" ]; then
	echo "verify: no step needed: no code, script, app, or demo report changed"
	exit 0
fi
echo "verify: steps:$steps$reasons"
if [ -z "$push" ] && [ -z "$only" ]; then
	for f in $changed; do
		case $f in
		macos/* | install.sh | install.ps1 | .github/scripts/* | internal/selfupdate/* | internal/schedule/* | internal/fsutil/*)
			echo "verify: build, install, and brew wait for --push"
			break
			;;
		esac
	done
fi
[ "$mode" = plan ] && exit 0

stamp=$(date +%Y%m%d-%H%M%S)
ev=$root/.verify/$stamp
mkdir -p "$ev"
ln -sfn "$stamp" "$root/.verify/latest"
# Keep the ten newest runs.
# shellcheck disable=SC2012
ls -1d "$root"/.verify/2* 2>/dev/null | sort -r | tail -n +11 | while read -r old; do rm -rf "$old"; done

# A socket path must be short (104 bytes on macOS), so not under $ev.
tmux_socket=/tmp/ai-usage-verify-$$.sock
relay_pid=
brew_tap=
# Each step runs in a subshell of its own, which sets this as its trap, so
# what a step started stops even when it fails half-way.
cleanup() {
	[ -z "$relay_pid" ] || kill "$relay_pid" 2>/dev/null || true
	if [ -n "$brew_tap" ]; then
		brew uninstall --force "$brew_tap/ai-usage" >/dev/null 2>&1 || true
		brew untrust --tap "$brew_tap" >/dev/null 2>&1 || true
		brew untap "$brew_tap" >/dev/null 2>&1 || true
	fi
	tmux -S "$tmux_socket" -f /dev/null kill-server 2>/dev/null || true
	rm -f "$tmux_socket"
}

# The binary page and stack drive, built once per run.
bin=$ev/bin/ai-usage
binary() {
	[ -x "$bin" ] || go build -o "$bin" ./cmd/ai-usage
}

fail() {
	echo "  FAIL: $*" >&2
	return 1
}

step_go() {
	# Tracked and untracked Go files that exist: a deleted one has nothing to format.
	files=$(git ls-files -co --exclude-standard '*.go' | while read -r f; do [ ! -f "$f" ] || echo "$f"; done)
	# shellcheck disable=SC2086
	unformatted=$(gofmt -l $files)
	[ -z "$unformatted" ] || fail "gofmt -w these: $unformatted"
	go vet ./...
	go build ./...
	if ! go test ./... >"$ev/go-test.txt" 2>&1; then
		failed=$(sed -n 's/^FAIL[[:space:]]\{1,\}\([^[:space:]]*\).*/\1/p' "$ev/go-test.txt" | sort -u)
		if [ "$failed" = github.com/neoromantic/ai-usage/internal/tui ]; then
			echo "  internal/tui failed; it depends on timing, so once more"
			go test -count=1 ./internal/tui >>"$ev/go-test.txt" 2>&1 ||
				{ grep -E '^(---|FAIL|panic)' "$ev/go-test.txt" | head -n 40; fail "go test; see go-test.txt"; }
		else
			grep -E '^(---|FAIL|panic)|_test\.go:[0-9]+:' "$ev/go-test.txt" | head -n 40
			fail "go test; see go-test.txt"
		fi
	fi
	echo "  packages ok: $(grep -c '^ok' "$ev/go-test.txt" || true)"
	case "$changed" in *go.mod* | *go.sum*) go mod tidy -diff ;; *) [ -z "$push" ] || go mod tidy -diff ;; esac
}

step_lint() {
	sc=$(pin honnef.co/go/tools/cmd/staticcheck)
	dc=$(pin golang.org/x/tools/cmd/deadcode)
	"$gobin/staticcheck" -version 2>/dev/null | grep -q "(${sc#v})" ||
		fail "staticcheck is not $sc: go install honnef.co/go/tools/cmd/staticcheck@$sc"
	go version -m "$gobin/deadcode" 2>/dev/null | grep -q "golang.org/x/tools[[:space:]]*$dc" ||
		fail "deadcode is not $dc: go install golang.org/x/tools/cmd/deadcode@$dc"
	oses=$goos
	[ -z "$push" ] || oses="linux darwin windows"
	for os in $oses; do
		echo "  $os"
		# go vet for this OS already ran in go.
		[ "$os" = "$goos" ] || GOOS=$os go vet ./...
		GOOS=$os "$gobin/staticcheck" ./...
		d=$(GOOS=$os "$gobin/deadcode" -test ./...)
		[ -z "$d" ] || fail "deadcode on $os: $d"
	done
}

step_shell() {
	scripts=$(ls install.sh scripts/*.sh .github/scripts/*.sh macos/build.sh)
	for f in $scripts; do sh -n "$f"; done
	# shellcheck disable=SC2086
	shellcheck -s sh $scripts
	if has pwsh; then
		# shellcheck disable=SC2016
		pwsh -NoProfile -Command '$e=$null; [void][System.Management.Automation.Language.Parser]::ParseFile("$PWD/install.ps1", [ref]$null, [ref]$e); if ($e) { $e | Format-List | Out-String | Write-Host; exit 1 }'
	else
		echo "  install.ps1: no pwsh here; CI parses it"
	fi
}

step_page() {
	binary
	mkdir -p "$ev/page"
	for r in solo team; do
		for w in 80 110; do
			out=$ev/page/$r-$w.txt
			TZ=Europe/Berlin "$bin" report --from "docs/demo/$r.json" --plain --width "$w" --color=never >"$out" 2>&1 ||
				fail "report --from docs/demo/$r.json --width $w; see page/$r-$w.txt"
			grep -q '^SUBSCRIPTIONS' "$out" || fail "page/$r-$w.txt has no SUBSCRIPTIONS"
			! grep -q 'panic\|goroutine ' "$out" || fail "page/$r-$w.txt panics"
		done
	done
	# The interactive view, in a tmux of its own: -f /dev/null keeps this
	# machine's tmux config, and any plugin that restores sessions, out of it.
	t() { tmux -S "$tmux_socket" -f /dev/null "$@"; }
	t new-session -d -s v -x 110 -y 45 \
		"TZ=Europe/Berlin LANG=en_US.UTF-8 TERM=xterm-256color '$bin' report --from docs/demo/team.json; echo \$? >'$ev/page/tui-exit'"
	i=0
	shot() {
		sleep 0.4
		t capture-pane -p -t v >"$ev/page/tui-$i-$1.txt"
		[ -s "$ev/page/tui-$i-$1.txt" ] || fail "an empty screen after $1"
		i=$((i + 1))
	}
	shot start
	grep -q '^ATTENTION' "$ev/page/tui-0-start.txt" || fail "the interactive view opened without ATTENTION; see page/tui-0-start.txt"
	# Each key, then its screen: DEVICES as status, shares, the week, the
	# bottom of the page, the help, the help closed, then a row picked, the
	# next one, its detail, and back.
	for name in status share week end help close pick next open back; do
		case $name in
		status) t send-keys -t v -l s ;;
		share) t send-keys -t v -l % ;;
		week) t send-keys -t v -l 7 ;;
		end) t send-keys -t v -l G ;;
		help) t send-keys -t v -l '?' ;;
		close | back) t send-keys -t v Escape ;;
		pick | open) t send-keys -t v Enter ;;
		next) t send-keys -t v -l j ;;
		esac
		shot "$name"
	done
	grep -q '^STATES' "$ev/page/tui-5-help.txt" || fail "? showed no help; see page/tui-5-help.txt"
	! cmp -s "$ev/page/tui-0-start.txt" "$ev/page/tui-1-status.txt" || fail "s did not change the view"
	grep -q 'pick .* open' "$ev/page/tui-7-pick.txt" || fail "Enter picked no row; see page/tui-7-pick.txt"
	# A detail's key bar goes back, and opens nothing.
	case $(tail -n 1 "$ev/page/tui-9-open.txt") in
	*open*) fail "Enter opened no detail; see page/tui-9-open.txt" ;;
	*"esc back"*) ;;
	*) fail "Enter opened no detail; see page/tui-9-open.txt" ;;
	esac
	cmp -s "$ev/page/tui-8-next.txt" "$ev/page/tui-10-back.txt" || fail "Esc did not go back to the page as it was; see page/tui-10-back.txt"
	t send-keys -t v q
	sleep 0.4
	[ "$(cat "$ev/page/tui-exit" 2>/dev/null)" = 0 ] || fail "q did not quit the interactive view with 0"
	t kill-server 2>/dev/null || true
	echo "  $(find "$ev/page" -type f | wc -l | tr -d ' ') screens and pages in page/"
}

step_stack() {
	binary
	s=$ev/stack
	mkdir -p "$s/a" "$s/b"
	port=
	for p in 28741 28742 28743 28744 28745; do
		"$bin" relay serve --addr "127.0.0.1:$p" >"$s/relay.log" 2>&1 &
		relay_pid=$!
		k=0
		while [ "$k" -lt 20 ]; do
			if curl -fsS "http://127.0.0.1:$p/v1/health" >/dev/null 2>&1; then port=$p; break; fi
			kill -0 "$relay_pid" 2>/dev/null || break
			sleep 0.1
			k=$((k + 1))
		done
		[ -n "$port" ] && break
		kill "$relay_pid" 2>/dev/null || true
		relay_pid=
	done
	[ -n "$port" ] || fail "no relay started; see stack/relay.log"
	# Homes of their own, no harness on PATH, this relay only. The probe still
	# finds installed tools in the usual install places and asks them in the
	# made-up home, where they find no login and write only there.
	dev() {
		d=$1
		shift
		env -i HOME="$s/$d" PATH=/usr/bin:/bin TERM=dumb AI_USAGE_NO_SCHEDULE=1 AI_USAGE_TELEMETRY=0 \
			AI_USAGE_RELAY="http://127.0.0.1:$port" AI_USAGE_NAME="dev-$d" "$bin" "$@"
	}
	# Never collect --quiet here: that is the scheduled run, which first waits
	# up to a minute, by the device, so that a team's runs spread out.
	dev a team key >"$s/key" 2>"$s/key.err" || fail "team key; see stack/key.err"
	dev b team join <"$s/key" >"$s/b-join.txt" 2>&1 || fail "team join on dev-b; see stack/b-join.txt"
	dev b collect --plain --width 110 >"$s/b-collect.txt" 2>&1 || fail "collect on dev-b; see stack/b-collect.txt"
	dev a collect --json >"$s/a.json" 2>"$s/a.err" || fail "collect --json on dev-a; see stack/a.err"
	dev a report --plain --width 110 >"$s/a.txt" 2>&1 || fail "the report on dev-a; see stack/a.txt"
	dev b status >"$s/b-status.txt" 2>&1 || fail "status on dev-b; see stack/b-status.txt"
	jq -e '[.team.devices[].label] | index("dev-a") and index("dev-b")' "$s/a.json" >/dev/null ||
		fail "dev-a's report does not list both devices; see stack/a.json"
	grep -q 'dev-b' "$s/a.txt" || fail "dev-a's page does not show dev-b; see stack/a.txt"
	kill "$relay_pid" 2>/dev/null || true
	relay_pid=
	rm -f "$s/key"
	echo "  two devices, one team, through 127.0.0.1:$port"
}

step_plugin() {
	has bun || fail "no bun on PATH; the plugin step needs Bun"
	(cd bb && bun install --frozen-lockfile && bun run check) >"$ev/plugin.txt" 2>&1 ||
		{ tail -n 30 "$ev/plugin.txt"; fail "bun run check in bb/; see plugin.txt"; }
	grep -E '^ *[0-9]+ (pass|fail)' "$ev/plugin.txt" | sed 's/^ */  /'
}

step_app() {
	[ "$goos" = darwin ] || { echo "  skipped: the app builds only on macOS"; return 0; }
	mkdir -p "$ev/app"
	(cd macos && xcrun swift run AIUsageChecks) >"$ev/app/checks.txt" 2>&1 ||
		{ grep -v '^ok ' "$ev/app/checks.txt" | tail -n 30; fail "AIUsageChecks; see app/checks.txt"; }
	tail -n 1 "$ev/app/checks.txt" | sed 's/^/  /'
	(cd macos && xcrun swift build --product AIUsageBar) >"$ev/app/build.txt" 2>&1 ||
		{ tail -n 30 "$ev/app/build.txt"; fail "swift build AIUsageBar; see app/build.txt"; }
}

# The renderer opens the ⋯ menu on the screen for a moment, since AppKit
# draws a menu only while it is open. The team demo takes about 45 seconds,
# the solo one 20.
step_pictures() {
	[ "$goos" = darwin ] || { echo "  skipped: the app builds only on macOS"; return 0; }
	mkdir -p "$ev/app"
	(cd macos && xcrun swift build --product AIUsageBar) >"$ev/app/build.txt" 2>&1 ||
		{ tail -n 30 "$ev/app/build.txt"; fail "swift build AIUsageBar; see app/build.txt"; }
	barbin=$(cd macos && xcrun swift build --show-bin-path)/AIUsageBar
	reports=team
	[ -z "$push" ] || reports="team solo"
	for r in $reports; do
		TZ=Europe/Berlin "$barbin" --render "docs/demo/$r.json" "$ev/app/$r" >"$ev/app/render-$r.txt" 2>&1 ||
			fail "AIUsageBar --render $r; see app/render-$r.txt"
		echo "  $(find "$ev/app/$r" -name '*.png' | wc -l | tr -d ' ') pictures in app/$r"
	done
}

step_build() {
	[ "$goos" = darwin ] || { echo "  skipped: the app builds only on macOS"; return 0; }
	sh macos/build.sh v9.9.9-verify "$ev/build/app.zip" >"$ev/build.txt" 2>&1 ||
		{ tail -n 30 "$ev/build.txt"; fail "macos/build.sh; see build.txt"; }
}

step_install() {
	# 8765, the script's own port, is often taken on a desktop.
	PORT=28765 RUNNER_TEMP="$ev/install" sh .github/scripts/install-smoke.sh >"$ev/install.txt" 2>&1 ||
		{ tail -n 30 "$ev/install.txt"; fail "install-smoke.sh; see install.txt"; }
}

# CI's brew job, on macOS: formula.sh refuses an old release and a
# prerelease; a formula of local files installs with no dependency, passes
# brew test, and the binary, run in a home of its own, says Homebrew updates
# it; and the formula the tap gets passes brew style and audit. It installs
# into this Mac's Homebrew, from a tap of its own, and removes both at the
# end, so it stops when Homebrew already has an ai-usage. CI still runs it on
# Linux.
step_brew() {
	[ "$goos" = darwin ] || { echo "  skipped: CI runs the Homebrew smoke on Linux"; return 0; }
	has brew || fail "no brew on PATH; the brew step needs Homebrew"
	prefix=$(brew --prefix)
	[ ! -e "$prefix/opt/ai-usage" ] ||
		fail "Homebrew already has ai-usage ($prefix/opt/ai-usage), which this step would replace; brew uninstall ai-usage first, and install it again after"
	export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1 HOMEBREW_NO_ANALYTICS=1 HOMEBREW_NO_ENV_HINTS=1
	b=$ev/brew
	mkdir -p "$b"
	# The formula names all four binaries, so all four are built.
	AI_USAGE_RELAY_URL='' TARGETS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64" \
		sh .github/scripts/dist.sh v9.9.9 "$b/dist" >"$b/dist.txt" 2>&1 ||
		{ tail -n 30 "$b/dist.txt"; fail "dist.sh; see brew/dist.txt"; }
	for tag in v0.7.0 v0.8.0-rc1; do
		if sh .github/scripts/formula.sh "$tag" "$b/dist/checksums.txt" >/dev/null 2>&1; then
			fail "formula.sh wrote a formula for $tag"
		fi
	done
	brew_tap=verify/aiu
	brew untap "$brew_tap" >/dev/null 2>&1 || true
	brew tap-new --no-git "$brew_tap" >"$b/tap.txt" 2>&1 || { cat "$b/tap.txt"; fail "brew tap-new; see brew/tap.txt"; }
	# Homebrew 7 loads a formula of a tap it does not trust only after brew
	# trust; an older one has no such command. The trust goes with the tap.
	if brew trust --help >/dev/null 2>&1; then
		brew trust --tap "$brew_tap" >>"$b/tap.txt" 2>&1 || { cat "$b/tap.txt"; fail "brew trust; see brew/tap.txt"; }
	fi
	formula="$(brew --repo "$brew_tap")/Formula/ai-usage.rb"
	sh .github/scripts/formula.sh v9.9.9 "$b/dist/checksums.txt" "file://$b/dist" >"$formula"
	cp "$formula" "$b/local.rb"
	deps=$(brew deps --include-implicit "$brew_tap/ai-usage")
	[ -z "$deps" ] || fail "Homebrew gives the formula dependencies: $deps"
	brew install "$brew_tap/ai-usage" >"$b/install.txt" 2>&1 ||
		{ tail -n 30 "$b/install.txt"; fail "brew install; see brew/install.txt"; }
	brew test "$brew_tap/ai-usage" >"$b/test.txt" 2>&1 ||
		{ tail -n 30 "$b/test.txt"; fail "brew test; see brew/test.txt"; }
	[ "$("$prefix/bin/ai-usage" version)" = v9.9.9 ] || fail "$prefix/bin/ai-usage is not the build brew installed"
	# Never this Mac's own state: a home of its own, no harness on PATH.
	h=$b/home
	mkdir -p "$h"
	run() {
		env -i HOME="$h" PATH="$prefix/bin:/usr/bin:/bin" TERM=dumb AI_USAGE_NO_SCHEDULE=1 AI_USAGE_TELEMETRY=0 "$prefix/bin/ai-usage" "$@"
	}
	run </dev/null >"$b/first-run.txt" 2>&1 || fail "the first run; see brew/first-run.txt"
	run report --json >"$b/report.json" 2>"$b/report.err" || fail "report --json; see brew/report.err"
	jq -e '.collector.update.method == "homebrew" and (.collector.update.command | test("neoromantic/tap"))' "$b/report.json" >/dev/null ||
		fail "the report does not say Homebrew updates it; see brew/report.json"
	brew uninstall "$brew_tap/ai-usage" >/dev/null
	# The text the tap gets, with GitHub's URLs.
	sh .github/scripts/formula.sh v9.9.9 "$b/dist/checksums.txt" >"$formula"
	brew style "$brew_tap" >"$b/style.txt" 2>&1 || { tail -n 30 "$b/style.txt"; fail "brew style; see brew/style.txt"; }
	brew audit --strict --formula "$brew_tap/ai-usage" >"$b/audit.txt" 2>&1 ||
		{ tail -n 30 "$b/audit.txt"; fail "brew audit; see brew/audit.txt"; }
	brew untrust --tap "$brew_tap" >/dev/null 2>&1 || true
	brew untap "$brew_tap" >/dev/null 2>&1
	brew_tap=
	echo "  installed, tested, styled and audited from $prefix"
}

summary=
status=0
for s in $steps; do
	echo "== $s"
	start=$(date +%s)
	# Not `if (step)`: inside a condition the shell ignores set -e, and a
	# step would carry on past the command that failed.
	set +e
	(
		set -e
		trap cleanup EXIT
		step_"$s"
	)
	r=$?
	set -e
	if [ $r = 0 ]; then r=ok; else r=FAIL; status=1; fi
	summary="$summary$(printf '%-8s %-4s %4ss' "$s" "$r" "$(($(date +%s) - start))")
"
done
{
	echo "verify $stamp at $(git rev-parse --short HEAD)${push:+, --push}"
	printf '%s' "$summary"
} | tee "$ev/summary.txt"
echo "evidence: .verify/$stamp"
exit $status

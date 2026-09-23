#!/bin/sh
# Deploy the ai-usage relay to Vercel, with Upstash for Redis as its store.
# Run it from a clone of the repository:
#
#   sh scripts/deploy-relay.sh
#
# Environment, all optional:
#   AI_USAGE_RELAY_PROJECT  Vercel project name (default: ai-usage-relay)
#   VERCEL_SCOPE            Vercel team to deploy under (default: your own
#                           account; where Vercel keeps a Hobby account's
#                           projects in a team, the team the CLI uses now)
#
# The script links the clone to the project, creating the project if needed,
# adds an Upstash for Redis database unless the project already has one,
# deploys to production, checks /v1/health, and prints the commands that point
# collectors at the relay. It deploys the files in this clone as they are, so
# check out the commit you want first. Running it again redeploys.

# Everything is inside main so a partly downloaded script does nothing.
main() {
	set -eu

	command -v vercel >/dev/null 2>&1 || fail "the Vercel CLI is required: bun add -g vercel, or see https://vercel.com/docs/cli"
	command -v curl >/dev/null 2>&1 || fail "curl is required"

	root=$(cd "$(dirname "$0")/.." && pwd)
	if [ ! -f "$root/api/relay.go" ] || [ ! -f "$root/vercel.json" ]; then
		fail "run this script from a clone of the ai-usage repository"
	fi
	cd "$root"

	project=${AI_USAGE_RELAY_PROJECT:-ai-usage-relay}
	user=$(vercel whoami 2>/dev/null) || fail "log in to Vercel first: vercel login"
	say "logged in to Vercel as $user"

	# The scope goes into "$@", so every vercel call below gets the same one.
	# It is always named: without it, linking looks through every team the
	# user belongs to and would take another team's project of the same name.
	if [ -n "${VERCEL_SCOPE:-}" ]; then
		scope=$VERCEL_SCOPE
		say "using the Vercel scope $scope"
	elif vercel whoami --scope "$user" >/dev/null 2>&1; then
		scope=$user
		say "using your own Vercel account; VERCEL_SCOPE names a team instead"
	else
		# Accounts whose Hobby projects live in a team cannot name themselves
		# as the scope. Their team is the one the CLI uses now.
		scope=$(vercel whoami --format json 2>/dev/null | tr -d '\n' | sed -n 's/.*"team": *{[^}]*"slug": *"\([^"]*\)".*/\1/p')
		[ -n "$scope" ] || fail "could not tell which Vercel team to use; set VERCEL_SCOPE to one"
		say "using the Vercel team $scope, the one the CLI uses now; VERCEL_SCOPE names another"
	fi
	set -- --scope "$scope"

	say "linking $root to the Vercel project $project"
	vercel link --yes --project "$project" "$@" >&2 || fail "could not link the Vercel project $project"

	tmp=$(mktemp -d 2>/dev/null || mktemp -d -t ai-usage-relay)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 130' INT TERM

	# The relay reads KV_REST_API_URL and KV_REST_API_TOKEN, which connecting
	# Upstash for Redis sets. The listing shows names, not values, and is
	# never printed.
	vercel env ls production "$@" >"$tmp/env" 2>&1 || fail "could not list the project's environment variables"
	if grep -Eq '(^|[^A-Za-z0-9_])KV_REST_API_URL([^A-Za-z0-9_]|$)' "$tmp/env"; then
		say "the project already has a Redis store"
	else
		say "adding Upstash for Redis; the first install on a Vercel team may ask you to accept its terms"
		# --no-env-pull keeps the store's token out of a .env file in the clone.
		vercel integration add upstash/upstash-kv --no-env-pull "$@" >&2 || fail "could not add Upstash for Redis; add it in the project's Storage tab, then run this again"
	fi

	say "deploying to production"
	# Progress goes to the terminal and to a log, since the production address
	# is printed only there, as "Aliased: https://…". The deployment's own
	# URL goes to standard output.
	{
		status=0
		vercel deploy --prod --yes --no-color "$@" 2>&1 >"$tmp/out" || status=$?
		echo "$status" >"$tmp/status"
	} | tee "$tmp/log" >&2
	[ "$(cat "$tmp/status")" = 0 ] || fail "the deployment failed; see the output above"
	url=$(sed -n 's/.*Aliased: \(https:\/\/[^ ]*\).*/\1/p' "$tmp/log" | head -n 1)
	if [ -z "$url" ]; then
		url=$(grep -o 'https://[^" ]*' "$tmp/out" | head -n 1)
		[ -n "$url" ] || fail "the deployment finished, but its address was not found in the output"
		say "no production domain in the output; using the deployment's own URL, which Deployment Protection may put behind a Vercel login"
	fi
	url=${url%/}

	say "checking $url/v1/health"
	tries=0
	while :; do
		code=$(curl -sS --max-time 20 -o "$tmp/health" -w '%{http_code}' "$url/v1/health" 2>/dev/null) || code=000
		case $code in
		200) break ;;
		503) fail "the relay answers but has no store: connect Upstash for Redis in the project's Storage tab, then run this again" ;;
		401 | 403) fail "$url asks for a Vercel login; use the production domain, or turn off Deployment Protection for it" ;;
		esac
		# A new domain can take a few seconds to answer.
		tries=$((tries + 1))
		if [ "$tries" -ge 10 ]; then
			[ "$code" != 000 ] || fail "could not reach $url"
			fail "$url/v1/health answered HTTP $code"
		fi
		sleep 3
	done
	grep -q '"ok":true' "$tmp/health" || fail "$url/v1/health did not answer as a relay"

	cat <<EOF

The relay is running at $url

Point a collector at it:

  ai-usage relay set $url

Or install a new collector with it:

  curl -fsSL https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh | AI_USAGE_RELAY=$url sh

On Windows, in PowerShell:

  \$env:AI_USAGE_RELAY = '$url'; irm https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.ps1 | iex
EOF
}

say() { printf 'deploy-relay: %s\n' "$*" >&2; }

fail() {
	say "$*"
	exit 1
}

main "$@"

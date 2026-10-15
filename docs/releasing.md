# Releasing

A release is a Git tag. Pushing a tag like `v1.2.3` runs `.github/workflows/release.yml`, which tests, builds, and publishes the GitHub release that the installers and self-update read, and puts its formula in the Homebrew tap. It builds and publishes on macOS, since the menu bar app builds only there; Go cross-compiles the binaries for every OS. A Linux job, which the macOS one waits for, tests on Linux first, where most collectors run.

## Cut a release

```sh
git tag -a v1.2.3 -m v1.2.3
git push origin v1.2.3
```

Notes for a release go in `docs/releases/v1.2.3.md`, named after the tag and committed before it is pushed. They are written for the people who install it: what changed for them, what to do about it, and what it does not do yet, with made-up names only, since the repository is public. When the release changes a claim of the README, an install command, or a picture in `docs/demo`, its notes say so, for the website (see [After a release](#after-a-release)).

The workflow then:

1. on Linux, runs `go vet ./...` and `go test ./...`, and checks that the Linux amd64 binary reports the tag as its version
2. on macOS, runs `go vet ./...`, `go test ./...`, and the menu bar app's checks, `xcrun swift run AIUsageChecks` in `macos/`
3. builds the seven release files with `.github/scripts/dist.sh`
4. checks that the macOS arm64 binary reports the tag as its version; that `checksums.txt` lists seven files that match it; and that the app in `ai-usage_darwin_app.zip` reports the tag without its `v`, is built for both CPUs, and has a signature that verifies
5. creates the release as a draft, uploads the files, and then publishes it. Its notes are `docs/releases/<tag>.md`, when that file exists, followed by GitHub's generated notes, which link the changes since the last release
6. updates the Homebrew tap, in the step "Update the Homebrew tap", with `.github/scripts/tap.sh`; see [Homebrew](#homebrew)

The draft step keeps `releases/latest` from pointing at a release whose files are still uploading. The installers download from `releases/latest/download/`, and running collectors take the tag `releases/latest` redirects to and download from `releases/download/<tag>/`. If the workflow fails after the draft exists, rerun it: it replaces the files and publishes, and keeps the notes the draft was created with; change them with `gh release edit <tag> --notes-file docs/releases/<tag>.md`.

Release builds check at every scheduled run, so every collector the installer set up installs the release within about 15 minutes of its publication and runs it from the run after. Collectors Homebrew installed wait for someone to run `brew upgrade neoromantic/tap/ai-usage` or `ai-usage update`, once the tap has the release. On a Mac, the same check brings the menu bar app to the release, and the app restarts into it. A relay change the release needs must be deployed before then; see [Connect collectors to your relay](relay.md#connect-collectors-to-your-relay). A release a collector downloads and cannot install, for example one that does not start on its machine, is downloaded there again only after 6 hours, and so is a menu bar app that does not install, so fix it with a new tag, which collectors install at their next run.

A tag with a suffix, such as `v1.3.0-rc.1`, becomes a prerelease. GitHub's latest release skips prereleases, so the installers and self-update skip it too, and the tap keeps the last full release. Self-update also ignores any version that is not plain `vX.Y.Z`.

## Homebrew

The tap is the repository `neoromantic/homebrew-tap`, made by hand, with `Formula/ai-usage.rb`, a README, and the MIT license; `brew install neoromantic/tap/ai-usage` reads it. There is no cask: Homebrew quarantines a cask's download, and Gatekeeper refuses the ad-hoc signed app, so the app comes from `ai-usage app install` instead.

`.github/scripts/formula.sh TAG CHECKSUMS [BASE_URL]` prints the formula: the four macOS and Linux binaries of the release, with their checksums from `checksums.txt`, a test that runs `ai-usage version`, and caveats that say to run `ai-usage`, how to add the menu bar app, update, and uninstall. It refuses a prerelease and any tag before v0.8.0, the first release that knows Homebrew installed it: an older binary would replace itself inside Homebrew's folders.

After "Publish", the step "Update the Homebrew tap" runs `.github/scripts/tap.sh`. For a prerelease it does nothing. For a full release it waits up to 5 minutes until GitHub serves the four binaries, clones the tap with the deploy key, writes the formula, runs `brew audit --strict --online` on it, with the job's token for GitHub's API (`HOMEBREW_GITHUB_API_TOKEN`), and commits `ai-usage <tag>` to the tap's `main`. A rerun that finds the formula already there changes nothing, so rerun the job when the step fails. A tap that already has a later release keeps it, as when an earlier tag's job is rerun after the next release, or two tags' jobs finish out of order.

It needs the deploy key: a key with write access among the tap's deploy keys, whose private half is the repository secret `HOMEBREW_TAP_KEY` (Settings, Secrets and variables, Actions). Without it the step fails for a full release, since Homebrew's collectors would otherwise report the release as out for good. Add the secret, then rerun the job. When the job cannot be fixed, write the formula by hand with `formula.sh` and push it to the tap.

A release reaches Homebrew a few minutes after GitHub. Meanwhile `ai-usage update` on a Homebrew collector says the formula is behind and exits 1, and the collectors' reports say the release is out.

## After a release

Check the tap's commit `ai-usage <tag>`, and install the release with `brew install neoromantic/tap/ai-usage`, or `brew upgrade neoromantic/tap/ai-usage`, on a Mac and on a Linux machine.

The maintainer adds the release to the website's changelog, [au.sergeybaus.com/changelog](https://au.sergeybaus.com/changelog), written the way the release notes are, shorter, for people who have not installed ai-usage yet, with made-up names only. The website also repeats the README's claims, the install commands, and pictures from `docs/demo`; when the release notes say one of these changed, the maintainer updates the site too.

## Release files

| File | Built for |
| --- | --- |
| `ai-usage_darwin_amd64` | macOS, Intel |
| `ai-usage_darwin_arm64` | macOS, Apple silicon |
| `ai-usage_linux_amd64` | Linux, x86-64 |
| `ai-usage_linux_arm64` | Linux, ARM64 |
| `ai-usage_windows_amd64.exe` | Windows, x64 |
| `ai-usage_windows_arm64.exe` | Windows, ARM64 |
| `ai-usage_darwin_app.zip` | the menu bar app, `AI Usage.app`, for macOS 14 and later on Apple silicon and Intel |
| `checksums.txt` | one `sha256  filename` line per file |

Self-update, both installers, and the formula find their file by these names, so they must not change. Every binary is built with `CGO_ENABLED=0` and `-trimpath`, stripped with `-s -w`, and given its version with `-X main.version=<tag>`.

`macos/build.sh` builds the app: a release build of the Swift package for arm64 and for x86_64, joined with `lipo` into one executable, in a bundle whose `CFBundleShortVersionString` is the tag without its `v`, signed ad hoc with `codesign --sign -`, and zipped by `ditto` with `AI Usage.app` at the top. Self-update installs the app only when that version is the release's. The app is not notarized. curl and Go do not mark what they download as quarantined, so Gatekeeper lets it open; a copy downloaded in a browser would be quarantined, and macOS would refuse to open it.

## Default relay

Set the repository variable `AI_USAGE_RELAY_URL` (Settings, Secrets and variables, Actions, Variables) to build that relay into the binaries with `-X main.defaultRelay=…`. A collector uses it until someone runs `ai-usage relay set`, and `ai-usage relay clear` goes back to it. It must be an `https://` URL. Leave the variable unset to ship binaries without a relay.

## Build locally

`dist.sh` is what the workflow runs, from the repository's root:

```sh
sh .github/scripts/dist.sh v9.9.9-local /tmp/ai-usage-dist
TARGETS=linux/amd64 sh .github/scripts/dist.sh v9.9.9-local /tmp/ai-usage-dist
TARGETS=darwin/app sh .github/scripts/dist.sh v9.9.9-local /tmp/ai-usage-dist
AI_USAGE_RELAY_URL=https://relay.example.com sh .github/scripts/dist.sh v1.2.3 /tmp/ai-usage-dist
```

The target `darwin/app` is the menu bar app. It needs macOS with Xcode or the Command Line Tools; on another OS, `dist.sh` skips it and says so, and builds the six binaries.

A version with a suffix, such as `v9.9.9-local`, counts as a development build: the binary never registers with the scheduler on its own and never updates itself, and self-update leaves an app of that version alone. Use one for anything you run by hand. Give it a release of v0.4.0 or later before the suffix: the team takes a suffixed version before v0.4.0, such as the `v0.0.0-local` that earlier releases said to use, for a build of a release that drops the snapshot fields v0.4.0 added, and holds them back from every device until it is built again.

To try an installer against local files, serve the folder and point the installer at it with `AI_USAGE_DOWNLOAD_URL`. CI does this on every push with `.github/scripts/install-smoke.sh`, on macOS, Linux, and Windows.

## CI

`.github/workflows/ci.yml` runs on pushes to `main` and on pull requests:

- `gofmt` and `go build` with the oldest Go that `go.mod` allows, at the patch its `toolchain` line names, which is also the Go Vercel builds the relay with
- `sh -n` and ShellCheck on the shell scripts, `macos/build.sh` among them, and a PowerShell parse of `install.ps1`
- a cross-compile of the six binaries, on Linux
- the menu bar app's checks and a build of it with `macos/build.sh`, on macOS
- `go vet`, `staticcheck` and `deadcode -test` for Linux, macOS, and Windows, and `go mod tidy -diff`, with the stable Go
- `go test` on Linux and macOS, plus `go test -race` on Linux; Windows is cross-compiled and installed by the smoke test, but its unit tests are not a gate
- the Homebrew smoke test on macOS and Linux: `formula.sh` refuses v0.7.0 and a prerelease; a formula of local files installs, passes `brew test`, and reports `"method": "homebrew"` and its command in the JSON; on Linux the crontab line names Homebrew's `opt` path, and stays so after a copy in `~/.local/bin` runs; and the formula of a release passes `brew style` and `brew audit --strict`
- the installer smoke test on Linux, macOS, and Windows. On macOS it builds the app too, checks that the installer puts it in `~/Applications` of a home of its own with the release's version, that the upgrade replaces it, and that it runs, and quits it at the end. On Windows it also registers the Task Scheduler task for real, checks that it runs on battery, and removes it.

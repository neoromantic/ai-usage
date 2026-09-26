# Releasing

A release is a Git tag. Pushing a tag like `v1.2.3` runs `.github/workflows/release.yml`, which tests, builds, and publishes the GitHub release that the installers and self-update read. It builds and publishes on macOS, since the menu bar app builds only there; Go cross-compiles the binaries for every OS. A Linux job, which the macOS one waits for, tests on Linux first, where most collectors run.

## Cut a release

```sh
git tag -a v1.2.3 -m v1.2.3
git push origin v1.2.3
```

The workflow then:

1. on Linux, runs `go vet ./...` and `go test ./...`, and checks that the Linux amd64 binary reports the tag as its version
2. on macOS, runs `go vet ./...`, `go test ./...`, and the menu bar app's checks, `xcrun swift run AIUsageChecks` in `macos/`
3. builds the seven release files with `.github/scripts/dist.sh`
4. checks that the macOS arm64 binary reports the tag as its version; that `checksums.txt` lists seven files that match it; and that the app in `ai-usage_darwin_app.zip` reports the tag without its `v`, is built for both CPUs, and has a signature that verifies
5. creates the release as a draft with generated notes, uploads the files, and then publishes it

The draft step keeps `releases/latest` from pointing at a release whose files are still uploading. The installers download from `releases/latest/download/`, and running collectors take the tag `releases/latest` redirects to and download from `releases/download/<tag>/`. If the workflow fails after the draft exists, rerun it: it replaces the files and publishes.

Release builds check at every scheduled run, so every collector installs the release within about 15 minutes of its publication and runs it from the run after. On a Mac, the same check brings the menu bar app to the release, and the app restarts into it. A relay change the release needs must be deployed before then; see [Connect collectors to your relay](relay.md#connect-collectors-to-your-relay). A release a collector downloads and cannot install, for example one that does not start on its machine, is downloaded there again only after 6 hours, and so is a menu bar app that does not install, so fix it with a new tag, which collectors install at their next run.

A tag with a suffix, such as `v1.3.0-rc.1`, becomes a prerelease. GitHub's latest release skips prereleases, so the installers and self-update skip it too. Self-update also ignores any version that is not plain `vX.Y.Z`.

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

Self-update and both installers find their file by these names, so they must not change. Every binary is built with `CGO_ENABLED=0` and `-trimpath`, stripped with `-s -w`, and given its version with `-X main.version=<tag>`.

`macos/build.sh` builds the app: a release build of the Swift package for arm64 and for x86_64, joined with `lipo` into one executable, in a bundle whose `CFBundleShortVersionString` is the tag without its `v`, signed ad hoc with `codesign --sign -`, and zipped by `ditto` with `AI Usage.app` at the top. Self-update installs the app only when that version is the release's. The app is not notarized. curl and Go do not mark what they download as quarantined, so Gatekeeper lets it open; a copy downloaded in a browser would be quarantined, and macOS would refuse to open it.

## Default relay

Set the repository variable `AI_USAGE_RELAY_URL` (Settings, Secrets and variables, Actions, Variables) to build that relay into the binaries with `-X main.defaultRelay=…`. A collector uses it until someone runs `ai-usage relay set`, and `ai-usage relay clear` goes back to it. It must be an `https://` URL. Leave the variable unset to ship binaries without a relay.

## Build locally

`dist.sh` is what the workflow runs, from the repository's root:

```sh
sh .github/scripts/dist.sh v0.0.0-local /tmp/ai-usage-dist
TARGETS=linux/amd64 sh .github/scripts/dist.sh v0.0.0-local /tmp/ai-usage-dist
TARGETS=darwin/app sh .github/scripts/dist.sh v0.0.0-local /tmp/ai-usage-dist
AI_USAGE_RELAY_URL=https://relay.example.com sh .github/scripts/dist.sh v1.2.3 /tmp/ai-usage-dist
```

The target `darwin/app` is the menu bar app. It needs macOS with Xcode or the Command Line Tools; on another OS, `dist.sh` skips it and says so, and builds the six binaries.

A version with a suffix, such as `v0.0.0-local`, counts as a development build: the binary never registers with the scheduler on its own and never updates itself, and self-update leaves an app of that version alone. Use one for anything you run by hand.

To try an installer against local files, serve the folder and point the installer at it with `AI_USAGE_DOWNLOAD_URL`. CI does this on every push with `.github/scripts/install-smoke.sh`, on macOS, Linux, and Windows.

## CI

`.github/workflows/ci.yml` runs on pushes to `main` and on pull requests:

- `gofmt` and `go build` with the oldest Go that `go.mod` allows
- `sh -n` and ShellCheck on the shell scripts, `macos/build.sh` among them, and a PowerShell parse of `install.ps1`
- a cross-compile of the six binaries, on Linux
- the menu bar app's checks and a build of it with `macos/build.sh`, on macOS
- `go vet`, `staticcheck` and `deadcode -test` for Linux, macOS, and Windows, and `go mod tidy -diff`, with the stable Go
- `go test` on Linux and macOS, plus `go test -race` on Linux; Windows is cross-compiled and installed by the smoke test, but its unit tests are not a gate
- the installer smoke test on Linux, macOS, and Windows. On macOS it builds the app too, checks that the installer puts it in `~/Applications` of a home of its own with the release's version, that the upgrade replaces it, and that it runs, and quits it at the end. On Windows it also registers the Task Scheduler task for real, checks that it runs on battery, and removes it.

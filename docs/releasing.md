# Releasing

A release is a Git tag. Pushing a tag like `v1.2.3` runs `.github/workflows/release.yml`, which tests, builds, and publishes the GitHub release that the installers and self-update read.

## Cut a release

```sh
git tag -a v1.2.3 -m v1.2.3
git push origin v1.2.3
```

The workflow then:

1. runs `go vet ./...` and `go test ./...`
2. builds the six release files with `.github/scripts/dist.sh`
3. checks that the Linux amd64 binary reports the tag as its version, and that `checksums.txt` lists six files that match it
4. creates the release as a draft with generated notes, uploads the files, and then publishes it

The draft step keeps `releases/latest` from pointing at a release whose files are still uploading, since installers and running collectors download from it. If the workflow fails after the draft exists, rerun it: it replaces the files and publishes.

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
| `checksums.txt` | one `sha256  filename` line per file |

Self-update and both installers find their file by these names, so they must not change. Every binary is built with `CGO_ENABLED=0` and `-trimpath`, stripped with `-s -w`, and given its version with `-X main.version=<tag>`.

## Default relay

Set the repository variable `AI_USAGE_RELAY_URL` (Settings, Secrets and variables, Actions, Variables) to build that relay into the binaries with `-X main.defaultRelay=…`. A collector uses it until someone runs `ai-usage relay set`, and `ai-usage relay clear` goes back to it. It must be an `https://` URL. Leave the variable unset to ship binaries without a relay.

## Build locally

`dist.sh` is what the workflow runs:

```sh
sh .github/scripts/dist.sh v0.0.0-local /tmp/ai-usage-dist
TARGETS=linux/amd64 sh .github/scripts/dist.sh v0.0.0-local /tmp/ai-usage-dist
AI_USAGE_RELAY_URL=https://relay.example.com sh .github/scripts/dist.sh v1.2.3 /tmp/ai-usage-dist
```

A version with a suffix, such as `v0.0.0-local`, counts as a development build: the binary never registers with the scheduler on its own and never updates itself. Use one for anything you run by hand.

To try an installer against local files, serve the folder and point the installer at it with `AI_USAGE_DOWNLOAD_URL`. CI does this on every push with `.github/scripts/install-smoke.sh`, on macOS, Linux, and Windows.

## CI

`.github/workflows/ci.yml` runs on pushes to `main` and on pull requests:

- `gofmt`, `go build`, and `go vet` with the oldest Go that `go.mod` allows
- `sh -n` and ShellCheck on the shell scripts, and a PowerShell parse of `install.ps1`
- a cross-compile of all six release files
- `go vet` and `go test` on Linux, macOS, and Windows, plus `go test -race` on Linux
- the installer smoke test on all three, which on Windows also registers the Task Scheduler task for real, checks that it runs on battery, and removes it

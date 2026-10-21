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
3. puts the Developer ID certificate in a keychain of its own and the notary key in a file, in the step "Set up signing"; see [Signing and notarization](#signing-and-notarization)
4. builds the seven release files with `.github/scripts/dist.sh`, which signs the two macOS binaries and the app with Developer ID, has Apple notarize them, and staples the ticket to the app, and then deletes the keychain and the key
5. checks that the macOS arm64 binary reports the tag as its version; that `checksums.txt` lists seven files that match it; that the app in `ai-usage_darwin_app.zip` reports the tag without its `v`, is built for both CPUs, and has a signature that verifies; and that the app and both macOS binaries are signed with the hardened runtime and notarized, that the app's ticket is stapled, and that Gatekeeper accepts them as `Notarized Developer ID`
6. creates the release as a draft, uploads the files, and then publishes it. Its notes are `docs/releases/<tag>.md`, when that file exists, followed by GitHub's generated notes, which link the changes since the last release
7. updates the Homebrew tap, in the step "Update the Homebrew tap", with `.github/scripts/tap.sh`; see [Homebrew](#homebrew)

The draft step keeps `releases/latest` from pointing at a release whose files are still uploading. The installers download from `releases/latest/download/`, and running collectors take the tag `releases/latest` redirects to and download from `releases/download/<tag>/`. If the workflow fails after the draft exists, rerun it: it replaces the files and publishes, and keeps the notes the draft was created with; change them with `gh release edit <tag> --notes-file docs/releases/<tag>.md`.

Release builds check at every scheduled run, so every collector the installer set up installs the release within about 15 minutes of its publication and runs it from the run after. Collectors Homebrew installed wait for someone to run `brew upgrade neoromantic/tap/ai-usage` or `ai-usage update`, once the tap has the release. On a Mac, the same check brings the menu bar app to the release, and the app restarts into it. A relay change the release needs must be deployed before then; see [Connect collectors to your relay](relay.md#connect-collectors-to-your-relay). A release a collector downloads and cannot install, for example one that does not start on its machine, is downloaded there again only after 6 hours, and so is a menu bar app that does not install, so fix it with a new tag, which collectors install at their next run.

A tag with a suffix, such as `v1.3.0-rc.1`, becomes a prerelease. GitHub's latest release skips prereleases, so the installers and self-update skip it too, and the tap keeps the last full release. Self-update also ignores any version that is not plain `vX.Y.Z`.

## Homebrew

The tap is the repository `neoromantic/homebrew-tap`, made by hand, with `Formula/ai-usage.rb`, a README, and the MIT license; `brew install neoromantic/tap/ai-usage` reads it. There is no cask: the app comes from `ai-usage app install`, and runs keep it at the binary's release.

`.github/scripts/formula.sh TAG CHECKSUMS [BASE_URL]` prints the formula: the four macOS and Linux binaries of the release, with their checksums from `checksums.txt`, a test that runs `ai-usage version`, and caveats that say to run `ai-usage`, how to add the menu bar app, update, and uninstall. It refuses a prerelease and any tag before v0.8.0, the first release that knows Homebrew installed it: an older binary would replace itself inside Homebrew's folders.

On a Linux whose glibc or libstdc++ is older than on Homebrew's own build machines, Ubuntu 24.04's, Homebrew gives every formula its glibc and gcc as implicit dependencies, bottled or not: about 650 MB with binutils and the rest, which the static binary never uses. Homebrew has no setting for a formula to decline them, so the formula overrides `add_global_deps_to_spec`, the method of Homebrew's that adds them, with one that adds nothing. A bottle would not help, and a cask would change where the binary lives, which the binary reads to know Homebrew installed it. The method is Homebrew's own, not a public one: CI installs the formula on Linux with `HOMEBREW_FORCE_LIBC_FORMULA` and `HOMEBREW_FORCE_COMPILER_FORMULA`, which make Homebrew act as on such a Linux, and fails when it gets any dependency, so a Homebrew that renames the method shows up there first. If it does, find what adds them in `Library/Homebrew/extend/os/linux/formula.rb` of Homebrew/brew. The two variables are Homebrew's own too, so CI first checks that a copy of the formula without the override does get dependencies with them, and fails if it gets none.

After "Publish", the step "Update the Homebrew tap" runs `.github/scripts/tap.sh`. For a prerelease it does nothing. For a full release it waits up to 5 minutes until GitHub serves the four binaries, clones the tap with the deploy key, writes the formula, runs `brew audit --strict --online` on it, with the job's token for GitHub's API (`HOMEBREW_GITHUB_API_TOKEN`), and commits `ai-usage <tag>` to the tap's `main`. A rerun that finds the formula already there changes nothing, so rerun the job when the step fails. A tap that already has a later release keeps it, as when an earlier tag's job is rerun after the next release, or two tags' jobs finish out of order.

It needs the deploy key: a key with write access among the tap's deploy keys, whose private half is the repository secret `HOMEBREW_TAP_KEY` (Settings, Secrets and variables, Actions). Without it the step fails for a full release, since Homebrew's collectors would otherwise report the release as out for good. Add the secret, then rerun the job. When the job cannot be fixed, write the formula by hand with `formula.sh` and push it to the tap.

A release reaches Homebrew a few minutes after GitHub. Meanwhile `ai-usage update` on a Homebrew collector says the formula is behind and exits 1, and the collectors' reports say the release is out.

## Signing and notarization

Every tag, a prerelease too, ships the app and the two macOS binaries signed with a Developer ID Application certificate, with the hardened runtime and a secure timestamp, and notarized by Apple. So Gatekeeper opens the app however it reached the Mac, from a browser too, and on macOS 27 launchd keeps a launch agent's pin across updates (see [the README](../README.md#a-mac-missed-a-run-after-an-update)). The workflow needs five repository secrets (Settings, Secrets and variables, Actions):

| Secret | Holds |
| --- | --- |
| `MACOS_SIGN_P12` | the Developer ID Application certificate and its private key, exported from Keychain Access as a `.p12`, in base64 (`base64 -i cert.p12`) |
| `MACOS_SIGN_P12_PASSWORD` | the password the `.p12` was exported with |
| `NOTARY_KEY` | an App Store Connect API key, its `.p8` file in base64; a team key with the Developer role is enough |
| `NOTARY_KEY_ID` | that key's ID |
| `NOTARY_ISSUER_ID` | the issuer ID of the team's keys |

Without `MACOS_SIGN_P12` or `NOTARY_KEY`, the release stops at "Set up signing", before anything is built. That step makes a keychain with a random password under the runner's temporary folder, imports the `.p12` into it for `codesign` alone, adds it to the keychains `codesign` searches, picks the Developer ID Application identity by its SHA-1, and writes the notary key to a file only the runner's user reads. Only "Set up signing" and "Build" see the secrets, through their environment. "Build" passes them to `dist.sh` as `AI_USAGE_SIGN_IDENTITY`, `AI_USAGE_NOTARY_KEY` (the key file's path), `AI_USAGE_NOTARY_KEY_ID`, and `AI_USAGE_NOTARY_ISSUER`, and the step after it, which runs even when Build fails, deletes the keychain and the key.

With those set, `dist.sh` signs each macOS binary with `codesign --options runtime --timestamp` and the identifier `io.github.neoromantic.ai-usage`, and `macos/build.sh` signs the app the same way. `.github/scripts/notarize.sh` zips what it is given, sends it with `xcrun notarytool submit --wait`, and fails, printing Apple's log, unless Apple answers `Accepted`. The app goes alone and gets its ticket stapled with `xcrun stapler staple` before it is zipped; the two binaries go together after the loop. A bare binary cannot hold a ticket, so Gatekeeper, when it checks one, asks Apple for it online. `checksums.txt` is taken after all this, so it matches the files as signed. A release whose notarization fails publishes nothing; rerun the job.

The app needs no entitlements under the hardened runtime: it runs `ai-usage` as a separate program, posts notifications, and opens at login through ServiceManagement, none of which the hardened runtime restricts.

A Developer ID certificate lasts five years. Before it expires, make a new one in the Apple Developer account, export it with its key as a `.p12`, and replace the two `MACOS_SIGN_P12` secrets. Releases signed before keep working, since their signatures carry a secure timestamp. A revoked API key only stops notarization: make a new one and replace the three `NOTARY_` secrets.

To sign and notarize a local build, with the identity in the login keychain:

```sh
export AI_USAGE_SIGN_IDENTITY="Developer ID Application: Ann Example (A1B2C3D4E5)"
export AI_USAGE_NOTARY_KEY=/path/to/AuthKey_ABC123DEFG.p8 AI_USAGE_NOTARY_KEY_ID=ABC123DEFG
export AI_USAGE_NOTARY_ISSUER=00000000-0000-0000-0000-000000000000
TARGETS="darwin/arm64 darwin/app" sh .github/scripts/dist.sh v9.9.9-local /tmp/ai-usage-dist
spctl -a -vv -t exec "AI Usage.app"    # after unzipping: source=Notarized Developer ID
```

Leave `AI_USAGE_NOTARY_KEY` unset to sign without notarizing. Without `AI_USAGE_SIGN_IDENTITY`, as in CI and for a build from source, everything is signed ad hoc and not notarized.

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

Self-update, both installers, and the formula find their file by these names, so they must not change. Every binary is built with `CGO_ENABLED=0` and `-trimpath`, stripped with `-s -w`, and given its version with `-X main.version=<tag>`. The two macOS binaries are signed with Developer ID and notarized; see [Signing and notarization](#signing-and-notarization).

`macos/build.sh` builds the app: a release build of the Swift package for arm64 and for x86_64, joined with `lipo` into one executable, in a bundle whose `CFBundleShortVersionString` is the tag without its `v`, signed, and zipped by `ditto` with `AI Usage.app` at the top. Self-update installs the app only when that version is the release's. A release signs it with Developer ID, has it notarized, and staples the ticket to it, so macOS opens a copy a browser downloaded too. A build without `AI_USAGE_SIGN_IDENTITY` is signed ad hoc with `codesign --sign -` and not notarized: it opens when curl or Go downloaded it, since they do not mark what they download as quarantined, but macOS refuses a copy downloaded in a browser.

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
- the Homebrew smoke test on macOS and Linux: `formula.sh` refuses v0.7.0 and a prerelease; a formula of local files installs, with no dependency even when Homebrew acts as on an older Linux, where a copy without the override gets glibc and gcc, passes `brew test`, and reports `"method": "homebrew"` and its command in the JSON; on Linux the crontab line names Homebrew's `opt` path, and stays so after a copy in `~/.local/bin` runs; and the formula of a release passes `brew style` and `brew audit --strict`
- the installer smoke test on Linux, macOS, and Windows. On macOS it builds the app too, checks that the installer puts it in `~/Applications` of a home of its own with the release's version, that the upgrade replaces it, and that it runs, and quits it at the end. On Windows it also registers the Task Scheduler task for real, checks that it runs on battery, and removes it.
- devkit's hygiene job: actionlint and zizmor on the workflows, gitleaks on the new commits, and devkit's privacy scan of the lines a change adds

Both workflows pin every action to a full commit, with its version in a comment. The Renovate preset in `renovate.json` can update those pins, but the Renovate app does not run on the repository yet (#28), so bump a pin by hand: take the commit of the new tag from GitHub and change the version in the comment with it. The checkouts keep no credentials, and the workflows can only read the repository, except the release job, which publishes the release. To check a workflow change before a push, run `actionlint` and `uvx zizmor@1.31.1 --offline .`, the zizmor version devkit's hygiene job runs.

# Install the latest ai-usage release on Windows.
#
#   irm https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.ps1 | iex
#
# Environment, all optional:
#   AI_USAGE_BIN_DIR       where ai-usage.exe goes (default: %LOCALAPPDATA%\Programs\ai-usage)
#   AI_USAGE_NAME          this device's name in the team (default: the host name)
#   AI_USAGE_RELAY         relay URL to save before the first run
#   AI_USAGE_TEAM_KEY      team key to join before the first run
#   AI_USAGE_DOWNLOAD_URL  where release files are fetched from (mirrors, tests)
#
# The script downloads the release file for this CPU, checks it against the
# release's checksums.txt, installs it, adds its folder to the user PATH, and
# runs it once. That first run registers the collector with Task Scheduler.
# Running the script again upgrades in place.

# Everything is inside one script block, so a partly downloaded script does
# nothing, and a failure throws instead of closing the window `iex` runs in.
& {
    Set-StrictMode -Version 3.0
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'
    # Strict mode refuses to read an exit code no native command has set yet.
    $global:LASTEXITCODE = 0

    function Say([string]$msg) { Write-Host "ai-usage install: $msg" }
    function Setting([string]$name) { [Environment]::GetEnvironmentVariable($name) }

    if ($PSVersionTable.PSVersion.Major -ge 6 -and -not $IsWindows) {
        throw 'ai-usage install: on macOS and Linux, use install.sh'
    }
    # Windows PowerShell 5.1 may not offer TLS 1.2 by default; GitHub requires it.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $base = Setting 'AI_USAGE_DOWNLOAD_URL'
    if ($base) { $base = $base.TrimEnd('/') } else { $base = 'https://github.com/neoromantic/ai-usage/releases/latest/download' }
    $binDir = Setting 'AI_USAGE_BIN_DIR'
    # The collector keeps its state in %LOCALAPPDATA%\ai-usage, apart from the binary.
    if (-not $binDir) { $binDir = Join-Path $env:LOCALAPPDATA 'Programs\ai-usage' }

    $arch = $null
    try { $arch = [string][System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture } catch { }
    if (-not $arch) {
        $arch = Setting 'PROCESSOR_ARCHITEW6432'
        if (-not $arch) { $arch = Setting 'PROCESSOR_ARCHITECTURE' }
    }
    switch ($arch) {
        { $_ -in 'X64', 'AMD64' } { $goarch = 'amd64'; break }
        'Arm64' { $goarch = 'arm64'; break }
        default { throw "ai-usage install: unsupported CPU: $arch (releases are built for amd64 and arm64)" }
    }
    $asset = "ai-usage_windows_$goarch.exe"
    $exe = Join-Path $binDir 'ai-usage.exe'

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('ai-usage-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Say "downloading $asset"
        $download = Join-Path $tmp $asset
        $sums = Join-Path $tmp 'checksums.txt'
        Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $download
        Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $sums

        $want = $null
        foreach ($line in Get-Content $sums) {
            $f = -split $line
            if ($f.Count -eq 2 -and $f[1].TrimStart('*') -eq $asset) { $want = $f[0].ToLowerInvariant(); break }
        }
        if (-not $want) { throw "ai-usage install: checksums.txt does not list $asset" }
        $got = (Get-FileHash -Algorithm SHA256 -Path $download).Hash.ToLowerInvariant()
        if ($got -ne $want) { throw "ai-usage install: $asset does not match its checksum (got $got, want $want)" }

        New-Item -ItemType Directory -Force -Path $binDir | Out-Null
        $new = Join-Path $binDir '.ai-usage-new.exe'
        Copy-Item -Force $download $new
        # A running exe cannot be overwritten but can be renamed. The collector
        # removes ai-usage.exe.old on a later run.
        if (Test-Path $exe) {
            $old = "$exe.old"
            Remove-Item -Force $old -ErrorAction SilentlyContinue
            if (Test-Path $old) { $old = "$exe.old-" + [guid]::NewGuid().ToString('N').Substring(0, 8) }
            Move-Item $exe $old
        }
        Move-Item $new $exe
    } finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }
    $version = & $exe version
    if ($LASTEXITCODE -ne 0) { throw "ai-usage install: $exe is installed but does not run" }
    Say "installed $version to $exe"

    # Edit the raw registry value: reading PATH through .NET expands entries
    # like %USERPROFILE%\bin, and writing it back would freeze them.
    $envKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    try {
        $raw = [string]$envKey.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $parts = @($raw -split ';' | Where-Object { $_ -ne '' })
        if ($parts -notcontains $binDir) {
            $joined = (@($parts) + $binDir) -join ';'
            $envKey.SetValue('Path', $joined, [Microsoft.Win32.RegistryValueKind]::ExpandString)
            # Setting any user variable through .NET broadcasts the change, so
            # new terminals see the new PATH without signing out.
            [Environment]::SetEnvironmentVariable('AI_USAGE_PATH_CHANGED', '1', 'User')
            [Environment]::SetEnvironmentVariable('AI_USAGE_PATH_CHANGED', $null, 'User')
            Say "added $binDir to your user PATH; new terminals will find ai-usage"
        }
    } finally {
        $envKey.Close()
    }
    if (($env:Path -split ';') -notcontains $binDir) { $env:Path = "$env:Path;$binDir" }

    $deviceName = Setting 'AI_USAGE_NAME'
    if ($deviceName) {
        & $exe name set $deviceName
        if ($LASTEXITCODE -ne 0) { throw 'ai-usage install: name set failed' }
    }
    $relay = Setting 'AI_USAGE_RELAY'
    if ($relay) {
        & $exe relay set $relay
        if ($LASTEXITCODE -ne 0) { throw 'ai-usage install: relay set failed' }
    }
    $key = Setting 'AI_USAGE_TEAM_KEY'
    if ($key) {
        # The key goes through stdin so it never shows in the process list.
        $key | & $exe team join
        if ($LASTEXITCODE -ne 0) { throw 'ai-usage install: team join failed' }
    }

    Say 'first run: collecting and registering with Task Scheduler'
    & $exe
    if ($LASTEXITCODE -ne 0) { throw "ai-usage install: the first run failed; the binary is installed, run $exe to retry" }
}

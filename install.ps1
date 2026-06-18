# loop installer (Windows PowerShell) — downloads prebuilt binary tarball
# from GitHub Releases. No runtime required.
#
#   irm https://raw.githubusercontent.com/notshekhar/loop/main/install.ps1 | iex
#
# Layout after install:
#   $env:USERPROFILE\.loop-bin\
#     ├── loop.exe
#     ├── package.json
#     └── .install-method
#   Adds $env:USERPROFILE\.loop-bin to user PATH (and the current session).
#
# Env knobs:
#   $env:LOOP_REPO_SLUG  notshekhar/loop
#   $env:LOOP_VERSION    vX.Y.Z       pin a specific tag
#   $env:LOOP_HOME       %USERPROFILE%\.loop-bin
#   $env:LOOP_FORCE      1            skip "already up to date" gate
#   $env:LOOP_UNINSTALL  1            remove the install + PATH entry and exit

$ErrorActionPreference = "Stop"

function Bold($msg)  { Write-Host $msg -ForegroundColor White }
function Dim($msg)   { Write-Host $msg -ForegroundColor DarkGray }
function Err($msg)   { Write-Host $msg -ForegroundColor Red }

$RepoSlug    = if ($env:LOOP_REPO_SLUG) { $env:LOOP_REPO_SLUG } else { "notshekhar/loop" }
$LoopHome    = if ($env:LOOP_HOME)      { $env:LOOP_HOME }      else { Join-Path $env:USERPROFILE ".loop-bin" }
$Force       = $env:LOOP_FORCE -eq "1"
$PinVersion  = $env:LOOP_VERSION

# Older installs (below this version) kept their config in ~\.pi; migrate once.
$MigrateFromBelow = "0.5.0"

# ── Migrate legacy config dir → ~\.loop (one-time, version-gated) ───────────
# MOVE ~\.pi into ~\.loop (copy, then delete the old dir only once the copy
# succeeds) so config is never lost or duplicated. Runs only for installs below
# $MigrateFromBelow (version read from ~\.pi-bin\package.json; unknown counts as
# below the cutoff).
function Migrate-LegacyConfig {
    $legacy  = Join-Path $env:USERPROFILE ".pi"
    $current = Join-Path $env:USERPROFILE ".loop"
    if (-not (Test-Path $legacy)) { return }
    if (Test-Path $current) { return }

    $legacyVer = $null
    $legacyPkg = Join-Path $env:USERPROFILE ".pi-bin\package.json"
    if (Test-Path $legacyPkg) {
        try { $legacyVer = (Get-Content $legacyPkg -Raw | ConvertFrom-Json).version } catch {}
    }
    # Skip if a known legacy version is at/above the cutoff (not a pre-rename install).
    if ($legacyVer) {
        try {
            if ([version]($legacyVer.TrimStart("v")) -ge [version]$MigrateFromBelow) { return }
        } catch {}
    }

    $from = if ($legacyVer) { $legacyVer } else { "unknown" }
    Bold "▶ Migrating config $legacy → $current (from $from, below $MigrateFromBelow)"
    try {
        Copy-Item -Recurse -Force $legacy $current
        Remove-Item -Recurse -Force $legacy -ErrorAction SilentlyContinue
        Dim "  moved auth, sessions, settings → $current (removed $legacy)"
    } catch {
        Err "  migration failed — your config stays in $legacy"
    }
}

# ── Uninstall ─────────────────────────────────────────────────────────────
if ($env:LOOP_UNINSTALL -eq "1") {
    Bold "▶ Uninstalling loop"
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath) {
        $newPath = ($userPath.Split(";") | Where-Object { $_ -and $_ -ne $LoopHome }) -join ";"
        if ($newPath -ne $userPath) {
            [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
            Dim "  removed $LoopHome from user PATH"
        }
    }
    if (Test-Path $LoopHome) {
        Remove-Item -Recurse -Force $LoopHome -ErrorAction SilentlyContinue
        Dim "  removed $LoopHome"
    }
    Get-ChildItem -Path (Split-Path $LoopHome -Parent) -Filter "$(Split-Path $LoopHome -Leaf).old.*" -Directory -ErrorAction SilentlyContinue |
        ForEach-Object { Remove-Item -Recurse -Force $_.FullName -ErrorAction SilentlyContinue }
    Bold "✓ Uninstalled. Config in ~\.loop (auth, sessions, settings) was kept;"
    Dim  "  remove it with: Remove-Item -Recurse -Force `"$env:USERPROFILE\.loop`""
    exit 0
}

Migrate-LegacyConfig

# ── Detect arch ───────────────────────────────────────────────────────────
if (-not [Environment]::Is64BitOperatingSystem) {
    Err "32-bit Windows not supported."
    exit 1
}
$target = "windows-x64"
if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64") {
    # No native windows-arm64 release yet; the x64 build runs fine under
    # Windows 11's x64 emulation.
    Dim "  Windows on ARM detected — installing the x64 build (runs emulated)."
}
Dim "  target: $target"

# ── Resolve target version ────────────────────────────────────────────────
# Prefer the releases/latest redirect — it isn't subject to the anonymous
# GitHub API rate limit. Fall back to the API.
function Resolve-LatestTag {
    try {
        $resp = Invoke-WebRequest "https://github.com/$RepoSlug/releases/latest" `
                                  -Method Head -MaximumRedirection 5 -UseBasicParsing
        $final = $resp.BaseResponse.ResponseUri  # Windows PowerShell 5.x
        if (-not $final) { $final = $resp.BaseResponse.RequestMessage.RequestUri }  # PowerShell 7+
        $tag = ([string]$final).Split("/")[-1]
        if ($tag -match "^v[0-9]") { return $tag }
    } catch {}
    try {
        $resp = Invoke-RestMethod "https://api.github.com/repos/$RepoSlug/releases/latest" `
                                  -Headers @{ "User-Agent" = "loop-installer" }
        return $resp.tag_name
    } catch {
        return $null
    }
}

$latest = $PinVersion
if (-not $latest) {
    Bold "▶ Resolving latest release"
    $latest = Resolve-LatestTag
    if (-not $latest) {
        Err "Could not resolve latest release tag from $RepoSlug."
        Err "  Set `$env:LOOP_VERSION = 'vX.Y.Z' to pin."
        exit 1
    }
}
if (-not $latest.StartsWith("v")) { $latest = "v$latest" }

# ── Detect installed version ──────────────────────────────────────────────
$installed = ""
$installedPkgJson = Join-Path $LoopHome "package.json"
if (Test-Path $installedPkgJson) {
    try {
        $installed = (Get-Content $installedPkgJson -Raw | ConvertFrom-Json).version
    } catch {}
}
if (-not $Force -and $installed) {
    $latestSemver    = [version]($latest.TrimStart("v"))
    $installedSemver = [version]($installed.TrimStart("v"))
    if ($latestSemver -le $installedSemver) {
        Bold "✓ Up to date (installed $installed, latest $latest)"
        Dim  "  Set `$env:LOOP_FORCE = '1' to reinstall."
        exit 0
    }
    Dim "  update: $installed → $latest"
} else {
    Dim "  installing $latest"
}

# ── Download tarball + verify sha256 ─────────────────────────────────────
$tmpRoot = Join-Path ([System.IO.Path]::GetTempPath()) "loop-install-$(Get-Random)"
New-Item -ItemType Directory -Force -Path $tmpRoot | Out-Null

$base = "https://github.com/$RepoSlug/releases/download/$latest"
$url  = "$base/loop-$target.tar.gz"
$tar  = Join-Path $tmpRoot "loop.tar.gz"

Bold "▶ Downloading $($url.Split('/')[-1])"
try {
    Invoke-WebRequest -Uri $url -OutFile $tar -UseBasicParsing
} catch {
    Err "download failed: $url"
    Err "  release may not have $target asset"
    exit 1
}

try {
    $sumUrl = "$url.sha256"
    $resp = Invoke-WebRequest -Uri $sumUrl -UseBasicParsing
    # .Content is byte[] when server sends application/octet-stream — decode.
    $sumTxt = if ($resp.Content -is [byte[]]) {
        [System.Text.Encoding]::ASCII.GetString($resp.Content)
    } else {
        [string]$resp.Content
    }
    $expected = ($sumTxt.Trim() -split '\s+')[0]
    $got = (Get-FileHash -Algorithm SHA256 -Path $tar).Hash.ToLower()
    if ($expected.ToLower() -ne $got) {
        Err "sha256 mismatch (expected $expected, got $got)"
        exit 1
    }
    Dim "  sha256 ok"
} catch {
    Dim "  sha256 file missing — skipping verify"
}

# ── Extract (tar.exe ships with Windows 10 1803+) ─────────────────────────
Bold "▶ Extracting"
Push-Location $tmpRoot
tar -xzf "loop.tar.gz"
Pop-Location

$srcDir = Join-Path $tmpRoot $target
$binExe = Join-Path $srcDir "loop.exe"
if (-not (Test-Path $binExe)) {
    Err "tarball missing $target\loop.exe"
    exit 1
}

# ── Swap into place ───────────────────────────────────────────────────────
Bold "▶ Installing to $LoopHome"
$parent = Split-Path $LoopHome -Parent
if (-not (Test-Path $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }

# Sweep backup dirs left by earlier self-updates (a running loop.exe can't be
# deleted at update time, only renamed — by now those locks are gone).
Get-ChildItem -Path $parent -Filter "$(Split-Path $LoopHome -Leaf).old.*" -Directory -ErrorAction SilentlyContinue |
    ForEach-Object { Remove-Item -Recurse -Force $_.FullName -ErrorAction SilentlyContinue }

# A running loop.exe (self-update via `loop update`) locks deletion but allows
# renames — move the old dir aside, place the new one, then best-effort clean.
if (Test-Path $LoopHome) {
    $backup = "$LoopHome.old.$(Get-Random)"
    Move-Item -Force $LoopHome $backup
    try { Remove-Item -Recurse -Force $backup -ErrorAction SilentlyContinue } catch {}
}
Move-Item -Force $srcDir $LoopHome

Set-Content -Path (Join-Path $LoopHome ".install-method") -Value "binary" -NoNewline

Remove-Item -Recurse -Force $tmpRoot -ErrorAction SilentlyContinue

# ── Add to PATH: user (persistent) + current session (works right now) ───
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not $userPath) { $userPath = "" }
$paths = $userPath.Split(";") | Where-Object { $_ -ne "" }
if ($paths -notcontains $LoopHome) {
    Bold "▶ Adding $LoopHome to user PATH"
    $newPath = if ($userPath) { "$userPath;$LoopHome" } else { $LoopHome }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
}
$sessionPaths = $env:Path.Split(";") | Where-Object { $_ -ne "" }
if ($sessionPaths -notcontains $LoopHome) {
    $env:Path = "$env:Path;$LoopHome"
    Dim "  PATH updated for this session too — `loop` works right away."
}

# ── Smoke test: the binary must actually run ──────────────────────────────
try {
    $v = & (Join-Path $LoopHome "loop.exe") --version 2>&1
    if ($LASTEXITCODE -ne 0) { throw "exit code $LASTEXITCODE`: $v" }
    Dim "  verified: loop v$v"
} catch {
    Err "installed binary failed to run: $_"
    exit 1
}

Bold "✓ Installed $latest"
Write-Host "  loop:    $(Join-Path $LoopHome 'loop.exe')"
Write-Host "  target:  $LoopHome"
Write-Host ""
Dim "Run ``loop`` to start. Run ``loop login`` to add a provider."
Dim "Update later with ``loop update`` (or /update inside the TUI)."
Dim "First-run SmartScreen warning: click 'More info' → 'Run anyway'."

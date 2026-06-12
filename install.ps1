# pi installer (Windows PowerShell) — downloads prebuilt binary tarball
# from GitHub Releases. No runtime required.
#
#   irm https://raw.githubusercontent.com/notshekhar/pi/main/install.ps1 | iex
#
# Layout after install:
#   $env:USERPROFILE\.pi-bin\
#     ├── pi.exe
#     ├── package.json
#     └── .install-method
#   Adds $env:USERPROFILE\.pi-bin to user PATH (and the current session).
#
# Env knobs:
#   $env:PI_REPO_SLUG  notshekhar/pi
#   $env:PI_VERSION    vX.Y.Z       pin a specific tag
#   $env:PI_HOME       %USERPROFILE%\.pi-bin
#   $env:PI_FORCE      1            skip "already up to date" gate
#   $env:PI_UNINSTALL  1            remove the install + PATH entry and exit

$ErrorActionPreference = "Stop"

function Bold($msg)  { Write-Host $msg -ForegroundColor White }
function Dim($msg)   { Write-Host $msg -ForegroundColor DarkGray }
function Err($msg)   { Write-Host $msg -ForegroundColor Red }

$RepoSlug    = if ($env:PI_REPO_SLUG) { $env:PI_REPO_SLUG } else { "notshekhar/pi" }
$PiHome      = if ($env:PI_HOME)      { $env:PI_HOME }      else { Join-Path $env:USERPROFILE ".pi-bin" }
$Force       = $env:PI_FORCE -eq "1"
$PinVersion  = $env:PI_VERSION

# ── Uninstall ─────────────────────────────────────────────────────────────
if ($env:PI_UNINSTALL -eq "1") {
    Bold "▶ Uninstalling pi"
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath) {
        $newPath = ($userPath.Split(";") | Where-Object { $_ -and $_ -ne $PiHome }) -join ";"
        if ($newPath -ne $userPath) {
            [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
            Dim "  removed $PiHome from user PATH"
        }
    }
    if (Test-Path $PiHome) {
        Remove-Item -Recurse -Force $PiHome -ErrorAction SilentlyContinue
        Dim "  removed $PiHome"
    }
    Get-ChildItem -Path (Split-Path $PiHome -Parent) -Filter "$(Split-Path $PiHome -Leaf).old.*" -Directory -ErrorAction SilentlyContinue |
        ForEach-Object { Remove-Item -Recurse -Force $_.FullName -ErrorAction SilentlyContinue }
    Bold "✓ Uninstalled. Config in ~\.pi (auth, sessions, settings) was kept;"
    Dim  "  remove it with: Remove-Item -Recurse -Force `"$env:USERPROFILE\.pi`""
    exit 0
}

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
                                  -Headers @{ "User-Agent" = "pi-installer" }
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
        Err "  Set `$env:PI_VERSION = 'vX.Y.Z' to pin."
        exit 1
    }
}
if (-not $latest.StartsWith("v")) { $latest = "v$latest" }

# ── Detect installed version ──────────────────────────────────────────────
$installed = ""
$installedPkgJson = Join-Path $PiHome "package.json"
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
        Dim  "  Set `$env:PI_FORCE = '1' to reinstall."
        exit 0
    }
    Dim "  update: $installed → $latest"
} else {
    Dim "  installing $latest"
}

# ── Download tarball + verify sha256 ─────────────────────────────────────
$tmpRoot = Join-Path ([System.IO.Path]::GetTempPath()) "pi-install-$(Get-Random)"
New-Item -ItemType Directory -Force -Path $tmpRoot | Out-Null

$base = "https://github.com/$RepoSlug/releases/download/$latest"
$url  = "$base/pi-$target.tar.gz"
$tar  = Join-Path $tmpRoot "pi.tar.gz"

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
tar -xzf "pi.tar.gz"
Pop-Location

$srcDir = Join-Path $tmpRoot $target
$binExe = Join-Path $srcDir "pi.exe"
if (-not (Test-Path $binExe)) {
    Err "tarball missing $target\pi.exe"
    exit 1
}

# ── Swap into place ───────────────────────────────────────────────────────
Bold "▶ Installing to $PiHome"
$parent = Split-Path $PiHome -Parent
if (-not (Test-Path $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }

# Sweep backup dirs left by earlier self-updates (a running pi.exe can't be
# deleted at update time, only renamed — by now those locks are gone).
Get-ChildItem -Path $parent -Filter "$(Split-Path $PiHome -Leaf).old.*" -Directory -ErrorAction SilentlyContinue |
    ForEach-Object { Remove-Item -Recurse -Force $_.FullName -ErrorAction SilentlyContinue }

# A running pi.exe (self-update via `pi update`) locks deletion but allows
# renames — move the old dir aside, place the new one, then best-effort clean.
if (Test-Path $PiHome) {
    $backup = "$PiHome.old.$(Get-Random)"
    Move-Item -Force $PiHome $backup
    try { Remove-Item -Recurse -Force $backup -ErrorAction SilentlyContinue } catch {}
}
Move-Item -Force $srcDir $PiHome

Set-Content -Path (Join-Path $PiHome ".install-method") -Value "binary" -NoNewline

Remove-Item -Recurse -Force $tmpRoot -ErrorAction SilentlyContinue

# ── Add to PATH: user (persistent) + current session (works right now) ───
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not $userPath) { $userPath = "" }
$paths = $userPath.Split(";") | Where-Object { $_ -ne "" }
if ($paths -notcontains $PiHome) {
    Bold "▶ Adding $PiHome to user PATH"
    $newPath = if ($userPath) { "$userPath;$PiHome" } else { $PiHome }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
}
$sessionPaths = $env:Path.Split(";") | Where-Object { $_ -ne "" }
if ($sessionPaths -notcontains $PiHome) {
    $env:Path = "$env:Path;$PiHome"
    Dim "  PATH updated for this session too — `pi` works right away."
}

# ── Smoke test: the binary must actually run ──────────────────────────────
try {
    $v = & (Join-Path $PiHome "pi.exe") --version 2>&1
    if ($LASTEXITCODE -ne 0) { throw "exit code $LASTEXITCODE`: $v" }
    Dim "  verified: pi v$v"
} catch {
    Err "installed binary failed to run: $_"
    exit 1
}

Bold "✓ Installed $latest"
Write-Host "  pi:      $(Join-Path $PiHome 'pi.exe')"
Write-Host "  target:  $PiHome"
Write-Host ""
Dim "Run ``pi`` to start. Run ``pi login`` to add a provider."
Dim "Update later with ``pi update`` (or /update inside the TUI)."
Dim "First-run SmartScreen warning: click 'More info' → 'Run anyway'."

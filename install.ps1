# pi installer (Windows PowerShell) — downloads prebuilt binary tarball
# from GitHub Releases. No runtime required.
#
#   irm https://raw.githubusercontent.com/notshekhar/pi/main/install.ps1 | iex
#
# Layout after install:
#   $env:USERPROFILE\.pi-bin\
#     ├── pi.exe
#     ├── package.json
#     ├── theme\, assets\, export-html\
#     └── .install-method
#   Adds $env:USERPROFILE\.pi-bin to user PATH.
#
# Env knobs:
#   $env:PI_REPO_SLUG  notshekhar/pi
#   $env:PI_VERSION    vX.Y.Z       pin a specific tag
#   $env:PI_HOME       %USERPROFILE%\.pi-bin
#   $env:PI_FORCE      1            skip "already up to date" gate

$ErrorActionPreference = "Stop"

function Bold($msg)  { Write-Host $msg -ForegroundColor White }
function Dim($msg)   { Write-Host $msg -ForegroundColor DarkGray }
function Err($msg)   { Write-Host $msg -ForegroundColor Red }

$RepoSlug    = if ($env:PI_REPO_SLUG) { $env:PI_REPO_SLUG } else { "notshekhar/pi" }
$PiHome      = if ($env:PI_HOME)      { $env:PI_HOME }      else { Join-Path $env:USERPROFILE ".pi-bin" }
$Force       = $env:PI_FORCE -eq "1"
$PinVersion  = $env:PI_VERSION

# ── Detect arch ───────────────────────────────────────────────────────────
$arch = if ([Environment]::Is64BitOperatingSystem) { "x64" } else {
    Err "32-bit Windows not supported."
    exit 1
}
$target = "windows-$arch"
Dim "  target: $target"

# ── Resolve target version ────────────────────────────────────────────────
$latest = $PinVersion
if (-not $latest) {
    Bold "▶ Resolving latest release"
    try {
        $resp = Invoke-RestMethod "https://api.github.com/repos/$RepoSlug/releases/latest" `
                                   -Headers @{ "User-Agent" = "pi-installer" }
        $latest = $resp.tag_name
    } catch {
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
    $sumTxt = (Invoke-WebRequest -Uri $sumUrl -UseBasicParsing).Content
    $expected = ($sumTxt -split '\s+')[0]
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

# Close any existing pi.exe by retrying a few times if locked.
if (Test-Path $PiHome) {
    $backup = "$PiHome.old.$(Get-Random)"
    Move-Item -Force $PiHome $backup
    try { Remove-Item -Recurse -Force $backup -ErrorAction SilentlyContinue } catch {}
}
Move-Item -Force $srcDir $PiHome

Set-Content -Path (Join-Path $PiHome ".install-method") -Value "binary" -NoNewline

Remove-Item -Recurse -Force $tmpRoot -ErrorAction SilentlyContinue

# ── Add to user PATH if missing ──────────────────────────────────────────
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not $userPath) { $userPath = "" }
$paths = $userPath.Split(";") | Where-Object { $_ -ne "" }
if ($paths -notcontains $PiHome) {
    Bold "▶ Adding $PiHome to user PATH"
    $newPath = if ($userPath) { "$userPath;$PiHome" } else { $PiHome }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Dim "  Open a new terminal for PATH to take effect."
}

Bold "✓ Installed $latest"
Write-Host "  pi:      $(Join-Path $PiHome 'pi.exe')"
Write-Host "  target:  $PiHome"
Write-Host ""
Dim "Run ``pi`` to start. Run ``pi login`` to add a provider."
Dim "First-run SmartScreen warning: click 'More info' → 'Run anyway'."

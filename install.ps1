# pi installer (Windows) — downloads a prebuilt single binary. No node/bun
# required on the host.
#
# Usage (PowerShell):
#   irm https://raw.githubusercontent.com/notshekhar/agent/main/install.ps1 | iex
#
# Env knobs:
#   $env:PI_REPO_SLUG   "notshekhar/agent"            override GitHub repo
#   $env:PI_VERSION     "v0.2.0"                       pin specific tag
#   $env:PI_BIN_DIR     "$env:LOCALAPPDATA\pi"         where to extract
#   $env:PI_LINK_DIR    "$env:LOCALAPPDATA\Programs"   directory placed on PATH
#   $env:PI_FORCE       "1"                            skip "already up to date" gate

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Write-Bold($msg) { Write-Host $msg -ForegroundColor White }
function Write-Dim($msg)  { Write-Host $msg -ForegroundColor DarkGray }
function Write-Err($msg)  { Write-Host $msg -ForegroundColor Red }

$RepoSlug  = if ($env:PI_REPO_SLUG) { $env:PI_REPO_SLUG } else { "notshekhar/agent" }
$PinVer    = $env:PI_VERSION
$BinDir    = if ($env:PI_BIN_DIR)   { $env:PI_BIN_DIR }   else { Join-Path $env:LOCALAPPDATA "pi" }
$LinkDir   = if ($env:PI_LINK_DIR)  { $env:PI_LINK_DIR }  else { Join-Path $env:LOCALAPPDATA "Programs" }
$Force     = ($env:PI_FORCE -eq "1")

Write-Bold "[pi installer]"

# ── Detect arch ────────────────────────────────────────────────────────────
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { "x64" }
  "ARM64" { "arm64" }
  default { throw "Unsupported PROCESSOR_ARCHITECTURE: $($env:PROCESSOR_ARCHITECTURE)" }
}
$target = "windows-$arch"
Write-Dim "  target: $target"

# ── Resolve target version ─────────────────────────────────────────────────
function Get-LatestTag {
  try {
    $r = Invoke-RestMethod -Uri "https://api.github.com/repos/$RepoSlug/releases/latest" -Headers @{ "User-Agent" = "pi-installer" }
    return $r.tag_name
  } catch { return $null }
}
$Latest = if ($PinVer) { $PinVer } else { Get-LatestTag }
if (-not $Latest) {
  Write-Err "Could not resolve latest release. Set `$env:PI_VERSION=vX.Y.Z to pin one."
  exit 1
}

function To-Version([string]$v) { [version]($v -replace '^v','') }

$PiBinDir   = Join-Path $BinDir "pi-bin"
$Installed  = $null
$InstalledExe = Join-Path $PiBinDir "pi.exe"
if (Test-Path $InstalledExe) {
  try {
    $Installed = "v$((& $InstalledExe --version) -replace '^v','')".Trim()
  } catch {}
}

if (-not $Force -and $Installed) {
  if ((To-Version $Latest) -le (To-Version $Installed)) {
    Write-Bold "[OK] Up to date (installed $Installed, latest $Latest)"
    Write-Dim "Set `$env:PI_FORCE=1 to reinstall."
    return
  }
  Write-Dim "  update: $Installed -> $Latest"
} else {
  Write-Dim "  installing $Latest"
}

# ── Download tarball ───────────────────────────────────────────────────────
$LatestNoV = $Latest -replace '^v',''
$LatestV   = "v$LatestNoV"

$Tmp = New-Item -ItemType Directory -Path (Join-Path $env:TEMP ("pi-install-" + [guid]::NewGuid().ToString("N")))

$TarName = $null
$SumName = $null
$TarPath = $null
$SumPath = $null

# Try tag with and without leading `v`, asset name with and without.
:download foreach ($tagSeg in @($Latest, $LatestNoV)) {
  foreach ($nameSeg in @($LatestV, $LatestNoV)) {
    $candidate = "pi-$nameSeg-$target.tar.gz"
    $url       = "https://github.com/$RepoSlug/releases/download/$tagSeg/$candidate"
    Write-Bold "[Downloading] $candidate"
    try {
      $TarPath = Join-Path $Tmp $candidate
      Invoke-WebRequest -Uri $url -OutFile $TarPath -ErrorAction Stop
      $TarName = $candidate
      $SumName = "$candidate.sha256"
      $SumPath = Join-Path $Tmp $SumName
      try { Invoke-WebRequest -Uri "$url.sha256" -OutFile $SumPath -ErrorAction Stop } catch { }
      break download
    } catch {
      Remove-Item -Force $TarPath -ErrorAction SilentlyContinue
      $TarPath = $null
      Write-Dim "  miss: $url"
    }
  }
}

if (-not $TarName) {
  Write-Err "No prebuilt binary for $target in release $Latest"
  exit 1
}

try {

  if (Test-Path $SumPath) {
    $expected = (Get-Content $SumPath | Select-Object -First 1).Split(" ")[0]
    $got = (Get-FileHash -Algorithm SHA256 $TarPath).Hash.ToLower()
    if ($expected -ne $got) {
      Write-Err "sha256 mismatch (expected $expected, got $got)"
      exit 1
    }
    Write-Dim "  sha256 ok"
  } else {
    Write-Dim "  sha256 file missing - skipping verify"
  }

  # ── Extract + atomic swap ────────────────────────────────────────────────
  $Stage = Join-Path $Tmp "stage"
  New-Item -ItemType Directory -Path $Stage | Out-Null
  tar.exe -xzf $TarPath -C $Stage
  if (-not (Test-Path (Join-Path $Stage "pi.exe"))) {
    Write-Err "extracted tarball missing pi.exe"
    exit 1
  }

  New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
  $Backup = $null
  if (Test-Path $PiBinDir) {
    $Backup = "$PiBinDir.old.$([guid]::NewGuid().ToString('N'))"
    Move-Item $PiBinDir $Backup
  }
  Move-Item $Stage $PiBinDir
  if ($Backup) { Remove-Item -Recurse -Force $Backup }
} finally {
  Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}

# ── Make a launcher shim on PATH ───────────────────────────────────────────
New-Item -ItemType Directory -Force -Path $LinkDir | Out-Null
$Launcher = Join-Path $LinkDir "pi.cmd"
@"
@echo off
"$($PiBinDir)\pi.exe" %*
"@ | Set-Content -Path $Launcher -Encoding ASCII

Write-Bold "[OK] Installed pi $Latest"
Write-Host "  binary:  $($PiBinDir)\pi.exe"
Write-Host "  shim:    $Launcher"

# Add LinkDir to user PATH if missing
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not ($userPath -split ";" -contains $LinkDir)) {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$LinkDir", "User")
  Write-Dim "Added $LinkDir to user PATH. Open a new terminal to pick it up."
}

Write-Host ""
Write-Dim "Run ``pi`` to start. Run ``pi login`` to add a provider."

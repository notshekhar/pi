# pi installer (Windows PowerShell) — prebuilt binary from GitHub Releases.
# No runtime required.
#
#   irm https://raw.githubusercontent.com/notshekhar/pi/main/install.ps1 | iex
#
# Layout after install:
#   $env:USERPROFILE\.pi-bin\
#     ├── pi.exe
#     └── .version
#   Adds that directory to the user PATH, and to the current session so `pi`
#   works immediately rather than after a restart.
#
# Env knobs:
#   $env:PI_REPO_SLUG  notshekhar/pi
#   $env:PI_VERSION    vX.Y.Z   pin a specific tag
#   $env:PI_HOME       %USERPROFILE%\.pi-bin
#   $env:PI_FORCE      1        skip the "already up to date" gate
#   $env:PI_UNINSTALL  1        remove the install + PATH entry and exit

$ErrorActionPreference = "Stop"

# Windows PowerShell 5.1 on older .NET defaults may lack TLS 1.2, which GitHub
# requires — opt in without clobbering anything newer (no-op on PowerShell 7+).
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072
} catch {}

function Bold($msg) { Write-Host $msg -ForegroundColor White }
function Dim($msg)  { Write-Host $msg -ForegroundColor DarkGray }
function Err($msg)  { Write-Host $msg -ForegroundColor Red }

$RepoSlug   = if ($env:PI_REPO_SLUG) { $env:PI_REPO_SLUG } else { "notshekhar/pi" }
$PiHome     = if ($env:PI_HOME)      { $env:PI_HOME }      else { Join-Path $env:USERPROFILE ".pi-bin" }
$Force      = $env:PI_FORCE -eq "1"
$PinVersion = $env:PI_VERSION

# ── Uninstall ─────────────────────────────────────────────────────────────
if ($env:PI_UNINSTALL -eq "1") {
    Bold "▶ Uninstalling pi"
    if (Test-Path $PiHome) {
        Remove-Item -Recurse -Force $PiHome -ErrorAction SilentlyContinue
        Dim "  removed $PiHome"
    }
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath) {
        $kept = $userPath.Split(";") | Where-Object { $_ -and $_ -ne $PiHome }
        [Environment]::SetEnvironmentVariable("Path", ($kept -join ";"), "User")
        Dim "  removed $PiHome from the user PATH"
    }
    Bold "✓ Uninstalled."
    Dim  "  Your sessions and settings in ~\.pi-agent were kept."
    exit 0
}

Bold "▶ pi installer"

if (-not [Environment]::Is64BitOperatingSystem) {
    Err "32-bit Windows is not supported."
    exit 1
}
$target = "windows-x64"
if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64") {
    # No native windows-arm64 build yet; the x64 one runs under Windows 11's
    # x64 emulation, which is worth saying out loud rather than shipping a
    # binary that silently runs slower than the user expects.
    Dim "  Windows on ARM detected — installing the x64 build (runs emulated)."
}
Dim "  target: $target"

# ── Resolve the target version ────────────────────────────────────────────
# The releases/latest redirect first: it is not subject to the anonymous
# GitHub API rate limit. The API is the fallback.
function Resolve-LatestTag {
    try {
        $resp = Invoke-WebRequest "https://github.com/$RepoSlug/releases/latest" `
                                  -Method Head -MaximumRedirection 5 -UseBasicParsing
        $final = $resp.BaseResponse.ResponseUri                       # PowerShell 5.x
        if (-not $final) { $final = $resp.BaseResponse.RequestMessage.RequestUri }  # 7+
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
    Bold "▶ Resolving the latest release"
    $latest = Resolve-LatestTag
    if (-not $latest) {
        Err "Could not resolve the latest release tag from $RepoSlug."
        Err "  Set `$env:PI_VERSION = 'vX.Y.Z' to pin one."
        exit 1
    }
}
if (-not $latest.StartsWith("v")) { $latest = "v$latest" }

# ── Up-to-date gate ───────────────────────────────────────────────────────
$installed = ""
$versionFile = Join-Path $PiHome ".version"
if (Test-Path $versionFile) {
    try { $installed = (Get-Content $versionFile -Raw).Trim() } catch {}
}
if (-not $Force -and $installed) {
    try {
        if ([version]($latest.TrimStart("v")) -le [version]($installed.TrimStart("v"))) {
            Bold "✓ Up to date (installed $installed, latest $latest)"
            Dim  "  Set `$env:PI_FORCE = '1' to reinstall."
            exit 0
        }
    } catch {}
    Dim "  update: $installed → $latest"
} else {
    Dim "  installing $latest"
}

# ── Download + verify ─────────────────────────────────────────────────────
$tmpRoot = Join-Path ([System.IO.Path]::GetTempPath()) "pi-install-$(Get-Random)"
New-Item -ItemType Directory -Force -Path $tmpRoot | Out-Null

$base = "https://github.com/$RepoSlug/releases/download/$latest"
$url  = "$base/pi-$target.tar.gz"
$tar  = Join-Path $tmpRoot "pi.tar.gz"

# Streamed download with a live ■■■···  42% bar. Throws on an HTTP error; the
# caller falls back to Invoke-WebRequest on any failure at all (older hosts, a
# redirected console, a missing System.Net.Http) — a progress bar is never
# worth failing an install over.
function Download-WithProgress {
    param([string]$Url, [string]$OutFile)

    # Windows PowerShell 5.1 needs the assembly loaded explicitly.
    try { Add-Type -AssemblyName System.Net.Http -ErrorAction SilentlyContinue } catch {}

    $client = [System.Net.Http.HttpClient]::new()
    $client.DefaultRequestHeaders.UserAgent.ParseAdd("pi-installer")
    $stream = $null
    $file = $null
    try {
        $resp = $client.GetAsync($Url, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
        if (-not $resp.IsSuccessStatusCode) { throw "HTTP $([int]$resp.StatusCode)" }
        $total  = $resp.Content.Headers.ContentLength
        $stream = $resp.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $file   = [System.IO.File]::Create($OutFile)

        $buf = New-Object byte[] 262144
        $done = 0
        $width = 50
        $lastPct = -1
        try { [Console]::CursorVisible = $false } catch {}
        while (($n = $stream.Read($buf, 0, $buf.Length)) -gt 0) {
            $file.Write($buf, 0, $n)
            $done += $n
            if ($total) {
                $pct = [int][math]::Min(100, ($done * 100 / $total))
                if ($pct -ne $lastPct) {
                    $on = [int]($pct * $width / 100)
                    # "·" (U+00B7) rather than "･": it exists in the legacy
                    # conhost codepages, so an old terminal degrades to a dot
                    # instead of a replacement box.
                    $bar = ("■" * $on) + ("·" * ($width - $on))
                    Write-Host -NoNewline ("`r$bar {0,3}%" -f $pct) -ForegroundColor DarkYellow
                    $lastPct = $pct
                }
            }
        }
        if ($lastPct -ge 0) { Write-Host "" }
    } finally {
        try { [Console]::CursorVisible = $true } catch {}
        if ($file)   { $file.Dispose() }
        if ($stream) { $stream.Dispose() }
        $client.Dispose()
    }
}

Bold "▶ Downloading $($url.Split('/')[-1])"
$downloaded = $false
if (-not [Console]::IsOutputRedirected) {
    try {
        Download-WithProgress -Url $url -OutFile $tar
        $downloaded = $true
    } catch {
        Remove-Item $tar -Force -ErrorAction SilentlyContinue
    }
}
if (-not $downloaded) {
    try {
        Invoke-WebRequest -Uri $url -OutFile $tar -UseBasicParsing
    } catch {
        Err "download failed: $url"
        Err "  the release may not have a $target asset"
        exit 1
    }
}

try {
    $resp = Invoke-WebRequest -Uri "$url.sha256" -UseBasicParsing
    # .Content is byte[] when the server sends application/octet-stream.
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
    Err "the tarball is missing $target\pi.exe"
    exit 1
}
Set-Content -Path (Join-Path $srcDir ".version") -Value $latest -NoNewline

# ── Swap into place ───────────────────────────────────────────────────────
Bold "▶ Installing to $PiHome"
$parent = Split-Path $PiHome -Parent
if (-not (Test-Path $parent)) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }

# Sweep backups left by an earlier update. A running pi.exe cannot be deleted,
# only renamed — by now those locks are gone.
Get-ChildItem -Path $parent -Filter "$(Split-Path $PiHome -Leaf).old.*" -Directory -ErrorAction SilentlyContinue |
    ForEach-Object { Remove-Item -Recurse -Force $_.FullName -ErrorAction SilentlyContinue }

if (Test-Path $PiHome) {
    $backup = "$PiHome.old.$(Get-Random)"
    Move-Item -Force $PiHome $backup
    try { Remove-Item -Recurse -Force $backup -ErrorAction SilentlyContinue } catch {}
}
Move-Item -Force $srcDir $PiHome
Remove-Item -Recurse -Force $tmpRoot -ErrorAction SilentlyContinue

# ── PATH: persistent for the user, and this session so it works now ───────
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not $userPath) { $userPath = "" }
$paths = $userPath.Split(";") | Where-Object { $_ -ne "" }
if ($paths -notcontains $PiHome) {
    Bold "▶ Adding $PiHome to the user PATH"
    $newPath = if ($userPath) { "$userPath;$PiHome" } else { $PiHome }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
}
$sessionPaths = $env:Path.Split(";") | Where-Object { $_ -ne "" }
if ($sessionPaths -notcontains $PiHome) {
    $env:Path = "$env:Path;$PiHome"
    Dim "  PATH updated for this session too — ``pi`` works right away."
}

# ── Smoke test: the binary must actually run here ─────────────────────────
try {
    & (Join-Path $PiHome "pi.exe") --help *> $null
    Dim "  verified: it runs"
} catch {
    Err "the installed binary did not run: $_"
    exit 1
}

Bold "✓ Installed $latest"
Write-Host "  pi:      $(Join-Path $PiHome 'pi.exe')"
Write-Host "  target:  $PiHome"
Write-Host ""
Dim "Run ``pi`` to start, then ``/login`` inside it to add a provider."
Dim "First-run SmartScreen warning: click 'More info' → 'Run anyway'."
Dim "Docs: https://github.com/$RepoSlug#readme"

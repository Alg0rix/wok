# Install the Wok CLI from GitHub releases on Windows.
#
#   irm https://raw.githubusercontent.com/alg0rix/wok/main/install.ps1 | iex
#
# $env:WOK_VERSION = "v0.1.0" pins a release (default: latest).
# $env:WOK_INSTALL_DIR sets the destination (default: %LOCALAPPDATA%\Programs\wok).
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'alg0rix/wok'
$version = if ($env:WOK_VERSION) { $env:WOK_VERSION } else { 'latest' }
$dir = if ($env:WOK_INSTALL_DIR) { $env:WOK_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\wok' }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "wok install: unsupported architecture $env:PROCESSOR_ARCHITECTURE" }
}

if ($version -eq 'latest') {
    $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name
    if (-not $version) { throw 'wok install: could not resolve latest release' }
}

$name = "wok_$($version.TrimStart('v'))_windows_$arch"
$base = "https://github.com/$repo/releases/download/$version"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "Downloading Wok $version (windows/$arch)"
    $archive = Join-Path $tmp "$name.zip"
    Invoke-WebRequest "$base/$name.zip" -OutFile $archive -UseBasicParsing
    $sums = (Invoke-WebRequest "$base/checksums.txt" -UseBasicParsing).Content
    $line = ($sums -split "`n") | Where-Object { ($_ -split '\s+')[1] -eq "$name.zip" }
    if (-not $line) { throw "wok install: no checksum for $name.zip" }
    $expected = ($line -split '\s+')[0]
    $actual = (Get-FileHash $archive -Algorithm SHA256).Hash.ToLower()
    if ($expected -ne $actual) { throw "wok install: checksum mismatch for $name.zip" }

    Expand-Archive $archive -DestinationPath $tmp
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item (Join-Path $tmp "$name\wok.exe") (Join-Path $dir 'wok.exe') -Force
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$exe = Join-Path $dir 'wok.exe'
Write-Host "Installed $(& $exe --version) to $exe"
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $dir) {
    [Environment]::SetEnvironmentVariable('Path', "$dir;$userPath", 'User')
    Write-Host "Added $dir to your user PATH; restart the terminal to use 'wok'."
}
Write-Host "Chromium headless shell downloads automatically on first 'wok start'."

$ErrorActionPreference = 'Stop'

$baseUrl = if ($env:PORTLIGHT_BASE_URL) { $env:PORTLIGHT_BASE_URL.TrimEnd('/') } else { 'https://portlight.616.pub' }
$installDir = if ($env:PORTLIGHT_INSTALL_DIR) { $env:PORTLIGHT_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\portlight' }

$arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()) {
  'x64' { 'amd64' }
  'x86' { 'amd64' }
  'arm64' { 'arm64' }
  default { throw "unsupported architecture: $([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)" }
}

New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$target = Join-Path $installDir 'portlight.exe'
$release = Invoke-RestMethod -Uri "$baseUrl/releases/latest.json"
$file = $release.files | Where-Object { $_.os -eq 'windows' -and $_.arch -eq $arch } | Select-Object -First 1
if (-not $file) {
  throw "release metadata missing windows/$arch"
}

Invoke-WebRequest -Uri $file.url -OutFile $target
$gotHash = (Get-FileHash -Algorithm SHA256 $target).Hash.ToLowerInvariant()
$wantHash = ([string]$file.sha256).ToLowerInvariant()
if ($gotHash -ne $wantHash) {
  Remove-Item -LiteralPath $target -Force
  throw "checksum mismatch"
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $installDir) {
  $newPath = if ($userPath) { $userPath.TrimEnd(';') + ';' + $installDir } else { $installDir }
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  Write-Host "Added $installDir to the user PATH. Restart the shell if portlight is not found."
}

& $target --version

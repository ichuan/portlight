param(
  [string] $Version = 'dev'
)

$ErrorActionPreference = 'Stop'

$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$outRoot = Join-Path $repo 'dist'
$targets = @(
  @{ os = 'linux';   arch = 'amd64'; ext = '' },
  @{ os = 'linux';   arch = 'arm64'; ext = '' },
  @{ os = 'darwin';  arch = 'amd64'; ext = '' },
  @{ os = 'darwin';  arch = 'arm64'; ext = '' },
  @{ os = 'windows'; arch = 'amd64'; ext = '.exe' },
  @{ os = 'windows'; arch = 'arm64'; ext = '.exe' }
)

Set-Location $repo
foreach ($target in $targets) {
  $dir = Join-Path $outRoot "$($target.os)-$($target.arch)"
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  $out = Join-Path $dir ("portlight" + $target.ext)
  $env:GOOS = $target.os
  $env:GOARCH = $target.arch
  $env:CGO_ENABLED = '0'
  Write-Host "==> Building $($target.os)/$($target.arch) portlight $Version"
  & go build -trimpath -ldflags "-s -w -X main.version=$Version" -o $out ./cmd/portlight
  if ($LASTEXITCODE -ne 0) {
    throw "build failed for $($target.os)/$($target.arch)"
  }
}

Remove-Item env:GOOS, env:GOARCH, env:CGO_ENABLED -ErrorAction SilentlyContinue
Get-ChildItem -Recurse $outRoot -File | ForEach-Object {
  "{0}  {1,12} bytes" -f $_.FullName, $_.Length
}

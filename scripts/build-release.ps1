param(
  [string] $Version = 'dev',
  [string] $PublicBase = 'https://portlight.616.pub',
  [switch] $PublishSite
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

if ($PublishSite) {
  $siteRoot = Join-Path $repo 'site'
  $downloadsRoot = Join-Path $siteRoot 'downloads'
  $releasesRoot = Join-Path $siteRoot 'releases'
  New-Item -ItemType Directory -Force -Path $downloadsRoot, $releasesRoot | Out-Null

  $files = @()
  foreach ($target in $targets) {
    $source = Join-Path (Join-Path $outRoot "$($target.os)-$($target.arch)") ("portlight" + $target.ext)
    $name = "portlight-$($target.os)-$($target.arch)$($target.ext)"
    $dest = Join-Path $downloadsRoot $name
    Copy-Item -Force $source $dest
    $hash = (Get-FileHash -Algorithm SHA256 $dest).Hash.ToLowerInvariant()
    $files += [ordered]@{
      os = $target.os
      arch = $target.arch
      name = $name
      url = "$($PublicBase.TrimEnd('/'))/downloads/$name"
      sha256 = $hash
      size = (Get-Item $dest).Length
    }
  }

  $latest = [ordered]@{
    version = $Version
    generatedAt = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
    files = $files
  }
  $json = $latest | ConvertTo-Json -Depth 5
  $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
  [System.IO.File]::WriteAllText((Join-Path $releasesRoot 'latest.json'), $json + [Environment]::NewLine, $utf8NoBom)
  Write-Host "Wrote site release metadata and downloads under $siteRoot"
}

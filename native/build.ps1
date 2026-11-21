param([switch]$Run, [switch]$Demo)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$env:GOPATH = Join-Path $repo '.build/go'
$env:GOCACHE = Join-Path $repo '.build/go-cache'
$env:GOTELEMETRY = 'off'
$env:GOTOOLCHAIN = 'go1.27.4'
$build = Join-Path $PSScriptRoot 'build'
New-Item -ItemType Directory -Force -Path $build | Out-Null
Copy-Item -LiteralPath (Join-Path $repo 'assets/brand/webhola-icon.svg') -Destination (Join-Path $PSScriptRoot 'internal/desktop/resources/icon.svg')
Copy-Item -LiteralPath (Join-Path $repo 'assets/brand/webhola-icon.png') -Destination (Join-Path $PSScriptRoot 'internal/desktop/resources/icon.png')
Push-Location $PSScriptRoot
try {
    go build -trimpath -ldflags '-H=windowsgui' -o (Join-Path $build 'Webhola.exe') .
    if ($LASTEXITCODE -ne 0) { throw 'Webhola build failed' }
    if ($Run -or $Demo) {
        $arguments = @()
        if ($Demo) { $arguments += '--demo' }
        if ($arguments.Count) { Start-Process -FilePath (Join-Path $build 'Webhola.exe') -ArgumentList $arguments -WorkingDirectory $build -WindowStyle Hidden }
        else { Start-Process -FilePath (Join-Path $build 'Webhola.exe') -WorkingDirectory $build -WindowStyle Hidden }
    }
} finally { Pop-Location }

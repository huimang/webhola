param(
    [string]$NpcapPackage
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$testRoot = Join-Path $repo ('.build/installer-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force $testRoot | Out-Null
$support = Join-Path $PSScriptRoot 'Dependencies.ps1'
$powershell = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$state = Join-Path $testRoot 'state.ini'
function Run-Support([string]$Action, [string]$Package = '', [switch]$ExpectFailure) {
    $arguments = @('-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$support,'-Action',$Action,'-StateFile',$state)
    if ($Package) { $arguments += @('-PackagePath',$Package) }
    & $powershell @arguments
    if ($ExpectFailure) { if ($LASTEXITCODE -eq 0) { throw "Expected failure: $Action" } }
    elseif ($LASTEXITCODE -ne 0) { throw "Unexpected failure: $Action" }
}
Run-Support 'Detect'
$detected = Get-Content -LiteralPath $state -Raw -Encoding Unicode
if ($detected -notmatch 'NpcapInstalled=[01]' -or $detected -notmatch 'CaptureReady=[01]') { throw 'Capture state was not reported' }
Run-Support 'VerifyNpcap' (Join-Path $repo 'native/build/Webhola.exe') -ExpectFailure
if ($NpcapPackage) { Run-Support 'VerifyNpcap' $NpcapPackage }
Write-Output 'PASS: Npcap detection and package signature checks.'
$global:LASTEXITCODE = 0

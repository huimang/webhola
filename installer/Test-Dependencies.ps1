param(
    [string]$TShark,
    [string]$WiresharkPackage,
    [string]$NpcapPackage
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
if (!$TShark) { $TShark = Join-Path $repo '.build/wireshark/App/Wireshark/tshark.exe' }
$testRoot = Join-Path $repo ('.build/installer-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force $testRoot | Out-Null
$support = Join-Path $PSScriptRoot 'Dependencies.ps1'
$powershell = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$state = Join-Path $testRoot 'state.ini'
$config = Join-Path $testRoot 'settings.json'
$env:WEBHOLA_TSHARK = $TShark
function Run-Support([string]$Action, [string]$Package = '', [switch]$ExpectFailure) {
    $arguments = @('-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$support,'-Action',$Action,'-ConfigPath',$config,'-StateFile',$state)
    if ($Package) { $arguments += @('-PackagePath',$Package) }
    & $powershell @arguments
    if ($ExpectFailure) { if ($LASTEXITCODE -eq 0) { throw "Expected failure: $Action" } }
    elseif ($LASTEXITCODE -ne 0) { throw "Unexpected failure: $Action" }
}
Run-Support 'Detect'
if (!(Get-Content -LiteralPath $state -Raw -Encoding Unicode).Contains($TShark)) { throw 'Custom TShark path was not detected' }
$initial = @{tsharkPath=$TShark;keep='preserved'} | ConvertTo-Json
[IO.File]::WriteAllText($config,$initial,(New-Object Text.UTF8Encoding $false))
Run-Support 'Configure'
$saved = Get-Content -LiteralPath $config -Raw -Encoding UTF8 | ConvertFrom-Json
if ($saved.tsharkPath -ne $TShark -or $saved.keep -ne 'preserved') { throw 'Configuration did not round-trip' }
[IO.File]::WriteAllText($config,'[]')
Run-Support 'Configure' -ExpectFailure
if ([IO.File]::ReadAllText($config) -ne '[]') { throw 'Invalid existing configuration was overwritten' }
Run-Support 'VerifyWireshark' (Join-Path $repo 'native/build/Webhola.exe') -ExpectFailure
if ($WiresharkPackage) { Run-Support 'VerifyWireshark' $WiresharkPackage }
if ($NpcapPackage) {
    Run-Support 'VerifyNpcap' $NpcapPackage
    Run-Support 'VerifyWireshark' $NpcapPackage -ExpectFailure
}
Write-Output 'PASS: dependency detection, configuration persistence, invalid-config preservation, and package signature checks.'
$global:LASTEXITCODE = 0

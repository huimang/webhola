param(
    [string]$JdkPackage = (Join-Path (Split-Path -Parent $PSScriptRoot) '.build\installer-tools\temurin21-jdk.zip'),
    [string]$ArthasPackage = (Join-Path (Split-Path -Parent $PSScriptRoot) '.build\installer-tools\arthas-4.3.5-bin.zip')
)
$ErrorActionPreference='Stop'
$repo=Split-Path -Parent $PSScriptRoot
$testRoot=Join-Path $repo ('.build\java-installer-test-'+[guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($testRoot) | Out-Null
$root=Join-Path $testRoot 'dependencies'
$config=Join-Path $testRoot 'settings.json'
$state=Join-Path $testRoot 'state.ini'
$support=Join-Path $PSScriptRoot 'JavaDependencies.ps1'
$powershell=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$oldPath=$env:PATH; $oldHome=$env:JAVA_HOME; $oldJava=$env:WEBHOLA_JAVA; $oldBoot=$env:WEBHOLA_ARTHAS_BOOT
function Run-Support([string]$Action,[string]$Jdk=$JdkPackage,[switch]$Fail) {
    & $powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $support -Action $Action -DependencyRoot $root -ConfigPath $config -StateFile $state -JdkPackage $Jdk -ArthasPackage $ArthasPackage
    if ($Fail) { if ($LASTEXITCODE -eq 0) { throw 'Expected failure' } }
    elseif ($LASTEXITCODE -ne 0) { throw "Unexpected failure: $Action" }
}
try {
    $env:PATH=''; $env:JAVA_HOME=''; $env:WEBHOLA_JAVA=''; $env:WEBHOLA_ARTHAS_BOOT=''
    [IO.File]::WriteAllText($config,'{"legacySetting":"keep","arthasHttpPort":"18563","other":{"text":"保留"}}',(New-Object Text.UTF8Encoding $false))
    Run-Support 'Detect'
    if ((Get-Content -LiteralPath $state -Raw -Encoding Unicode) -notmatch 'JavaReady=0') { throw 'Missing dependencies reported ready' }
    $bad=Join-Path $testRoot 'bad.zip'; [IO.File]::WriteAllText($bad,'invalid')
    Run-Support 'Install' -Jdk $bad -Fail
    if (Test-Path -LiteralPath (Join-Path $root 'temurin-21.0.13.1-2')) { throw 'Invalid archive installed' }
    Run-Support 'Install'
    $saved=Get-Content -LiteralPath $config -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($saved.legacySetting -ne 'keep' -or $saved.arthasHttpPort -ne '18563' -or $saved.other.text -ne '保留') { throw 'Existing configuration changed' }
    if (!(Test-Path -LiteralPath $saved.javaPath) -or !(Test-Path -LiteralPath $saved.arthasBootPath)) { throw 'Dependency paths not saved' }
    if ((Get-Content -LiteralPath $state -Raw -Encoding Unicode) -notmatch 'JavaReady=1') { throw 'Installed components not ready' }
    # Reuse succeeds with no downloadable archives, preserving custom paths.
    Run-Support 'Install' -Jdk $bad
    $env:WEBHOLA_JAVA=$saved.javaPath; $env:WEBHOLA_ARTHAS_BOOT=$saved.arthasBootPath
    [IO.File]::WriteAllText($config,'{}')
    Run-Support 'Install' -Jdk $bad
    $defaults=Get-Content -LiteralPath $config -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($defaults.arthasHttpPort -ne '8563') { throw 'Default port missing' }
    [IO.File]::WriteAllText($config,'[]')
    Run-Support 'Install' -Fail
    if ([IO.File]::ReadAllText($config) -ne '[]') { throw 'Invalid configuration overwritten' }
    Write-Output "Java dependency checks passed. Test tools: $root"
} finally {
    $env:PATH=$oldPath; $env:JAVA_HOME=$oldHome; $env:WEBHOLA_JAVA=$oldJava; $env:WEBHOLA_ARTHAS_BOOT=$oldBoot
}
$global:LASTEXITCODE=0

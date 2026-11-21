param([string]$Compiler, [switch]$SkipAppBuild)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
if (!$SkipAppBuild) { & (Join-Path $repo 'native/build.ps1') }
if (!(Test-Path -LiteralPath (Join-Path $repo 'native/build/Webhola.exe'))) { throw 'Build Webhola.exe first.' }
if (!$Compiler) {
    $local = Join-Path $repo '.build/installer-tools/inno/ISCC.exe'
    if (Test-Path -LiteralPath $local) { $Compiler = $local }
    else { $command = Get-Command ISCC.exe -ErrorAction SilentlyContinue; if ($command) { $Compiler=$command.Source } }
}
if (!$Compiler) { throw 'Provide -Compiler with the path to Inno Setup 6.7+ ISCC.exe.' }
$language = Join-Path $repo '.build/installer-tools/ChineseSimplified.isl'
if (!(Test-Path -LiteralPath $language)) {
    New-Item -ItemType Directory -Force (Split-Path -Parent $language) | Out-Null
    Invoke-WebRequest -Uri 'https://raw.githubusercontent.com/jrsoftware/issrc/refs/heads/main/Files/Languages/ChineseSimplified.isl' -OutFile $language
}
& $Compiler "/DLanguageFile=$language" (Join-Path $PSScriptRoot 'Webhola.iss')
if ($LASTEXITCODE -ne 0) { throw 'Installer compilation failed' }
$setup = Join-Path $repo 'native/build/Webhola-Setup-0.2.0-x64.exe'
$hash = (Get-FileHash -LiteralPath $setup -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText($setup+'.sha256',($hash+'  '+[IO.Path]::GetFileName($setup)+[Environment]::NewLine),(New-Object Text.UTF8Encoding $false))

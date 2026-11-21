# Optional: regenerate the checked-in Windows icon/version resource after a brand change.
# Requires Windows SDK and Visual Studio Build Tools. Normal builds use the existing .syso.
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$rc = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\rc.exe" | Sort-Object FullName -Descending | Select-Object -First 1
$cvt = Get-ChildItem "${env:ProgramFiles(x86)}\Microsoft Visual Studio\*\BuildTools\VC\Tools\MSVC\*\bin\Hostx64\x64\cvtres.exe" | Sort-Object FullName -Descending | Select-Object -First 1
if (!$rc -or !$cvt) { throw 'Windows SDK and Visual Studio Build Tools are required to regenerate resources.' }
$res = Join-Path $repo '.build/webhola.res'
New-Item -ItemType Directory -Force (Join-Path $repo '.build') | Out-Null
Push-Location $PSScriptRoot
try {
    & $rc.FullName /nologo /fo $res resources/webhola.rc
    if ($LASTEXITCODE -ne 0) { throw 'Resource compilation failed' }
    & $cvt.FullName /nologo /machine:x64 "/out:$PSScriptRoot/resource_windows_amd64.syso" $res
    if ($LASTEXITCODE -ne 0) { throw 'Resource conversion failed' }
    # MSVC emits an absolute @comp.id metadata symbol. Go's internal linker
    # accepts it as a debug symbol; it has no resource relocations referring to it.
    $objectPath = Join-Path $PSScriptRoot 'resource_windows_amd64.syso'
    $bytes = [IO.File]::ReadAllBytes($objectPath)
    $symbolOffset = [BitConverter]::ToUInt32($bytes, 8)
    $symbolCount = [BitConverter]::ToUInt32($bytes, 12)
    for ($i = 0; $i -lt $symbolCount; $i++) {
        $offset = $symbolOffset + 18 * $i
        if ([Text.Encoding]::ASCII.GetString($bytes, $offset, 8) -eq '@comp.id') {
            $bytes[$offset + 12] = 254
            $bytes[$offset + 13] = 255
        }
    }
    [IO.File]::WriteAllBytes($objectPath, $bytes)
} finally { Pop-Location }

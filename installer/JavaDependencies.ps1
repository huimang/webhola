param(
    [Parameter(Mandatory=$true)][ValidateSet('Detect','Install')][string]$Action,
    [Parameter(Mandatory=$true)][string]$DependencyRoot,
    [string]$StateFile,
    [string]$JdkPackage,
    [string]$ArthasPackage,
    [ValidateSet('zh-CN','en')][string]$Language = 'zh-CN',
    [string]$ConfigPath = (Join-Path $env:APPDATA 'Webhola\settings.json')
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'I18n.ps1')
Add-Type -AssemblyName System.IO.Compression.FileSystem
$jdkHash = 'BBEBEA1469996B276683C9299C227B294F85C847039C82633218186A4AD4FBF2'
$arthasHash = '2F75EA954EAD5D02C4A743ECD783FD92F546A44EF3AEE437EA5066EB30AD07BF'
$DependencyRoot = [IO.Path]::GetFullPath($DependencyRoot).TrimEnd('\')
$jdkHome = Join-Path $DependencyRoot 'temurin-21.0.13.1-2'
$arthasHome = Join-Path $DependencyRoot 'arthas-4.3.5'

function Read-Config {
    if (!(Test-Path -LiteralPath $ConfigPath)) { return [pscustomobject]@{} }
    $config = Get-Content -LiteralPath $ConfigPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($null -eq $config -or $config.GetType().FullName -ne 'System.Management.Automation.PSCustomObject') { throw (Get-InstallerMessage 'InvalidConfig') }
    return $config
}

function Test-Jdk([string]$Java) {
    if (!$Java -or !(Test-Path -LiteralPath $Java -PathType Leaf)) { return $false }
    $bin = Split-Path -Parent $Java
    if (!(Test-Path -LiteralPath (Join-Path $bin 'javac.exe')) -or !(Test-Path -LiteralPath (Join-Path $bin 'jcmd.exe'))) { return $false }
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $Java; $info.Arguments = '-version'
    $info.UseShellExecute = $false; $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true; $info.RedirectStandardError = $true
    try {
        $process = [Diagnostics.Process]::Start($info)
        try {
            $stdout=$process.StandardOutput.ReadToEndAsync(); $stderr=$process.StandardError.ReadToEndAsync()
            if (!$process.WaitForExit(10000)) { $process.Kill(); return $false }
            return $process.ExitCode -eq 0 -and ($stdout.Result + $stderr.Result) -match '64-Bit'
        } finally { $process.Dispose() }
    } catch { return $false }
}

function Test-Arthas([string]$Boot) {
    if (!$Boot) { return $false }
    $directory = Split-Path -Parent $Boot
    foreach ($jar in @('arthas-boot.jar','arthas-core.jar','arthas-agent.jar','arthas-spy.jar')) {
        $path = Join-Path $directory $jar
        if (!(Test-Path -LiteralPath $path -PathType Leaf)) { return $false }
        try {
            $zip = [IO.Compression.ZipFile]::OpenRead($path)
            try {
                if (!$zip.GetEntry('META-INF/MANIFEST.MF')) { return $false }
                if ($jar -eq 'arthas-boot.jar' -and !$zip.GetEntry('com/taobao/arthas/boot/Bootstrap.class')) { return $false }
            } finally { $zip.Dispose() }
        } catch { return $false }
    }
    return $true
}

function Detect {
    $config = Read-Config
    $candidates = @($config.javaPath,$env:WEBHOLA_JAVA)
    if ($env:JAVA_HOME) { $candidates += Join-Path $env:JAVA_HOME 'bin\java.exe' }
    foreach ($name in @('javac.exe','java.exe')) {
        foreach ($command in @(Get-Command $name -All -ErrorAction SilentlyContinue)) {
            $candidates += Join-Path (Split-Path -Parent $command.Source) 'java.exe'
        }
    }
    $candidates += Join-Path $jdkHome 'bin\java.exe'
    $java = ''
    foreach ($candidate in ($candidates | Where-Object { $_ } | Select-Object -Unique)) {
        if (Test-Jdk $candidate) { $java=[IO.Path]::GetFullPath($candidate); break }
    }
    $boot = ''
    foreach ($candidate in @($config.arthasBootPath,$env:WEBHOLA_ARTHAS_BOOT,(Join-Path $arthasHome 'arthas-boot.jar'))) {
        if (Test-Arthas $candidate) { $boot=[IO.Path]::GetFullPath($candidate); break }
    }
    return [pscustomobject]@{JavaPath=$java; BootPath=$boot; Error=''}
}

function Assert-Owned([string]$Path) {
    $full = [IO.Path]::GetFullPath($Path)
    if (!$full.StartsWith($DependencyRoot + '\',[StringComparison]::OrdinalIgnoreCase)) { throw (Get-InstallerMessage 'DependencyOutsideRoot') }
    if (Test-Path -LiteralPath $DependencyRoot) {
        $item = Get-Item -LiteralPath $DependencyRoot
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw (Get-InstallerMessage 'DependencyDirectoryLink') }
    }
    $current = $full
    while ($current -and $current -ne $DependencyRoot) {
        if (Test-Path -LiteralPath $current) {
            if ((Get-Item -LiteralPath $current).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw (Get-InstallerMessage 'DependencyPathLink') }
        }
        $current = Split-Path -Parent $current
    }
    return $full
}

function Install-Archive([string]$Package,[string]$Hash,[string]$Destination,[bool]$IsJdk) {
    if (!$Package -or (Get-FileHash -LiteralPath $Package -Algorithm SHA256).Hash -ne $Hash) { throw (Get-InstallerMessage 'DependencyHashFailed') }
    $destination = Assert-Owned $Destination
    $staging = Assert-Owned (Join-Path $DependencyRoot ([IO.Path]::GetRandomFileName()))
    $backup = Assert-Owned (Join-Path $DependencyRoot ([IO.Path]::GetRandomFileName()))
    $installed = $false
    [IO.Directory]::CreateDirectory($staging) | Out-Null
    try {
        $zip = [IO.Compression.ZipFile]::OpenRead($Package)
        try {
            foreach ($entry in $zip.Entries) {
                $target = [IO.Path]::GetFullPath((Join-Path $staging $entry.FullName))
                if ([IO.Path]::IsPathRooted($entry.FullName) -or !$target.StartsWith($staging+'\',[StringComparison]::OrdinalIgnoreCase) -or $entry.FullName.Contains(':') -or (($entry.ExternalAttributes -shr 16) -band 0xF000) -eq 0xA000) { throw (Get-InstallerMessage 'UnsafeArchivePath') }
            }
        } finally { $zip.Dispose() }
        [IO.Compression.ZipFile]::ExtractToDirectory($Package,$staging)
        $source=$staging
        if ($IsJdk) {
            $children=@(Get-ChildItem -LiteralPath $staging -Directory)
            if ($children.Count -ne 1) { throw (Get-InstallerMessage 'InvalidJdkArchive') }
            $source=Assert-Owned $children[0].FullName
            if (!(Test-Jdk (Join-Path $source 'bin\java.exe'))) { throw (Get-InstallerMessage 'JdkVerificationFailed') }
        } elseif (!(Test-Arthas (Join-Path $source 'arthas-boot.jar'))) { throw (Get-InstallerMessage 'ArthasVerificationFailed') }
        if (Test-Path -LiteralPath $destination) { Move-Item -LiteralPath (Assert-Owned $destination) -Destination $backup }
        try { Move-Item -LiteralPath (Assert-Owned $source) -Destination $destination }
        catch {
            if (Test-Path -LiteralPath $backup) { Move-Item -LiteralPath (Assert-Owned $backup) -Destination $destination }
            throw
        }
        $installed = $true
    } finally {
        $cleanup = @($staging)
        if ($installed) { $cleanup += $backup }
        foreach ($temporary in $cleanup) {
            if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath (Assert-Owned $temporary) -Recurse -Force }
        }
    }
}

function Save-Config($State) {
    $config = Read-Config
    $config | Add-Member -NotePropertyName javaPath -NotePropertyValue $State.JavaPath -Force
    $config | Add-Member -NotePropertyName arthasBootPath -NotePropertyValue $State.BootPath -Force
    if (!$config.arthasHttpPort) { $config | Add-Member -NotePropertyName arthasHttpPort -NotePropertyValue '8563' -Force }
    $directory = Split-Path -Parent $ConfigPath
    [IO.Directory]::CreateDirectory($directory) | Out-Null
    $temporary=Join-Path $directory ([IO.Path]::GetRandomFileName())
    $backup=Join-Path $directory ([IO.Path]::GetRandomFileName())
    $committed=$false
    try {
        [IO.File]::WriteAllText($temporary,($config | ConvertTo-Json -Depth 32),(New-Object Text.UTF8Encoding $false))
        if (Test-Path -LiteralPath $ConfigPath) { Move-Item -LiteralPath $ConfigPath -Destination $backup }
        Move-Item -LiteralPath $temporary -Destination $ConfigPath
        $saved=Read-Config
        if ($saved.javaPath -ne $State.JavaPath -or $saved.arthasBootPath -ne $State.BootPath) { throw (Get-InstallerMessage 'JavaReadbackFailed') }
        $committed=$true
    } catch {
        if (Test-Path -LiteralPath $backup) {
            if (Test-Path -LiteralPath $ConfigPath) { Remove-Item -LiteralPath $ConfigPath }
            Move-Item -LiteralPath $backup -Destination $ConfigPath
        }
        throw
    } finally {
        if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary }
        if ($committed -and (Test-Path -LiteralPath $backup)) { Remove-Item -LiteralPath $backup }
    }
}

function Write-State($State) {
    if (!$StateFile) { return }
    $ready=[int][bool]($State.JavaPath -and $State.BootPath -and !$State.Error)
    [IO.File]::WriteAllText($StateFile,"[State]`r`nJavaPath=$($State.JavaPath)`r`nBootPath=$($State.BootPath)`r`nJavaReady=$ready`r`nError=$($State.Error -replace '[\r\n]+',' ')`r`n",[Text.Encoding]::Unicode)
}

try {
    $state=Detect
    if ($Action -eq 'Install') {
        if (!$state.JavaPath) { Install-Archive $JdkPackage $jdkHash $jdkHome $true }
        if (!$state.BootPath) { Install-Archive $ArthasPackage $arthasHash $arthasHome $false }
        $state=Detect
        if (!$state.JavaPath -or !$state.BootPath) { throw (Get-InstallerMessage 'JavaInstallFailed') }
        Save-Config $state
    }
    Write-State $state
    exit 0
} catch {
    Write-State ([pscustomobject]@{JavaPath=''; BootPath=''; Error=$_.Exception.Message})
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}

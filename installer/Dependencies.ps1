param(
    [Parameter(Mandatory=$true)]
    [ValidateSet('Detect','Configure','VerifyWireshark','VerifyNpcap')][string]$Action,
    [string]$StateFile,
    [string]$PackagePath,
    [ValidateSet('zh-CN','en')][string]$Language = 'zh-CN',
    [string]$ConfigPath = (Join-Path $env:APPDATA 'Webhola\settings.json')
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'I18n.ps1')

function Invoke-TShark([string]$Tool, [string]$Arguments) {
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $Tool
    $info.Arguments = $Arguments
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $process = [Diagnostics.Process]::Start($info)
    try {
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (!$process.WaitForExit(10000)) { $process.Kill(); throw (Get-InstallerMessage 'TSharkTimeout') }
        [pscustomobject]@{ Code=$process.ExitCode; Text=($stdout.Result + $stderr.Result) }
    } finally { $process.Dispose() }
}

function Find-TShark {
    $candidates = @()
    if (Test-Path -LiteralPath $ConfigPath) {
        try { $config = Get-Content -LiteralPath $ConfigPath -Raw -Encoding UTF8 | ConvertFrom-Json; $candidates += $config.tsharkPath } catch { }
    }
    $candidates += $env:WEBHOLA_TSHARK
    $command = Get-Command tshark.exe -ErrorAction SilentlyContinue
    if ($command) { $candidates += $command.Source }
    foreach ($root in @($env:ProgramW6432,$env:ProgramFiles,${env:ProgramFiles(x86)})) {
        if ($root) { $candidates += Join-Path $root 'Wireshark\tshark.exe' }
    }
    foreach ($candidate in ($candidates | Where-Object { $_ } | Select-Object -Unique)) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            try { $result=Invoke-TShark $candidate '--version'; if ($result.Code -eq 0 -and $result.Text -match '(?i)tshark') { return [IO.Path]::GetFullPath($candidate) } } catch { }
        }
    }
    return ''
}

function Get-DependencyState {
    $tool = Find-TShark
    $npcap = (Test-Path -LiteralPath (Join-Path $env:SystemRoot 'System32\Npcap\wpcap.dll')) -and (Test-Path 'HKLM:\SYSTEM\CurrentControlSet\Services\npcap')
    $ready = $false
    $reason = ''
    if ($tool) {
        try {
            $result = Invoke-TShark $tool '-D'
            $ready = $result.Code -eq 0 -and $result.Text -match '(?m)^\s*\d+\. \\Device\\NPF_'
            if (!$ready) { $reason = (Get-InstallerMessage 'NoCaptureAdapters') }
        } catch { $reason = $_.Exception.Message }
    } else { $reason = (Get-InstallerMessage 'TSharkMissing') }
    [pscustomobject]@{ TSharkPath=$tool; NpcapInstalled=$npcap; CaptureReady=$ready; Error=$reason }
}

function Write-State($State) {
    if (!$StateFile) { return }
    $text = "[State]`r`nTSharkPath=$($State.TSharkPath)`r`nNpcapInstalled=$([int][bool]$State.NpcapInstalled)`r`nCaptureReady=$([int][bool]$State.CaptureReady)`r`nError=$($State.Error -replace '[\r\n]+',' ')`r`n"
    [IO.File]::WriteAllText($StateFile,$text,[Text.Encoding]::Unicode)
}

function Assert-OfficialSignature([string]$File,[string[]]$Publishers) {
    $signature = Get-AuthenticodeSignature -LiteralPath $File
    if ($signature.Status -ne 'Valid' -or !$signature.SignerCertificate) { throw (Get-InstallerMessage 'InvalidSignature') }
    $name = $signature.SignerCertificate.GetNameInfo([Security.Cryptography.X509Certificates.X509NameType]::SimpleName,$false)
    if ($Publishers -notcontains $name) { throw (Get-InstallerMessage 'PublisherMismatch' @($name)) }
}

try {
    if ($Action -eq 'VerifyWireshark') { Assert-OfficialSignature $PackagePath @('Wireshark Foundation'); exit 0 }
    if ($Action -eq 'VerifyNpcap') { Assert-OfficialSignature $PackagePath @('Insecure.Com LLC','Nmap Software LLC'); exit 0 }
    $state = Get-DependencyState
    if ($Action -eq 'Configure') {
        if (!$state.TSharkPath) { throw (Get-InstallerMessage 'TSharkInstallFailed') }
        $config = [pscustomobject]@{}
        if (Test-Path -LiteralPath $ConfigPath) { $config = Get-Content -LiteralPath $ConfigPath -Raw -Encoding UTF8 | ConvertFrom-Json }
        if ($null -eq $config -or $config.GetType().FullName -ne 'System.Management.Automation.PSCustomObject') { throw (Get-InstallerMessage 'InvalidConfig') }
        $config | Add-Member -NotePropertyName tsharkPath -NotePropertyValue $state.TSharkPath -Force
        $directory = Split-Path -Parent $ConfigPath
        [IO.Directory]::CreateDirectory($directory) | Out-Null
        $temporary = Join-Path $directory ([IO.Path]::GetRandomFileName())
        $backup = Join-Path $directory ([IO.Path]::GetRandomFileName())
        $committed = $false
        try {
            [IO.File]::WriteAllText($temporary,($config | ConvertTo-Json -Depth 32),(New-Object Text.UTF8Encoding $false))
            if (Test-Path -LiteralPath $ConfigPath) { Move-Item -LiteralPath $ConfigPath -Destination $backup }
            try { Move-Item -LiteralPath $temporary -Destination $ConfigPath }
            catch {
                if (Test-Path -LiteralPath $backup) { Move-Item -LiteralPath $backup -Destination $ConfigPath }
                throw
            }
            $saved = Get-Content -LiteralPath $ConfigPath -Raw -Encoding UTF8 | ConvertFrom-Json
            if ($saved.tsharkPath -ne $state.TSharkPath) { throw (Get-InstallerMessage 'TSharkReadbackFailed') }
            $committed = $true
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
    Write-State $state
    exit 0
} catch {
    Write-State ([pscustomobject]@{ TSharkPath=''; NpcapInstalled=$false; CaptureReady=$false; Error=$_.Exception.Message })
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}

param(
    [Parameter(Mandatory=$true)]
    [ValidateSet('Detect','VerifyNpcap')][string]$Action,
    [string]$StateFile,
    [string]$PackagePath,
    [ValidateSet('zh-CN','en')][string]$Language = 'zh-CN'
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'I18n.ps1')

function Get-DependencyState {
    $npcap = (Test-Path -LiteralPath (Join-Path $env:SystemRoot 'System32\Npcap\wpcap.dll')) -and (Test-Path 'HKLM:\SYSTEM\CurrentControlSet\Services\npcap')
    $reason = ''
    if (!$npcap) { $reason = (Get-InstallerMessage 'NpcapMissing') }
    [pscustomobject]@{ NpcapInstalled=$npcap; CaptureReady=$npcap; Error=$reason }
}

function Write-State($State) {
    if (!$StateFile) { return }
    $text = "[State]`r`nNpcapInstalled=$([int][bool]$State.NpcapInstalled)`r`nCaptureReady=$([int][bool]$State.CaptureReady)`r`nError=$($State.Error -replace '[\r\n]+',' ')`r`n"
    [IO.File]::WriteAllText($StateFile,$text,[Text.Encoding]::Unicode)
}

function Assert-OfficialSignature([string]$File,[string[]]$Publishers) {
    $signature = Get-AuthenticodeSignature -LiteralPath $File
    if ($signature.Status -ne 'Valid' -or !$signature.SignerCertificate) { throw (Get-InstallerMessage 'InvalidSignature') }
    $name = $signature.SignerCertificate.GetNameInfo([Security.Cryptography.X509Certificates.X509NameType]::SimpleName,$false)
    if ($Publishers -notcontains $name) { throw (Get-InstallerMessage 'PublisherMismatch' @($name)) }
}

try {
    if ($Action -eq 'VerifyNpcap') { Assert-OfficialSignature $PackagePath @('Insecure.Com LLC','Nmap Software LLC'); exit 0 }
    Write-State (Get-DependencyState)
    exit 0
} catch {
    Write-State ([pscustomobject]@{ NpcapInstalled=$false; CaptureReady=$false; Error=$_.Exception.Message })
    [Console]::Error.WriteLine($_.Exception.Message)
    exit 1
}

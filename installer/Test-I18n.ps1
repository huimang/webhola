$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'I18n.ps1')
$catalogs = @{}
foreach ($locale in @('zh-CN','en')) {
    $catalogs[$locale] = Get-Content -LiteralPath (Join-Path $PSScriptRoot "locales\$locale.json") -Raw -Encoding UTF8 | ConvertFrom-Json
}
foreach ($property in $catalogs['zh-CN'].PSObject.Properties) {
    $translated = $catalogs['en'].PSObject.Properties[$property.Name]
    if (!$translated -or !$translated.Value) { throw "Missing English installer message: $($property.Name)" }
    $sourceArgs = [regex]::Matches($property.Value,'\{\d+\}').Value -join ','
    $targetArgs = [regex]::Matches($translated.Value,'\{\d+\}').Value -join ','
    if ($sourceArgs -ne $targetArgs) { throw "Installer format arguments differ: $($property.Name)" }
}
$Language = 'en'
if ((Get-InstallerMessage 'PublisherMismatch' @('Test Publisher')) -ne 'Installer publisher does not match: Test Publisher') { throw 'Installer parameter formatting failed' }
$Language = 'zh-CN'
if ((Get-InstallerMessage 'NpcapMissing') -ne $catalogs['zh-CN'].NpcapMissing) { throw 'Installer language change failed' }
foreach ($name in @('Dependencies.ps1','JavaDependencies.ps1','I18n.ps1')) {
    $parseErrors=$null; $tokens=$null
    $tree = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot $name),[ref]$tokens,[ref]$parseErrors)
    if ($parseErrors) { throw "PowerShell syntax error: $name" }
    $strings = $tree.FindAll({param($node) $node -is [Management.Automation.Language.StringConstantExpressionAst] -or $node -is [Management.Automation.Language.ExpandableStringExpressionAst]},$true)
    foreach ($text in $strings) {
        if ($text.Value -match '[\p{IsCJKUnifiedIdeographs}]') { throw "Hardcoded Chinese installer text in $name" }
    }
}
$iss = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'Webhola.iss') -Raw -Encoding UTF8
if ($iss -match '[\p{IsCJKUnifiedIdeographs}]') { throw 'Hardcoded Chinese in Inno Setup source' }
Write-Output 'PASS: installer catalogs, formatting, language selection, and hardcoded-text checks.'

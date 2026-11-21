function Get-InstallerMessage {
    param([Parameter(Mandatory=$true)][string]$Key, [object[]]$Arguments = @())
    if (!$script:InstallerCatalog -or $script:InstallerCatalogLanguage -ne $Language) {
        $catalogPath = Join-Path $PSScriptRoot ("locales\{0}.json" -f $Language)
        if (!(Test-Path -LiteralPath $catalogPath)) { $catalogPath = Join-Path $PSScriptRoot ($Language + '.json') }
        $script:InstallerCatalog = Get-Content -LiteralPath $catalogPath -Raw -Encoding UTF8 | ConvertFrom-Json
        $script:InstallerCatalogLanguage = $Language
    }
    $property = $script:InstallerCatalog.PSObject.Properties[$Key]
    if (!$property) { throw "Missing installer message: $Key" }
    if ($Arguments.Count) { return [string]::Format([string]$property.Value, $Arguments) }
    return [string]$property.Value
}

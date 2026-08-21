[CmdletBinding()]
param(
    [string]$Version = $(if ($env:GG_INSTALL_VERSION) { $env:GG_INSTALL_VERSION } else { 'latest' }),
    [string]$Prefix = $(if ($env:GG_INSTALL_PREFIX) { $env:GG_INSTALL_PREFIX } else { Join-Path $HOME '.local\bin' }),
    [string]$Url
)

$ErrorActionPreference = 'Stop'
$repository = if ($env:GG_INSTALL_REPOSITORY) { $env:GG_INSTALL_REPOSITORY } else { 'VedranJanjetovic/gg' }

function Fail([string]$Message) { throw "gg installer: $Message" }
function Assert-NoReparsePoint([string]$Path) {
    $current = [IO.Path]::GetFullPath($Path)
    while ($null -ne $current) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -Force -LiteralPath $current
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                Fail "refusing reparse-point destination component: $current"
            }
        }
        $parent = [IO.Directory]::GetParent($current)
        if ($null -eq $parent -or $parent.FullName -eq $current) { break }
        $current = $parent.FullName
    }
}
function Get-ArchiveUrl([string]$Asset) {
    if ($Url) { return $Url }
    if ($Version -eq 'latest') { return "https://github.com/$repository/releases/latest/download/$Asset" }
    return "https://github.com/$repository/releases/download/gg-v$Version/$Asset"
}

# --- Agent skills ------------------------------------------------------------
# Install gg's agent skills into the user-level Claude Code and Codex
# configuration under a collision-free gg- prefix, plus the raw
# coding-patterns reference at ~/.gg/gg-coding-patterns.md that gg's prompts
# point code-touching phases at. Directories are created even when the agent
# CLIs are not installed yet. Shared files (CLAUDE.md, AGENTS.md,
# instructions.md) are never touched.
function Install-SkillFile([string]$Source, [string]$Target, [string]$Name) {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Target) | Out-Null
    $content = Get-Content -Raw -LiteralPath $Source
    # Rewrite the first frontmatter name field to the gg- identity.
    if ($content.StartsWith("---")) {
        $content = [Text.RegularExpressions.Regex]::Replace(
            $content, '(?m)^name:[ \t]*\S.*$', "name: gg-$Name", 1)
    }
    Set-Content -LiteralPath $Target -Value $content -NoNewline
}

function Install-SkillsFrom([string]$Dir) {
    if (-not (Test-Path -LiteralPath (Join-Path $Dir 'canonical'))) { Fail "skill assets missing at $Dir" }
    $count = 0
    foreach ($skillDir in Get-ChildItem -Directory -LiteralPath (Join-Path $Dir 'canonical')) {
        $name = $skillDir.Name
        $canonical = Join-Path $skillDir.FullName "$name.md"
        if (-not (Test-Path -LiteralPath $canonical)) { continue }
        $claudeSource = Join-Path $Dir "claude\commands\$name.md"
        if (-not (Test-Path -LiteralPath $claudeSource)) { $claudeSource = $canonical }
        $codexSource = Join-Path $Dir "codex\skills\$name\SKILL.md"
        if (-not (Test-Path -LiteralPath $codexSource)) { $codexSource = $canonical }
        Install-SkillFile $claudeSource (Join-Path $HOME ".claude\commands\gg-$name.md") $name
        Install-SkillFile $claudeSource (Join-Path $HOME ".claude\skills\gg-$name\SKILL.md") $name
        Install-SkillFile $codexSource (Join-Path $HOME ".codex\skills\gg-$name\SKILL.md") $name
        $count++
    }
    $patterns = Join-Path $Dir 'core\coding-patterns.md'
    if (Test-Path -LiteralPath $patterns) {
        $wrapped = Join-Path ([IO.Path]::GetTempPath()) ('gg-coding-patterns-' + [Guid]::NewGuid() + '.md')
        $header = "---`nname: gg-coding-patterns`ndescription: Coding patterns — SOLID, encapsulation, dependency injection, design patterns, bounded concurrency, testability`n---`n`n"
        Set-Content -LiteralPath $wrapped -Value ($header + (Get-Content -Raw -LiteralPath $patterns)) -NoNewline
        Install-SkillFile $wrapped (Join-Path $HOME ".claude\commands\gg-coding-patterns.md") 'coding-patterns'
        Install-SkillFile $wrapped (Join-Path $HOME ".claude\skills\gg-coding-patterns\SKILL.md") 'coding-patterns'
        Install-SkillFile $wrapped (Join-Path $HOME ".codex\skills\gg-coding-patterns\SKILL.md") 'coding-patterns'
        Remove-Item -Force -LiteralPath $wrapped -ErrorAction SilentlyContinue
        New-Item -ItemType Directory -Force -Path (Join-Path $HOME '.gg') | Out-Null
        Copy-Item -LiteralPath $patterns -Destination (Join-Path $HOME '.gg\gg-coding-patterns.md') -Force
        $count++
    }
    Write-Output "gg installer: installed $count agent skills (~/.claude/{commands,skills}/gg-*, ~/.codex/skills/gg-*, ~/.gg/gg-coding-patterns.md)"
}

function Install-Skills([string]$TempDir) {
    $local = if ($PSScriptRoot) { Join-Path $PSScriptRoot 'skills' } else { $null }
    if ($local -and (Test-Path -LiteralPath (Join-Path $local 'canonical'))) {
        Install-SkillsFrom $local
        return
    }
    $ref = if ($Version -eq 'latest') { 'refs/heads/main' } else { "refs/tags/gg-v$Version" }
    $snapshotZip = Join-Path $TempDir 'skills-src.zip'
    Invoke-WebRequest -Uri "https://github.com/$repository/archive/$ref.zip" -OutFile $snapshotZip -UseBasicParsing
    $snapshotDir = Join-Path $TempDir 'skills-src'
    Expand-Archive -LiteralPath $snapshotZip -DestinationPath $snapshotDir -Force
    $assets = Get-ChildItem -Directory -LiteralPath $snapshotDir |
        ForEach-Object { Join-Path $_.FullName 'skills' } |
        Where-Object { Test-Path -LiteralPath $_ } |
        Select-Object -First 1
    if (-not $assets) { Fail 'skill assets not found in repository snapshot' }
    Install-SkillsFrom $assets
}

if ($Version -notmatch '^(latest|[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?)$') { Fail "invalid version: $Version" }
if (-not [Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)) { Fail 'Windows is required' }

$architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
$asset = switch ($architecture) {
    'x64' { 'gg-windows-amd64.zip'; break }
    'arm64' { 'gg-windows-arm64.zip'; break }
    default { Fail "unsupported architecture: $architecture (supported: amd64, arm64)" }
}

$Prefix = [IO.Path]::GetFullPath($Prefix)
Assert-NoReparsePoint $Prefix
if (Test-Path -LiteralPath $Prefix -PathType Leaf) { Fail 'prefix is not a directory' }
New-Item -ItemType Directory -Force -Path $Prefix | Out-Null
Assert-NoReparsePoint $Prefix

$destination = Join-Path $Prefix 'gg.exe'
Assert-NoReparsePoint $destination
if ((Test-Path -LiteralPath $destination) -and -not (Get-Item -Force -LiteralPath $destination).PSIsContainer) {
    # A regular existing executable is replaceable; directories and reparse
    # points are rejected by the checks above.
} elseif (Test-Path -LiteralPath $destination) {
    Fail 'destination executable is not a regular file'
}

$downloadUrl = Get-ArchiveUrl $asset
try { $parsedUrl = [Uri]$downloadUrl } catch { Fail 'download URL must use https' }
if ($parsedUrl.Scheme -ne 'https' -or [string]::IsNullOrWhiteSpace($parsedUrl.Host)) { Fail 'download URL must use https' }

$temp = Join-Path ([IO.Path]::GetTempPath()) ('gg-install-' + [Guid]::NewGuid())
$stage = $null
try {
    New-Item -ItemType Directory -Path $temp | Out-Null
    $zipPath = Join-Path $temp $asset
    Invoke-WebRequest -Uri $downloadUrl -OutFile $zipPath -UseBasicParsing
    if ((Get-Item -LiteralPath $zipPath).Length -eq 0) { Fail 'downloaded archive is empty' }

    Add-Type -AssemblyName System.IO.Compression
    $zip = [IO.Compression.ZipFile]::OpenRead($zipPath)
    try {
        $entries = @($zip.Entries)
        if ($entries.Count -ne 1 -or $entries[0].FullName -ne 'gg.exe' -or $entries[0].Name -ne 'gg.exe' -or $entries[0].Length -eq 0) {
            Fail 'archive must contain exactly root entry gg.exe'
        }
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entries[0], (Join-Path $temp 'gg.exe'), $false)
    } finally { $zip.Dispose() }

    $stage = Join-Path $Prefix ('.gg-install-' + [Guid]::NewGuid() + '.exe')
    if (Test-Path -LiteralPath $stage) { Fail 'temporary destination exists' }
    Copy-Item -LiteralPath (Join-Path $temp 'gg.exe') -Destination $stage
    Assert-NoReparsePoint $destination
    if ((Test-Path -LiteralPath $destination) -and (Get-Item -Force -LiteralPath $destination).PSIsContainer) { Fail 'destination executable is not a regular file' }
    Move-Item -LiteralPath $stage -Destination $destination -Force
    $stage = $null
    Write-Output "gg installer: installed $destination"
    try { Install-Skills $temp } catch { Write-Warning "agent skill installation failed: $_" }
    if (-not (($env:Path -split ';') -contains $Prefix)) { Write-Warning "add $Prefix to PATH" }
} finally {
    if ($stage -and (Test-Path -LiteralPath $stage)) { Remove-Item -Force -LiteralPath $stage -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $temp) { Remove-Item -Recurse -Force -LiteralPath $temp -ErrorAction SilentlyContinue }
}

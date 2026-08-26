# PowerShell wrapper around scripts/release-npm.sh (the bash release pipeline).
# Works from any directory; the bash script derives the repo root itself.
#
# Usage:
#   scripts\release-npm.ps1            # publish current package version
#   scripts\release-npm.ps1 0.2.0      # bump to 0.2.0, stamp the binary, publish
#
# Requires Git Bash (installed with Git for Windows) and `npm adduser` once.
$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$sh = Join-Path $repoRoot 'scripts\release-npm.sh'

# Locate a bash to run the script with.
$bash = $null
if (Get-Command bash -ErrorAction SilentlyContinue) {
    $bash = (Get-Command bash).Source
} elseif (Test-Path "$env:ProgramFiles\Git\bin\bash.exe") {
    $bash = "$env:ProgramFiles\Git\bin\bash.exe"
} elseif (Test-Path "${env:ProgramFiles(x86)}\Git\bin\bash.exe") {
    $bash = "${env:ProgramFiles(x86)}\Git\bin\bash.exe"
}
if (-not $bash) {
    Write-Error "Git Bash not found. Install Git for Windows, or run scripts\release-npm.sh from a Git Bash terminal."
}

Write-Host "Running $sh via $bash"
& $bash $sh @args
exit $LASTEXITCODE

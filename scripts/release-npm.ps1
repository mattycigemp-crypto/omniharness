# PowerShell wrapper around scripts/release-npm.sh (the bash release pipeline).
# Works from any directory; the bash script derives the repo root itself.
#
# Usage:
#   scripts\release-npm.ps1            # auto-bump patch, verify, publish
#   scripts\release-npm.ps1 --minor    # auto-bump minor
#   scripts\release-npm.ps1 --major    # auto-bump major
#   scripts\release-npm.ps1 0.2.0      # explicit version
#   scripts\release-npm.ps1 --dry-run  # bump + verify only
#
# Requires Git Bash (installed with Git for Windows), and npm auth for a real
# publish (see the prerequisites in release-npm.sh).
$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$sh = Join-Path $repoRoot 'scripts\release-npm.sh'

# Locate a bash to run the script with. Prefer Git Bash's own bash.exe (it
# accepts Windows paths); fall back to a PATH `bash` only when it is not the
# WSL shim (C:\Windows\System32\bash.exe), which cannot run Windows paths.
$candidates = @()
foreach ($p in @(
    "$env:ProgramFiles\Git\bin\bash.exe",
    "${env:ProgramFiles(x86)}\Git\bin\bash.exe",
    "$env:LOCALAPPDATA\Programs\Git\bin\bash.exe"
)) {
    if (Test-Path $p) { $candidates += $p }
}
if (-not $candidates) {
    $cmd = Get-Command bash -ErrorAction SilentlyContinue
    if ($cmd -and $cmd.Source -notlike 'C:\Windows\System32\*') {
        $candidates += $cmd.Source
    }
}
if (-not $candidates) {
    Write-Error "Git Bash not found. Install Git for Windows, or run scripts\release-npm.sh from a Git Bash terminal."
}
$bash = $candidates[0]

Write-Host "Running $sh via $bash"
& $bash $sh @args
exit $LASTEXITCODE

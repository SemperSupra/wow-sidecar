<#
.SYNOPSIS
    Runs local GitHub Actions workflows using nektos/act without consuming billable cloud minutes.

.DESCRIPTION
    Wraps act.exe to provide offline, local execution of GitHub Actions workflows.
    Ensures that private GitHub Actions spending remains at $0.00.

.PARAMETER Workflow
    Path to the specific workflow file (e.g. .github/workflows/test.yml).

.PARAMETER Job
    Specific job ID to execute.

.PARAMETER DryRun
    If set, validates workflow syntax and step definitions without spinning up containers.

.EXAMPLE
    .\scripts\local-act-check.ps1 -DryRun
    .\scripts\local-act-check.ps1 -Workflow .github/workflows/lint.yml
#>
[CmdletBinding()]
param (
    [string]$Workflow,
    [string]$Job,
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

$actCmd = Get-Command act -ErrorAction SilentlyContinue
if (-not $actCmd) {
    Write-Error "nektos/act is not installed or not in PATH. Install via: winget install nektos.act"
}

$actArgs = @()

if ($DryRun) {
    $actArgs += "-n"
}

if ($Workflow) {
    if (-not (Test-Path $Workflow)) {
        Write-Error "Workflow file not found: $Workflow"
    }
    $actArgs += "-W"
    $actArgs += $Workflow
}

if ($Job) {
    $actArgs += "-j"
    $actArgs += $Job
}

Write-Host "Running local GitHub Actions validation via act (Zero-Budget Mode)..." -ForegroundColor Cyan
& $actCmd.Source @actArgs
exit $LASTEXITCODE

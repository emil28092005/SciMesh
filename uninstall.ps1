# SciMesh uninstaller for Windows: stops the running component, removes its
# binary, and — when asked — deletes its data directory.
#
#   powershell -ExecutionPolicy Bypass -c "irm https://raw.githubusercontent.com/emil28092005/SciMesh/main/uninstall.ps1 | iex"
#
# $env:SCIMESH_COMPONENT selects the component (coordinator | worker | all,
# default all). Data is kept unless $env:SCIMESH_PURGE -eq "1" or the
# -Purge switch is passed (in a pipe there is no interactive prompt).
param(
    [switch]$Purge
)
$ErrorActionPreference = "Stop"

$Component = if ($env:SCIMESH_COMPONENT) { $env:SCIMESH_COMPONENT } else { "all" }
if ($Purge -or $env:SCIMESH_PURGE -eq "1") { $Purge = $true } else { $Purge = $false }

$InstallDir = if ($env:SCIMESH_INSTALL_DIR) {
    $env:SCIMESH_INSTALL_DIR
} else {
    Join-Path $env:LOCALAPPDATA "SciMesh"
}

function Remove-Component {
    param([string]$Name, [string]$Binary)
    Write-Host "Stopping $Name…"
    Get-Process -Name $Name -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    $target = Join-Path $InstallDir $Binary
    if (Test-Path $target) {
        Write-Host "Removing $target…"
        Remove-Item -Force $target
    }
}

function Remove-Data {
    param([string]$Dir, [string]$Label)
    if (-not (Test-Path $Dir)) { return }
    if ($Purge) {
        Remove-Item -Recurse -Force $Dir
        Write-Host "Deleted $Dir"
        return
    }
    # No interactive prompt in a pipe: keep the data by default.
    Write-Host "Keeping $Dir ($Label). Pass -Purge to delete it."
}

switch ($Component) {
    "coordinator" {
        Remove-Component -Name "coordinator" -Binary "coordinator.exe"
        Remove-Data -Dir (Join-Path $HOME ".scimesh") -Label "secrets, databases, artifacts, users"
    }
    "worker" {
        Remove-Component -Name "worker-agent" -Binary "worker-agent.exe"
        Remove-Data -Dir (Join-Path $HOME ".scimesh-worker") -Label "worker config, runtime venv, logs"
    }
    "all" {
        Remove-Component -Name "coordinator" -Binary "coordinator.exe"
        Remove-Component -Name "worker-agent" -Binary "worker-agent.exe"
        Remove-Data -Dir (Join-Path $HOME ".scimesh") -Label "secrets, databases, artifacts, users"
        Remove-Data -Dir (Join-Path $HOME ".scimesh-worker") -Label "worker config, runtime venv, logs"
    }
    default { throw "unknown component: $Component (use 'coordinator', 'worker' or 'all')" }
}

Write-Host ""
Write-Host "SciMesh $Component uninstalled."
Write-Host "Pass -Purge to also delete the data directories."

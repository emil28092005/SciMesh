# SciMesh installer for Windows: downloads the coordinator binary for this
# machine from the latest GitHub release and installs it under %LOCALAPPDATA%.
#
#   powershell -ExecutionPolicy Bypass -c "irm https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.ps1 | iex"
#
# Then run:
#
#   coordinator serve --open
$ErrorActionPreference = "Stop"

$Repo = "emil28092005/SciMesh"
$Version = if ($env:SCIMESH_VERSION) { $env:SCIMESH_VERSION } else { "latest" }
$InstallDir = if ($env:SCIMESH_INSTALL_DIR) {
    $env:SCIMESH_INSTALL_DIR
} else {
    Join-Path $env:LOCALAPPDATA "SciMesh"
}

$Arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

if ($Version -eq "latest") {
    Write-Host "Resolving the latest SciMesh release..."
    $Url = "https://github.com/$Repo/releases/latest/download/coordinator-windows-$Arch.exe"
} else {
    $Url = "https://github.com/$Repo/releases/download/$Version/coordinator-windows-$Arch.exe"
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$Target = Join-Path $InstallDir "coordinator.exe"

Write-Host "Downloading $Url"
Invoke-WebRequest -Uri $Url -OutFile "$Target.tmp"
Move-Item -Force "$Target.tmp" $Target

Write-Host ""
Write-Host "SciMesh installed: $Target"
& $Target --version
Write-Host ""
Write-Host "Start the platform (one command, everything embedded):"
Write-Host "  $Target serve --open"
Write-Host ""
Write-Host "Your data lives in ~\.scimesh. The admin login is printed on first start."

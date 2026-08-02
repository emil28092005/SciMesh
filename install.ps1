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
$Component = if ($env:SCIMESH_COMPONENT) { $env:SCIMESH_COMPONENT } else { "coordinator" }
$Version = if ($env:SCIMESH_VERSION) { $env:SCIMESH_VERSION } else { "latest" }
$InstallDir = if ($env:SCIMESH_INSTALL_DIR) {
    $env:SCIMESH_INSTALL_DIR
} else {
    Join-Path $env:LOCALAPPDATA "SciMesh"
}
# Auto-start the component right after install and open its UI (the control
# room for the coordinator, the local setup wizard for the worker). Set
# SCIMESH_AUTO_START=0 to install only.
$AutoStart = if ($env:SCIMESH_AUTO_START) { $env:SCIMESH_AUTO_START } else { "1" }

switch ($Component) {
    "coordinator" { $Binary = "coordinator" }
    "worker"      { $Binary = "worker-agent" }
    default       { throw "unknown component: $Component (use 'coordinator' or 'worker')" }
}

$Arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

if ($Version -eq "latest") {
    Write-Host "Resolving the newest SciMesh release (including pre-releases)..."
    # /releases/latest only sees stable releases; the API list is newest-first
    # across all channels.
    try {
        $Releases = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases?per_page=1"
        if ($Releases -and $Releases[0].tag_name) {
            $Version = $Releases[0].tag_name
            Write-Host "  -> $Version"
        } else {
            Write-Host "  -> falling back to the stable latest release"
        }
    } catch {
        Write-Host "  -> falling back to the stable latest release"
    }
}

$Url = "https://github.com/$Repo/releases/download/$Version/$Binary-windows-$Arch.exe"

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$Target = Join-Path $InstallDir "$Binary.exe"

Write-Host "Downloading $Url"
Invoke-WebRequest -Uri $Url -OutFile "$Target.tmp"
Move-Item -Force "$Target.tmp" $Target

Write-Host ""
Write-Host "SciMesh $Component installed: $Target"
& $Target --version
Write-Host ""
if ($Component -eq "coordinator") {
    if ($AutoStart -eq "1") {
        Write-Host "Starting the platform and opening the admin console in your browser..."
        Write-Host "(stop it with Ctrl-C; it keeps your data in ~\.scimesh)"
        Write-Host ""
        & $Target serve --open
    } else {
        Write-Host "Start the platform (one command, everything embedded):"
        Write-Host "  $Target serve --open"
        Write-Host ""
        Write-Host "Your data lives in ~\.scimesh. The admin login is printed on first start."
    }
} else {
    if ($AutoStart -eq "1") {
        Write-Host "Starting the local setup wizard in your browser..."
        Write-Host "(stop it with Ctrl-C; it keeps the configuration in ~\.scimesh-worker)"
        Write-Host ""
        & $Target setup
    } else {
        Write-Host "The worker needs Python 3 with the scimesh package, then a coordinator"
        Write-Host "to connect to. Point the local wizard at it:"
        Write-Host ""
        Write-Host "  $Target setup"
        Write-Host ""
        Write-Host "Or run it with environment variables:"
        Write-Host ""
        Write-Host "  set COORDINATOR_URL=http://COORDINATOR_HOST:8080"
        Write-Host "  set WORKER_AUTH_TOKEN=<worker token from the coordinator>"
        Write-Host "  set WORK_DIR=%USERPROFILE%\scimesh-worker"
        Write-Host "  $Target"
        Write-Host ""
        Write-Host "For a coordinator started with 'coordinator serve', the worker token is"
        Write-Host "in ~\.scimesh\worker.token on that machine. Set SCIMESH_PIP_PACKAGE to"
        Write-Host "install scimesh into a managed venv, or install it yourself:"
        Write-Host "  pip install scimesh"
    }
}

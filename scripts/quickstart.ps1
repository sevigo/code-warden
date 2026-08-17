# scripts/quickstart.ps1 — Code-Warden guided setup wizard (Windows/PowerShell)
#
# Idempotent: safe to run multiple times.
# Run via:  make quickstart     (Windows auto-detects and calls this script)
#       or:  pwsh ./scripts/quickstart.ps1

[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$RepoRoot   = Split-Path -Parent $PSScriptRoot
$HealthURL  = 'http://localhost:8080/health'
$Compose    = 'docker-compose.demo.yml'

# ── Helpers ──────────────────────────────────────────────────────────────────

function Info($msg)    { Write-Host "  > $msg" -ForegroundColor Cyan }
function Ok($msg)       { Write-Host "  ✓ $msg" -ForegroundColor Green }
function Warn($msg)     { Write-Host "  ! $msg" -ForegroundColor Yellow }
function Err($msg)      { Write-Host "  ✗ $msg" -ForegroundColor Red }
function Heading($msg)  { Write-Host "`n$msg" -ForegroundColor White }

function Check-Dep($name) {
    if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
        Err "Required tool not found: $name"
        Write-Host "  Install it and re-run this script."
        exit 1
    }
    Ok "$name found"
}

# ── Banner ───────────────────────────────────────────────────────────────────

Heading 'Code-Warden Quickstart'
Write-Host '  Full server mode: PostgreSQL + Qdrant + Ollama + Code-Warden'
Write-Host '  Web UI will be available at http://localhost:8080'
Write-Host ''

# ── Prerequisites ────────────────────────────────────────────────────────────

Heading '1. Checking prerequisites...'
Check-Dep docker
Check-Dep git

# docker compose v2 (shipped with Docker Desktop) or docker-compose v1
$ComposeCmd = $null
if ((Get-Command docker -ErrorAction SilentlyContinue) -and
    (& docker compose version) 2>$null) {
    $ComposeCmd = 'docker compose'
} elseif (Get-Command docker-compose -ErrorAction SilentlyContinue) {
    $ComposeCmd = 'docker-compose'
} else {
    Err "Neither 'docker compose' (v2) nor 'docker-compose' (v1) found."
    Write-Host '  Install Docker Desktop and re-run this script.'
    exit 1
}
Ok "Docker Compose found ($ComposeCmd)"

# ── .env setup ───────────────────────────────────────────────────────────────

Heading '2. Environment configuration...'

Set-Location $RepoRoot

if (-not (Test-Path .env)) {
    Copy-Item .env.example .env
    Info 'Created .env from .env.example'
} else {
    Ok '.env already exists'
}

# NOTE: GitHub and LLM credentials are now optional in .env — the setup wizard
# at /setup will handle them after the server is up. We no longer prompt for a
# GitHub PAT here.
Info 'GitHub and LLM credentials will be configured via the setup wizard at http://localhost:8080/setup'

# ── GPU detection ────────────────────────────────────────────────────────────

Heading '3. GPU detection...'

$GpuOverride = @()
$nvidia = Get-Command nvidia-smi -ErrorAction SilentlyContinue
if ($nvidia -and (& nvidia-smi) 2>$null) {
    Ok 'NVIDIA GPU detected'
    $GpuOverride = @('-f', 'docker-compose.gpu.yml')
    Info 'Using NVIDIA GPU compose override for Ollama'
} else {
    Info 'No NVIDIA GPU detected — using CPU mode (Ollama runs on CPU)'
    Info 'Apple Silicon: Metal acceleration works automatically via the Ollama image'
}

# ── Build & start ────────────────────────────────────────────────────────────

Heading '4. Starting services...'

Info 'Building Code-Warden image and starting all containers...'
Info 'First run pulls local Ollama models (~1.6 GB): embedder + fast model.'
Info 'Generator (kimi-k2.5) is a cloud model — no local download needed.'
Write-Host ''

# Build the compose command and invoke it
$composeArgs = @('-f', $Compose) + $GpuOverride + @('up', '-d', '--build')
if ($ComposeCmd -eq 'docker compose') {
    & docker @composeArgs
} else {
    & docker-compose @composeArgs
}
if ($LASTEXITCODE -ne 0) { throw "docker compose up failed (exit $LASTEXITCODE)" }

Ok 'All containers started'

# ── Wait for health ──────────────────────────────────────────────────────────

Heading '5. Waiting for server to be ready...'

$MaxWait  = 180  # seconds
$Elapsed  = 0
$Interval = 5

Write-Host "  Polling $HealthURL" -NoNewline

$ready = $false
while (-not $ready) {
    try {
        $resp = Invoke-WebRequest -Uri $HealthURL -UseBasicParsing -TimeoutSec 5 -ErrorAction Stop
        if ($resp.StatusCode -eq 200) { $ready = $true; break }
    } catch {
        if ($Elapsed -ge $MaxWait) { break }
    }
    Write-Host '.' -NoNewline
    Start-Sleep -Seconds $Interval
    $Elapsed += $Interval
}

if (-not $ready) {
    Write-Host ''
    Err "Server did not become healthy within ${MaxWait}s"
    Write-Host ''
    Write-Host "  Check logs with:  $ComposeCmd -f $Compose logs server"
    Write-Host '  Common causes:'
    Write-Host '    - Ollama model pull still in progress (ollama-init service)'
    Write-Host '    - Not enough disk space for models (~5.3 GB needed)'
    Write-Host '    - Port 8080 already in use on the host'
    exit 1
}

Write-Host ''
Ok 'Server is healthy'

# ── Done ─────────────────────────────────────────────────────────────────────

Heading '6. Setup complete!'
Write-Host ''
Write-Host '  Code-Warden is running at http://localhost:8080' -ForegroundColor Green
Write-Host ''
Write-Host '  Next steps:'
Write-Host '    * Open http://localhost:8080/setup to configure your GitHub App via the wizard'
Write-Host ''
Write-Host '  Useful commands:'
Write-Host '    make demo-logs    -- tail server logs'
Write-Host '    make demo-down    -- stop all services'
Write-Host '    make demo-up      -- restart services'
Write-Host ''
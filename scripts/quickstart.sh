#!/usr/bin/env bash
# scripts/quickstart.sh — Code-Warden guided setup wizard
#
# Idempotent: safe to run multiple times.
# Run via: make quickstart   or   bash scripts/quickstart.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HEALTH_URL="http://localhost:8080/health"
COMPOSE_FILE="docker-compose.demo.yml"

# ── Colours ──────────────────────────────────────────────────────────────────

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
RED='\033[0;31m'
BOLD='\033[1m'
NC='\033[0m' # No Colour

info()    { echo -e "${CYAN}  >${NC} $*"; }
ok()      { echo -e "${GREEN}  ✓${NC} $*"; }
warn()    { echo -e "${YELLOW}  !${NC} $*"; }
err()     { echo -e "${RED}  ✗${NC} $*" >&2; }
heading() { echo -e "\n${BOLD}$*${NC}"; }

# ── Prerequisites ─────────────────────────────────────────────────────────────

heading "Code-Warden Quickstart"
echo "  Full server mode: PostgreSQL + Qdrant + Ollama + Code-Warden"
echo "  Web UI will be available at http://localhost:8080"
echo ""

check_dep() {
    if ! command -v "$1" &>/dev/null; then
        err "Required tool not found: $1"
        echo "  Install it and re-run this script."
        exit 1
    fi
    ok "$1 found"
}

heading "1. Checking prerequisites..."
check_dep docker
check_dep git

# docker compose v2 or docker-compose v1
if docker compose version &>/dev/null 2>&1; then
    COMPOSE_CMD="docker compose"
elif command -v docker-compose &>/dev/null; then
    COMPOSE_CMD="docker-compose"
else
    err "Neither 'docker compose' (v2) nor 'docker-compose' (v1) found."
    echo "  Install Docker Desktop or docker-compose and re-run."
    exit 1
fi
ok "Docker Compose found ($COMPOSE_CMD)"

# ── .env setup ────────────────────────────────────────────────────────────────

heading "2. Environment configuration..."

cd "$REPO_ROOT"

if [ ! -f .env ]; then
    cp .env.example .env
    info "Created .env from .env.example"
fi

# Check if GITHUB_TOKEN is still the placeholder
if grep -q 'GITHUB_TOKEN=ghp_replace_me' .env 2>/dev/null; then
    warn "GITHUB_TOKEN is not set in .env"
    echo ""
    echo "  You need a GitHub Personal Access Token for the server to authenticate."
    echo "  Create one at: https://github.com/settings/tokens"
    echo "  Required scopes: repo (or repo:read for private repos)"
    echo ""
    printf "  Paste your GitHub PAT (or press Enter to skip): "
    read -r pat
    if [ -n "$pat" ]; then
        # Replace the placeholder value
        sed -i.bak "s|GITHUB_TOKEN=ghp_replace_me|GITHUB_TOKEN=$pat|" .env
        rm -f .env.bak
        ok "GitHub PAT saved to .env"
    else
        warn "Skipped — set GITHUB_TOKEN in .env before connecting to GitHub"
    fi
else
    ok "GITHUB_TOKEN is configured"
fi

# ── GPU detection ─────────────────────────────────────────────────────────────

heading "3. GPU detection..."

GPU_OVERRIDE=""

if command -v nvidia-smi &>/dev/null && nvidia-smi &>/dev/null 2>&1; then
    ok "NVIDIA GPU detected"
    GPU_OVERRIDE="-f docker-compose.gpu.yml"
    info "Using NVIDIA GPU compose override for Ollama"
elif [ -e /dev/kfd ] && [ -e /dev/dri ]; then
    ok "AMD ROCm GPU detected (/dev/kfd + /dev/dri)"
    GPU_OVERRIDE="-f docker-compose.amd.yml"
    info "Using AMD ROCm compose override for Ollama"
    info "Note: HSA_OVERRIDE_GFX_VERSION=10.3.0 is set for RX 6000 series"
    info "Remove that env var from docker-compose.amd.yml for RX 7000/MI-series"
else
    info "No GPU detected — using CPU mode (Ollama runs on CPU)"
    info "Apple Silicon: Metal acceleration works automatically via the Ollama image"
fi

# ── Build & start ─────────────────────────────────────────────────────────────

heading "4. Starting services..."

info "Building Code-Warden image and starting all containers..."
info "First run pulls local Ollama models (~1.6 GB): embedder + fast model."
info "Generator (kimi-k2.5) is a cloud model — no local download needed."
echo ""

# shellcheck disable=SC2086
$COMPOSE_CMD -f $COMPOSE_FILE $GPU_OVERRIDE up -d --build

ok "All containers started"

# ── Wait for health ───────────────────────────────────────────────────────────

heading "5. Waiting for server to be ready..."

MAX_WAIT=180  # seconds
ELAPSED=0
INTERVAL=5

printf "  Polling %s" "$HEALTH_URL"

until curl -sf "$HEALTH_URL" &>/dev/null; do
    if [ "$ELAPSED" -ge "$MAX_WAIT" ]; then
        echo ""
        err "Server did not become healthy within ${MAX_WAIT}s"
        echo ""
        echo "  Check logs with:  $COMPOSE_CMD -f $COMPOSE_FILE logs server"
        echo "  Common causes:"
        echo "    - Ollama model pull still in progress (ollama-init service)"
        echo "    - Not enough disk space for models (~5.3 GB needed)"
        echo "    - Port 8080 already in use on the host"
        exit 1
    fi
    printf "."
    sleep "$INTERVAL"
    ELAPSED=$((ELAPSED + INTERVAL))
done

echo ""
ok "Server is healthy"

# ── Done ──────────────────────────────────────────────────────────────────────

heading "6. Setup complete!"
echo ""
echo -e "  ${GREEN}${BOLD}Code-Warden is running at http://localhost:8080${NC}"
echo ""
echo "  Next steps:"
echo "    • Open http://localhost:8080/setup to configure your GitHub App"
echo "    • Or run a quick CLI review (no GitHub App needed):"
echo ""
echo -e "      ${CYAN}make demo PR=https://github.com/owner/repo/pull/123${NC}"
echo ""
echo "  Useful commands:"
echo "    make demo-logs    — tail server logs"
echo "    make demo-down    — stop all services"
echo "    make demo-up      — restart services"
echo ""

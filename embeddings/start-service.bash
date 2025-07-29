#!/bin/bash

# --- Configuration ---
SERVICE_DIR="/workspace/code-warden/embeddings"

echo "--- Code-Warden Auto-Start Script ---"
echo "Service directory: $SERVICE_DIR"

# Navigate to the service directory
cd $SERVICE_DIR

echo "Environment variables set."
echo "HF_HOME is set to: $HF_HOME"

if [ -d "venv" ]; then
    echo "Activating Python virtual environment..."
    source venv/bin/activate
else
    echo "ERROR: Virtual environment 'venv' not found. Please set it up first."
    exit 1
fi

# --- Start the Uvicorn Server ---
echo "Starting Uvicorn server on host 0.0.0.0, port 18000..."
exec uvicorn main:app --host 0.0.0.0 --port 18000
#!/bin/bash
# VividGo background launcher for Linux/macOS/Unix-like systems.
# This script intentionally starts the bot in the background so it keeps running
# even after the terminal is closed or the session ends.
# Use this instead of running the binary directly when you want a detached service.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY="$SCRIPT_DIR/vividgo"
LOG_FILE="$SCRIPT_DIR/vividgo.log"

if [ ! -f "$BINARY" ]; then
  echo "Error: $BINARY not found in $SCRIPT_DIR"
  exit 1
fi

if [ ! -x "$BINARY" ]; then
  chmod +x "$BINARY"
fi

echo "Starting VividGo in the background..."
nohup "$BINARY" >>"$LOG_FILE" 2>&1 &
PID=$!

echo "VividGo started in the background."
echo "PID: $PID"
echo "Log file: $LOG_FILE"

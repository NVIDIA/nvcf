#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# NVSNAP Entrypoint
#
# Handles three modes:
# 1. Agent: If first arg starts with --, run nvsnap-agent
# 2. Restore: If NVSNAP_RESTORE_FROM is set, restore from checkpoint first
# 3. Normal: Run the provided command
#
# Environment variables:
#   NVSNAP_RESTORE_FROM - Path to checkpoint directory (triggers restore mode)
#   NVSNAP_CHECKPOINT_DIR - Where to save checkpoints (default: /checkpoints)

set -e

LOG_PREFIX="[NVSNAP]"
log() { echo "$LOG_PREFIX $1"; }

# Deploy CRIU tools to host for restore pods to use
NVSNAP_TOOLS_DIR="${NVSNAP_TOOLS_DIR:-/usr/local/nvsnap}"
if [ -d "$NVSNAP_TOOLS_DIR" ] && [ -w "$NVSNAP_TOOLS_DIR" ]; then
    log "Deploying NVSNAP tools to $NVSNAP_TOOLS_DIR"
    
    # Copy CRIU binary and wrapper
    cp -f /usr/local/bin/criu "$NVSNAP_TOOLS_DIR/" 2>/dev/null || true
    
    # Create wrapper that uses bundled libs
    cat > "$NVSNAP_TOOLS_DIR/criu-wrapper" << 'WRAPPER'
#!/bin/bash
BUNDLE_DIR="$(dirname "$(readlink -f "$0")")"
export LD_LIBRARY_PATH="$BUNDLE_DIR/lib:$LD_LIBRARY_PATH"
exec "$BUNDLE_DIR/criu" --libdir "$BUNDLE_DIR/plugins" "$@"
WRAPPER
    chmod +x "$NVSNAP_TOOLS_DIR/criu-wrapper"
    
    # Copy plugins
    mkdir -p "$NVSNAP_TOOLS_DIR/plugins"
    cp -f /app/plugins/* "$NVSNAP_TOOLS_DIR/plugins/" 2>/dev/null || true
    
    # Copy cuda-checkpoint 
    cp -f /app/bin/cuda-checkpoint "$NVSNAP_TOOLS_DIR/" 2>/dev/null || true
    cp -f /usr/local/bin/cuda-checkpoint "$NVSNAP_TOOLS_DIR/" 2>/dev/null || true
    
    # Copy restore-entrypoint
    cp -f /app/bin/restore-entrypoint "$NVSNAP_TOOLS_DIR/" 2>/dev/null || true
    
    # Copy required libraries from the container (not the old bundle)
    mkdir -p "$NVSNAP_TOOLS_DIR/lib"
    for lib in /lib/x86_64-linux-gnu/libc.so.6 \
               /lib/x86_64-linux-gnu/libpthread.so.0 \
               /lib/x86_64-linux-gnu/libdl.so.2 \
               /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 \
               /lib/x86_64-linux-gnu/libprotobuf-c.so.1 \
               /lib/x86_64-linux-gnu/libnl-3.so.200 \
               /lib/x86_64-linux-gnu/libnet.so.1; do
        [ -f "$lib" ] && cp -f "$lib" "$NVSNAP_TOOLS_DIR/lib/" 2>/dev/null || true
    done
    
    log "NVSNAP tools deployed successfully"
    ls -la "$NVSNAP_TOOLS_DIR/" || true
fi

# Check for restore mode
if [ -n "$NVSNAP_RESTORE_FROM" ]; then
    log "Restore mode: restoring from $NVSNAP_RESTORE_FROM"
    exec /app/restore.sh "$NVSNAP_RESTORE_FROM"
fi

# Check for agent mode (args starting with --)
if [ $# -gt 0 ] && [[ "$1" == --* ]]; then
    log "Agent mode: running nvsnap-agent $@"
    exec /app/bin/nvsnap-agent "$@"
fi

# Normal mode - just run the command (default to gpu_loop if no args)
if [ $# -eq 0 ]; then
    log "Normal mode: running /app/gpu_loop"
    exec /app/gpu_loop
else
    log "Normal mode: running $@"
    exec "$@"
fi

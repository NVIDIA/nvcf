#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Test CRIU streaming via RPC mode locally (no K8s, no GPU)
# This verifies that go-criu sends Stream=true and CRIU receives it.
#
# Usage: sudo ./scripts/test-stream-rpc-local.sh

set -euo pipefail

CRIU="${CRIU:?CRIU must be set: path to a built criu binary, e.g. ../criu/criu/criu}"
STREAMER="${STREAMER:?STREAMER must be set: path to criu-image-streamer binary, e.g. ../criu-image-streamer/target/release/criu-image-streamer}"
LZ4="${LZ4:-$(which lz4 2>/dev/null || echo /usr/bin/lz4)}"
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

log_ok()   { echo -e "${GREEN}[OK]${NC} $*"; }
log_fail() { echo -e "${RED}[FAIL]${NC} $*"; }
log_info() { echo -e "${CYAN}[INFO]${NC} $*"; }

if [ "$(id -u)" -ne 0 ]; then
    echo "Requires root. Run with sudo."
    exit 1
fi

WORK_DIR=$(mktemp -d /tmp/criu-rpc-stream-test-XXXXXX)
DUMP_DIR="$WORK_DIR/dump"
mkdir -p "$DUMP_DIR"

cleanup() {
    pkill -f "sleep 99887" 2>/dev/null || true
    rm -rf "$WORK_DIR"
}
trap cleanup EXIT

log_info "=== CRIU RPC Streaming Test ==="
log_info "Work dir: $WORK_DIR"

# Start test process
sleep 99887 &
TEST_PID=$!
log_ok "Test process PID: $TEST_PID"

# Build a small Go test program that uses go-criu with Stream=true
cat > "$WORK_DIR/test_rpc_stream.go" << 'GOEOF'
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	criu "github.com/balajinvda/go-criu/v8"
	rpc "github.com/balajinvda/go-criu/v8/rpc"
	"google.golang.org/protobuf/proto"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintf(os.Stderr, "Usage: %s <criu-path> <images-dir> <pid> <streamer-bin> <lz4-bin>\n", os.Args[0])
		os.Exit(1)
	}
	criuPath := os.Args[1]
	imagesDir := os.Args[2]
	pid, _ := strconv.Atoi(os.Args[3])
	streamerBin := os.Args[4]
	lz4Bin := os.Args[5]

	streamFile := filepath.Join(imagesDir, "stream.lz4")

	// Start streamer pipeline
	outFile, _ := os.Create(streamFile)
	streamerCmd := exec.Command(streamerBin, "--images-dir", imagesDir, "capture")
	lz4Cmd := exec.Command(lz4Bin, "-1", "-")
	pipe, _ := streamerCmd.StdoutPipe()
	lz4Cmd.Stdin = pipe
	lz4Cmd.Stdout = outFile
	lz4Cmd.Stderr = os.Stderr
	streamerCmd.Stderr = os.Stderr
	streamerCmd.Start()
	lz4Cmd.Start()

	// Wait for socket
	socketPath := filepath.Join(imagesDir, "streamer-capture.sock")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("Streamer socket ready: %s\n", socketPath)

	// Open images dir fd
	imageDir, err := os.Open(imagesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open images dir: %v\n", err)
		os.Exit(1)
	}

	// Build CRIU opts with Stream=true
	cgMode := rpc.CriuCgMode_IGNORE
	opts := &rpc.CriuOpts{
		Pid:               proto.Int32(int32(pid)),
		ImagesDirFd:       proto.Int32(int32(imageDir.Fd())),
		LogLevel:          proto.Int32(4),
		LogFile:           proto.String("dump.log"),
		ShellJob:          proto.Bool(true),
		ManageCgroups:     proto.Bool(true),
		ManageCgroupsMode: &cgMode,
		Stream:            proto.Bool(true),
	}

	fmt.Printf("CriuOpts.Stream = %v\n", opts.GetStream())

	c := criu.MakeCriu()
	c.SetCriuPath(criuPath)

	err = c.Dump(opts, nil)

	// Wait for pipeline
	streamerCmd.Wait()
	lz4Cmd.Wait()
	outFile.Close()

	if err != nil {
		fmt.Fprintf(os.Stderr, "CRIU dump error: %v\n", err)
		// Check debug log
		if data, err := os.ReadFile("/tmp/criu-stream-debug.log"); err == nil {
			fmt.Printf("=== /tmp/criu-stream-debug.log ===\n%s\n", string(data))
		}
		os.Exit(1)
	}

	// Check results
	if fi, err := os.Stat(streamFile); err == nil {
		fmt.Printf("stream.lz4 size: %d bytes\n", fi.Size())
		if fi.Size() > 0 {
			fmt.Println("SUCCESS: Streaming RPC dump worked!")
		} else {
			fmt.Println("FAIL: stream.lz4 is empty")
			os.Exit(1)
		}
	}

	// Check debug log
	if data, err := os.ReadFile("/tmp/criu-stream-debug.log"); err == nil {
		fmt.Printf("=== /tmp/criu-stream-debug.log ===\n%s\n", string(data))
	} else {
		fmt.Printf("No debug log at /tmp/criu-stream-debug.log: %v\n", err)
	}
}
GOEOF

log_info "Building RPC stream test binary..."
cd "$PROJECT_ROOT"
go build -o "$WORK_DIR/test_rpc_stream" "$WORK_DIR/test_rpc_stream.go"
log_ok "Built test binary"

# Clear any previous debug log
rm -f /tmp/criu-stream-debug.log

log_info "Running RPC dump with Stream=true..."
"$WORK_DIR/test_rpc_stream" "$CRIU" "$DUMP_DIR" "$TEST_PID" "$STREAMER" "$LZ4"

log_ok "=== TEST COMPLETE ==="

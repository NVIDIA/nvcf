/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// restore-entrypoint is a minimal binary that runs inside a placeholder container
// and performs CRIU restore using go-criu library (not CLI).
//
// Using go-criu instead of CLI CRIU avoids the slow ptrace-based thread detachment
// that hangs with multi-threaded applications (900+ threads).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	criu "github.com/balajinvda/go-criu/v8"
	criurpc "github.com/balajinvda/go-criu/v8/rpc"
	"github.com/vishvananda/netlink"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/streamer"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

const (
	defaultBundlePath     = "/nvsnap"
	defaultCheckpointBase = "/checkpoints"

	// criuRestoreWorkDir is the writable scratch directory CRIU's
	// WorkDirFd points to during restore. With phase 5b's per-capture
	// PVC mounted RO at /checkpoints, CRIU cannot write its
	// restore.log or restore-side IPC sockets into the images dir;
	// pointing WorkDir at /tmp tmpfs lets the dump images stay RO
	// while CRIU still has a writable scratch space. All readers of
	// restore.log (failure-path log dump, sigaction PID parser, etc.)
	// must reference this path, not the images dir.
	criuRestoreWorkDir = "/tmp/nvsnap-criu-work"
)

func ensureCriuOnPath(criuBinary string) {
	if _, err := exec.LookPath("criu"); err == nil {
		return
	}
	targets := []string{
		"/usr/local/sbin/criu",
		"/usr/sbin/criu",
		"/usr/local/bin/criu",
		"/usr/bin/criu",
	}
	for _, target := range targets {
		if _, err := os.Stat(target); err == nil {
			fmt.Printf("Found existing CRIU at %s\n", target)
			return
		}
		err := os.Symlink(criuBinary, target)
		if err == nil {
			fmt.Printf("Symlinked CRIU to %s\n", target)
			return
		}
		fmt.Printf("Warning: could not symlink CRIU to %s: %v\n", target, err)
	}
	fmt.Println("Warning: CRIU not found in PATH and no symlink could be created")
}

func probeCriuBinary(path string) {
	if path == "" {
		return
	}
	cmd := exec.Command(path, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("CRIU probe failed (%s): %v\n%s\n", path, err, string(output))
		return
	}
	fmt.Printf("CRIU probe ok (%s): %s\n", path, strings.TrimSpace(string(output)))
}

// Enhanced metadata types (must match internal/agent/checkpoint.go)

// SkippedResources tracks resources that were intentionally skipped during dump
type SkippedResources struct {
	UnixSocketFds  []int    `json:"unixSocketFds,omitempty"`
	InetSocketFds  []int    `json:"inetSocketFds,omitempty"`
	IoUringThreads []string `json:"ioUringThreads,omitempty"`
	Mounts         []string `json:"mounts,omitempty"`
}

// MappedFileInfo tracks a file that was copied during checkpoint
type MappedFileInfo struct {
	SourcePath string `json:"sourcePath"`
	DestPath   string `json:"destPath"`
}

// CUDACheckpointInfo tracks CUDA/GPU checkpoint state
type CUDACheckpointInfo struct {
	Enabled           bool `json:"enabled"`
	GPUPID            int  `json:"gpuPid,omitempty"`
	LockSuccess       bool `json:"lockSuccess"`
	CheckpointSuccess bool `json:"checkpointSuccess"`
	RestoreSuccess    bool `json:"restoreSuccess,omitempty"`
	UnlockSuccess     bool `json:"unlockSuccess,omitempty"`
	Interposition     bool `json:"interposition,omitempty"` // GPU memory saved by intercept library (no cuda-checkpoint)
}

// CRIUOptionsUsed tracks which CRIU options were used during dump
type CRIUOptionsUsed struct {
	External        []string `json:"external,omitempty"`
	SkipUnixSockets bool     `json:"skipUnixSockets"`
	SkipInFlight    bool     `json:"skipInFlight"`
	SkipFsnotify    bool     `json:"skipFsnotify"`
	LeaveRunning    bool     `json:"leaveRunning"`
	TCPEstablished  bool     `json:"tcpEstablished"`
}

// RestoreHints provides guidance to the restore process
type RestoreHints struct {
	SkipFds            []int  `json:"skipFds,omitempty"`
	RestoreMappedFiles bool   `json:"restoreMappedFiles"`
	CUDARestoreNeeded  bool   `json:"cudaRestoreNeeded"`
	NetworkMode        string `json:"networkMode,omitempty"`
}

// CheckpointMetadata contains all information about a checkpoint
type CheckpointMetadata struct {
	Version        string              `json:"version"`
	ID             string              `json:"id"`
	CreatedAt      string              `json:"createdAt"`
	PodName        string              `json:"podName"`
	PodNamespace   string              `json:"podNamespace"`
	NodeName       string              `json:"nodeName"`
	ContainerName  string              `json:"containerName"`
	ContainerID    string              `json:"containerID"`
	ContainerImage string              `json:"containerImage"`
	ContainerPID   uint32              `json:"containerPid"`
	RootFS         string              `json:"rootfs"`
	PodLabels      map[string]string   `json:"podLabels,omitempty"`
	Skipped        *SkippedResources   `json:"skipped,omitempty"`
	MappedFiles    []MappedFileInfo    `json:"mappedFiles,omitempty"`
	OpenFiles      []MappedFileInfo    `json:"openFiles,omitempty"` // v1.4+: open FD files (logs, etc.)
	CUDA           *CUDACheckpointInfo `json:"cuda,omitempty"`
	CRIUOptions    *CRIUOptionsUsed    `json:"criuOptions,omitempty"`
	Hints          *RestoreHints       `json:"restoreHints,omitempty"`
	// Plan A: explicit dump-time mountpoints (for ExtMnt consistency)
	DumpMountPoints []string `json:"dumpMountPoints,omitempty"`
	// Network identity for restore compatibility
	SourcePodIP string `json:"source_pod_ip,omitempty"`
	// Pipe IDs for stdout/stderr (v1.3+) - needed for InheritFd restore
	StdoutPipeID string `json:"stdout_pipe_id,omitempty"` // e.g., "pipe:[12345]"
	StderrPipeID string `json:"stderr_pipe_id,omitempty"` // e.g., "pipe:[12346]"
	// Compression info (v1.5+)
	Compression *CompressionMeta `json:"compression,omitempty"`
	// Integrity (v1.6+)
	Integrity *CheckpointIntegrity `json:"integrity,omitempty"`
	// Deprecated
	GPUPID int `json:"gpuPid,omitempty"`
}

// CheckpointIntegrity holds SHA-256 checksums for checkpoint files.
type CheckpointIntegrity struct {
	Algorithm  string            `json:"algorithm"`
	FileHashes map[string]string `json:"fileHashes"`
	TotalHash  string            `json:"totalHash"`
}

// CompressionMeta mirrors streamer.CompressionInfo for metadata deserialization.
type CompressionMeta struct {
	Algorithm           string                    `json:"algorithm"`
	Level               int                       `json:"level"`
	Files               map[string]CompressedFile `json:"files"`
	TotalOriginalSize   int64                     `json:"totalOriginalSize"`
	TotalCompressedSize int64                     `json:"totalCompressedSize"`
	Ratio               float64                   `json:"ratio"`
}

// CompressedFile records sizes for a single compressed file.
type CompressedFile struct {
	OriginalSize   int64 `json:"originalSize"`
	CompressedSize int64 `json:"compressedSize"`
}

// setupLoopbackAlias adds the original pod IP to the loopback interface
// to allow restored processes to bind to their original IP.
// This is safe because:
// - lo doesn't ARP, so no cluster IP conflicts
// - Traffic stays local within the netns
// - We add policy routing rules to prevent source IP leakage
//
// Uses netlink directly instead of `ip`/`iptables` commands to avoid dependency on external tools.
func setupLoopbackAlias(originalPodIP string) error {
	if originalPodIP == "" {
		return nil
	}

	fmt.Printf("=== Setting up loopback alias for stable network identity ===\n")
	fmt.Printf("Original pod IP: %s\n", originalPodIP)

	// Get loopback interface using netlink
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("failed to get loopback interface: %w", err)
	}

	// Parse the IP address
	ip := net.ParseIP(originalPodIP)
	if ip == nil {
		return fmt.Errorf("failed to parse IP address: %s", originalPodIP)
	}

	// Determine prefix length based on IP version (IPv4=/32, IPv6=/128)
	prefixLen := 32
	ipBits := 32
	if ip.To4() == nil {
		// IPv6 address
		prefixLen = 128
		ipBits = 128
	}

	// Create address with appropriate prefix
	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   ip,
			Mask: net.CIDRMask(prefixLen, ipBits),
		},
	}

	// Add the address to loopback
	if err := netlink.AddrAdd(lo, addr); err != nil {
		// Ignore "file exists" error (address already added)
		if !strings.Contains(err.Error(), "file exists") {
			return fmt.Errorf("failed to add address to loopback: %w", err)
		}
		fmt.Printf("Loopback alias already exists, continuing\n")
	} else {
		fmt.Printf("Added %s/%d to lo via netlink\n", originalPodIP, prefixLen)
	}

	// Add egress guardrails using policy routing via netlink
	// This prevents the old IP from being used as source for outbound non-local traffic
	if err := setupEgressGuardrails(ip); err != nil {
		fmt.Printf("Warning: Failed to set up egress guardrails: %v\n", err)
		fmt.Println("Continuing without egress protection - outbound connections may fail")
	}

	fmt.Printf("WARNING: Connections to %s inside this pod will now loop back locally\n", originalPodIP)
	fmt.Printf("=== Loopback alias setup complete ===\n")

	return nil
}

// setupEgressGuardrails uses policy routing to prevent source IP leakage.
// Equivalent to:
//
//	ip rule add from <oldIP> to <oldIP> lookup main priority 100
//	ip rule add from <oldIP> to 127.0.0.0/8 lookup main priority 101
//	ip route add blackhole default table 200
//	ip rule add from <oldIP> lookup 200 priority 200
func setupEgressGuardrails(oldIP net.IP) error { //nolint:unparam // returns error to keep a uniform setup-step signature; failures are logged, not propagated
	const (
		blackholeTable = 200
		allowSelfPrio  = 100
		allowLoopPrio  = 101
		blackholePrio  = 200
	)

	fmt.Println("Setting up egress guardrails via policy routing...")

	// Determine CIDR mask based on IP version
	isIPv6 := oldIP.To4() == nil
	prefixLen := 32
	ipBits := 32
	if isIPv6 {
		prefixLen = 128
		ipBits = 128
	}

	// Rule 1: Allow traffic from oldIP to oldIP (local self-traffic)
	rule1 := netlink.NewRule()
	rule1.Src = &net.IPNet{IP: oldIP, Mask: net.CIDRMask(prefixLen, ipBits)}
	rule1.Dst = &net.IPNet{IP: oldIP, Mask: net.CIDRMask(prefixLen, ipBits)}
	rule1.Table = unix.RT_TABLE_MAIN
	rule1.Priority = allowSelfPrio
	if err := netlink.RuleAdd(rule1); err != nil {
		if !strings.Contains(err.Error(), "file exists") {
			fmt.Printf("Warning: Failed to add self-traffic rule: %v\n", err)
		}
	} else {
		fmt.Printf("  Added rule: from %s to %s lookup main (prio %d)\n", oldIP, oldIP, allowSelfPrio)
	}

	// Rule 2: Allow traffic from oldIP to loopback range (127.0.0.0/8 for IPv4, ::1/128 for IPv6)
	rule2 := netlink.NewRule()
	rule2.Src = &net.IPNet{IP: oldIP, Mask: net.CIDRMask(prefixLen, ipBits)}
	var loopbackNet *net.IPNet
	if isIPv6 {
		_, loopbackNet, _ = net.ParseCIDR("::1/128")
	} else {
		_, loopbackNet, _ = net.ParseCIDR("127.0.0.0/8")
	}
	rule2.Dst = loopbackNet
	rule2.Table = unix.RT_TABLE_MAIN
	rule2.Priority = allowLoopPrio
	if err := netlink.RuleAdd(rule2); err != nil {
		if !strings.Contains(err.Error(), "file exists") {
			fmt.Printf("Warning: Failed to add loopback rule: %v\n", err)
		}
	} else {
		fmt.Printf("  Added rule: from %s to %s lookup main (prio %d)\n", oldIP, loopbackNet, allowLoopPrio)
	}

	// Add blackhole route in custom table
	blackholeRoute := &netlink.Route{
		Dst:   nil, // default route
		Table: blackholeTable,
		Type:  unix.RTN_BLACKHOLE,
	}
	if err := netlink.RouteAdd(blackholeRoute); err != nil {
		if !strings.Contains(err.Error(), "file exists") {
			fmt.Printf("Warning: Failed to add blackhole route: %v\n", err)
		}
	} else {
		fmt.Printf("  Added blackhole default route in table %d\n", blackholeTable)
	}

	// Rule 3: Send all other traffic from oldIP to blackhole table
	rule3 := netlink.NewRule()
	rule3.Src = &net.IPNet{IP: oldIP, Mask: net.CIDRMask(prefixLen, ipBits)}
	rule3.Table = blackholeTable
	rule3.Priority = blackholePrio
	if err := netlink.RuleAdd(rule3); err != nil {
		if !strings.Contains(err.Error(), "file exists") {
			fmt.Printf("Warning: Failed to add blackhole rule: %v\n", err)
		}
	} else {
		fmt.Printf("  Added rule: from %s lookup %d (blackhole, prio %d)\n", oldIP, blackholeTable, blackholePrio)
	}

	fmt.Println("Egress guardrails configured via netlink")
	return nil
}

// loadMetadata loads and parses the checkpoint metadata
func loadMetadata(checkpointPath string) (*CheckpointMetadata, error) {
	metadataPath := filepath.Join(checkpointPath, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var meta CheckpointMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &meta, nil
}

// verifyCheckpointIntegrity re-computes SHA-256 for checkpoint files and reports mismatches.
func verifyCheckpointIntegrity(checkpointPath string, integrity *CheckpointIntegrity) []string {
	var mismatches []string
	for relPath, expectedHash := range integrity.FileHashes {
		fullPath := filepath.Join(checkpointPath, relPath)
		f, err := os.Open(fullPath)
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: missing", relPath))
			continue
		}
		h := sha256.New()
		_, _ = io.Copy(h, f)
		_ = f.Close()
		actual := fmt.Sprintf("%x", h.Sum(nil))
		if actual != expectedHash {
			mismatches = append(mismatches, fmt.Sprintf("%s: expected %s..., got %s...", relPath, expectedHash[:16], actual[:16]))
		}
	}
	return mismatches
}

// initTracing wires OpenTelemetry into restore-entrypoint and threads
// the agent's traceparent (passed via OTEL_TRACE_PARENT env) into the
// returned context so all spans we create here become children of the
// agent's `restore.full` span.
//
// When OTEL_EXPORTER_OTLP_ENDPOINT is unset, returns a context without
// tracing and a no-op shutdown — restore-entrypoint runs unchanged.
func initTracing() (context.Context, func(context.Context) error, error) {
	ctx := context.Background()
	shutdown, err := tracing.Init(ctx, "nvsnap-restore-entrypoint")
	if err != nil {
		return ctx, shutdown, err
	}
	return tracing.ContextFromEnv(ctx), shutdown, nil
}

// straceProc holds the strace process started in PostRestore (before process unfreezes).
// Set by the CRIU callback, used by monitorProcess for cleanup.
var earlyStraceProc *os.Process

func main() {
	// Tee stdout+stderr to a persistent log file for post-mortem debugging.
	// K8s container log buffer rotates quickly; this file survives.
	if logFile, err := os.OpenFile("/tmp/restore-entrypoint.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		pr, pw, _ := os.Pipe()
		origStdout := os.Stdout
		os.Stdout = pw
		os.Stderr = pw
		go func() {
			_, _ = io.Copy(io.MultiWriter(origStdout, logFile), pr)
		}()
	}
	fmt.Println("=== NVSNAP Restore Entrypoint (go-criu) ===")

	// OpenTelemetry. No-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset.
	// When the agent sets OTEL_TRACE_PARENT in the placeholder pod env
	// (see internal/agent/restore.go), our spans nest under the agent's
	// `restore.full` span so the Jaeger UI shows one continuous flame
	// graph from agent → restore-entrypoint.
	otelCtx, otelShutdown, err := initTracing()
	if err != nil {
		fmt.Printf("Warning: tracing init failed: %v (continuing without OTel)\n", err)
	}
	defer func() {
		if otelShutdown != nil {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = otelShutdown(sctx)
		}
	}()
	rootCtx, rootSpan := tracing.Tracer().Start(otelCtx, "restore-entrypoint.full")
	defer rootSpan.End()

	// Enable core dumps for crash analysis
	// This sets the soft limit to match the hard limit
	var rlim syscall.Rlimit
	if gerr := syscall.Getrlimit(syscall.RLIMIT_CORE, &rlim); gerr == nil {
		rlim.Cur = rlim.Max // Set soft limit to hard limit
		if serr := syscall.Setrlimit(syscall.RLIMIT_CORE, &rlim); serr == nil {
			fmt.Printf("Core dumps enabled: soft=%d hard=%d\n", rlim.Cur, rlim.Max)
		}
	}
	bundlePath := getEnv("CRIU_BUNDLE_PATH", defaultBundlePath)
	checkpointBase := getEnv("CHECKPOINT_PATH", defaultCheckpointBase)
	checkpointID := os.Getenv("CHECKPOINT_ID")

	checkpointPath := checkpointBase
	if checkpointID != "" {
		checkpointPath = filepath.Join(checkpointBase, checkpointID)
	}

	// Also try to set core pattern (prefer checkpoint dir)
	corePattern := filepath.Join(checkpointPath, "core.%e.%p")
	if werr := os.WriteFile("/proc/sys/kernel/core_pattern", []byte(corePattern), 0o644); werr == nil {
		fmt.Printf("Core pattern set to %s\n", corePattern)
	} else {
		fmt.Printf("Warning: could not set core_pattern to %s: %v\n", corePattern, werr)
		_ = os.WriteFile("/proc/sys/kernel/core_pattern", []byte("/tmp/core.%e.%p"), 0o644)
	}

	fmt.Printf("Bundle path: %s\n", bundlePath)
	fmt.Printf("Checkpoint path: %s\n", checkpointPath)

	// NOTE: Do NOT add CRIU bundle's lib/ to LD_LIBRARY_PATH.
	// The CRIU bundle contains libc.so.6 from Ubuntu 22.04 (GLIBC 2.35).
	// If we add it to LD_LIBRARY_PATH, ALL processes in the container
	// (including /bin/sh and the restored process) load our old libc,
	// which breaks containers using newer GLIBC (e.g., SGLang with 2.38).
	// CRIU's own wrapper (criu-wrapper) handles its library path internally.
	fmt.Printf("LD_LIBRARY_PATH=%s\n", os.Getenv("LD_LIBRARY_PATH"))

	// Add bundle path to PATH so cuda-checkpoint can be found by CUDA plugin
	currentPATH := os.Getenv("PATH")
	newPATH := bundlePath + ":" + currentPATH
	_ = os.Setenv("PATH", newPATH)
	fmt.Printf("Set PATH=%s\n", newPATH)

	// Check CRIU availability via go-criu
	c := criu.MakeCriu()

	// Set CRIU binary path - prefer wrapper for LD_LIBRARY_PATH stability
	criuWrapper := filepath.Join(bundlePath, "criu-wrapper")
	criuBinary := filepath.Join(bundlePath, "criu")
	if _, serr := os.Stat(criuBinary); serr == nil {
		ensureCriuOnPath(criuBinary)
	}
	if _, serr := os.Stat(criuWrapper); serr == nil {
		c.SetCriuPath(criuWrapper)
		fmt.Printf("Using CRIU wrapper: %s\n", criuWrapper)
	} else if _, serr := os.Stat(criuBinary); serr == nil {
		c.SetCriuPath(criuBinary)
		fmt.Printf("Using CRIU binary: %s\n", criuBinary)
	} else {
		// Fall back to PATH
		fmt.Println("CRIU binary not in bundle, using PATH")
	}

	// Avoid preloading intercept library into CRIU worker.
	if savedPreload := os.Getenv("LD_PRELOAD"); savedPreload != "" {
		_ = os.Unsetenv("LD_PRELOAD")
		fmt.Println("Temporarily cleared LD_PRELOAD for CRIU")
		defer func() { _ = os.Setenv("LD_PRELOAD", savedPreload) }()
	}

	// Probe selected CRIU path for early diagnostics.
	if _, serr := os.Stat(criuWrapper); serr == nil {
		probeCriuBinary(criuWrapper)
	}
	if _, serr := os.Stat(criuBinary); serr == nil {
		probeCriuBinary(criuBinary)
	}

	version, err := c.GetCriuVersion()
	if err != nil {
		fatal("CRIU not available: %v", err)
	}
	fmt.Printf("CRIU version: %d (via go-criu)\n", version)

	// Wait for checkpoint to be ready.
	// Check for inventory.img (normal) or stream.lz4 (streaming checkpoint).
	inventoryPath := filepath.Join(checkpointPath, "inventory.img")
	streamPath := filepath.Join(checkpointPath, "stream.lz4")
	fmt.Printf("Waiting for checkpoint at %s (or stream.lz4)...\n", inventoryPath)

	timeout := 60 * time.Second
	start := time.Now()
	for {
		if _, serr := os.Stat(inventoryPath); serr == nil {
			fmt.Println("Checkpoint found!")
			break
		}
		if _, serr := os.Stat(streamPath); serr == nil {
			fmt.Println("Streaming checkpoint found!")
			break
		}
		if time.Since(start) > timeout {
			// nvsnap#147: webhook-injected restore puts us here when
			// the rox PVC mounted but its dump is unreadable (snap+clone
			// produced an empty tree, image corruption, partial promote,
			// …). Cold-starting from the original entrypoint is strictly
			// better than CrashLoopBackOff — the workload comes up, the
			// pod becomes Ready, the operator sees the L2 miss in agent
			// logs / catalog state.
			//
			// Fallback is only attempted BEFORE we touch the process tree
			// (here, at the inventory-wait timeout). Once go-criu starts
			// restoring, partial state (half-mmapped libs, half-spawned
			// children, shared-memory segments holding the old fork
			// tree's pointers) makes exec'ing the original entrypoint
			// unsafe — we'd get bizarre corruption instead of a clean
			// cold start. Mid-restore failures therefore go straight to
			// fatal() → CrashLoopBackOff, which is a noisy explicit
			// signal that something deeper is wrong.
			//
			// Only triggers when NVSNAP_ORIG_COMMAND was injected by the
			// webhook. For e2e test manifests that invoke restore-entrypoint
			// directly the env var is unset; attemptColdStartFallback
			// returns silently and we fall through to fatal — preserving
			// the existing behavior for catching real bugs.
			attemptColdStartFallback("Timeout waiting for checkpoint at " + checkpointPath)
			fatal("Timeout waiting for checkpoint")
		}
		time.Sleep(1 * time.Second)
	}

	// Small delay to ensure files are fully written
	time.Sleep(500 * time.Millisecond)

	// Load and display enhanced metadata
	_, metaSpan := tracing.Tracer().Start(rootCtx, "restore.load_metadata")
	meta, err := loadMetadata(checkpointPath)
	metaSpan.End()
	if err != nil {
		fmt.Printf("Warning: Could not load enhanced metadata: %v (using defaults)\n", err)
		meta = &CheckpointMetadata{} // Use empty defaults
	} else {
		fmt.Println("\n=== Checkpoint Metadata ===")
		fmt.Printf("Version: %s\n", meta.Version)
		fmt.Printf("Container: %s/%s (%s)\n", meta.PodNamespace, meta.PodName, meta.ContainerName)
		fmt.Printf("Image: %s\n", meta.ContainerImage)
		fmt.Printf("Original PID: %d\n", meta.ContainerPID)

		if meta.Skipped != nil {
			fmt.Printf("Skipped Unix Sockets: %v\n", meta.Skipped.UnixSocketFds)
			fmt.Printf("Skipped Inet Sockets: %v\n", meta.Skipped.InetSocketFds)
			fmt.Printf("Skipped io_uring threads: %v\n", meta.Skipped.IoUringThreads)
			fmt.Printf("Skipped Mounts: %d entries\n", len(meta.Skipped.Mounts))
		}

		if meta.CUDA != nil && meta.CUDA.Enabled {
			fmt.Printf("CUDA: enabled (GPU PID: %d)\n", meta.CUDA.GPUPID)
		} else {
			fmt.Println("CUDA: not enabled")
		}

		if meta.Hints != nil {
			fmt.Printf("Restore Hints: SkipFds=%v, MappedFiles=%v, CUDARestore=%v\n",
				meta.Hints.SkipFds, meta.Hints.RestoreMappedFiles, meta.Hints.CUDARestoreNeeded)
		}

		if meta.SourcePodIP != "" {
			fmt.Printf("Source Pod IP: %s (will add to loopback for compatibility)\n", meta.SourcePodIP)
		}
		if meta.Integrity != nil {
			fmt.Printf("Integrity: %s (%d files, hash=%s...)\n",
				meta.Integrity.Algorithm, len(meta.Integrity.FileHashes), meta.Integrity.TotalHash[:16])
		}
		fmt.Println("===========================")
	}

	// Verify checkpoint integrity before restore
	if meta.Integrity != nil && len(meta.Integrity.FileHashes) > 0 {
		fmt.Println("Verifying checkpoint integrity...")
		mismatches := verifyCheckpointIntegrity(checkpointPath, meta.Integrity)
		if len(mismatches) > 0 {
			fmt.Printf("WARNING: %d file(s) failed integrity check:\n", len(mismatches))
			for _, m := range mismatches {
				fmt.Printf("  - %s\n", m)
			}
			fmt.Println("Proceeding with restore despite integrity warnings")
		} else {
			fmt.Printf("Integrity OK: all %d files verified\n", len(meta.Integrity.FileHashes))
		}
	}

	// Set up loopback alias for stable network identity
	// This allows restored processes to bind to their original pod IP
	if meta.SourcePodIP != "" {
		if lerr := setupLoopbackAlias(meta.SourcePodIP); lerr != nil {
			fmt.Printf("Warning: Failed to set up loopback alias: %v\n", lerr)
			fmt.Println("Continuing without loopback alias - worker sockets may fail to bind")
		}
	}

	// Ensure /dev/shm exists for semaphore ghost files
	if merr := os.MkdirAll("/dev/shm", 0o777); merr != nil {
		fmt.Printf("Warning: Could not ensure /dev/shm exists: %v\n", merr)
	}

	// NOTE: Ghost file pre-creation is now handled by CRIU directly.
	// We modified CRIU to open ghost files directly when link fails.
	// Pre-creating files here causes issues because they're empty.
	// if err := precreateGhostFiles(checkpointPath); err != nil {
	// 	fmt.Printf("Warning: Failed to pre-create ghost files: %v\n", err)
	// }

	// Restore mapped files (JIT-compiled .so files, caches, etc.) before CRIU restore
	// CRIU saves these to mapped-files/ directory but can't restore them if the paths
	// don't exist in the new container. Copy them to the correct locations.
	if meta.Hints == nil || meta.Hints.RestoreMappedFiles {
		_, mappedSpan := tracing.Tracer().Start(rootCtx, "restore.restore_mapped_files")
		if merr := restoreMappedFiles(checkpointPath); merr != nil {
			mappedSpan.RecordError(merr)
			mappedSpan.SetStatus(codes.Error, "mapped files")
			fmt.Printf("Warning: Failed to restore mapped files: %v\n", merr)
		}
		mappedSpan.End()
	}

	// Restore open files (log files, cache files, etc.) that CRIU expects to exist.
	// These are files that were open at checkpoint time but don't exist in the base image.
	_, openSpan := tracing.Tracer().Start(rootCtx, "restore.restore_open_files")
	if oerr := restoreOpenFiles(checkpointPath, meta); oerr != nil {
		openSpan.RecordError(oerr)
		fmt.Printf("Warning: Failed to restore open files: %v\n", oerr)
	}
	openSpan.End()
	// Ensure runtime-generated logs exist even if not captured.
	ensureRuntimeFiles()

	// Recreate temp directories that the process expects to exist
	// (e.g., Ray temp directories in Python's sys.path)
	_, tempSpan := tracing.Tracer().Start(rootCtx, "restore.restore_temp_dirs")
	if terr := restoreTempDirectories(checkpointPath); terr != nil {
		tempSpan.RecordError(terr)
		fmt.Printf("Warning: Failed to restore temp directories: %v\n", terr)
	}
	// Restore temp files captured from /tmp directories.
	if tferr := restoreTempFiles(checkpointPath); tferr != nil {
		tempSpan.RecordError(tferr)
		fmt.Printf("Warning: Failed to restore temp files: %v\n", tferr)
	}
	tempSpan.End()

	// For CUDA interposition: tell CRIU plugin to skip cuda-checkpoint calls.
	// The plugin still handles NVIDIA device FDs (RESTORE_EXT_FILE / UPDATE_VMA_MAP).
	if meta != nil && meta.CUDA != nil && meta.CUDA.Interposition {
		_ = os.Setenv("NVSNAP_SKIP_CUDA_CHECKPOINT", "1")
		fmt.Println("CUDA interposition: NVSNAP_SKIP_CUDA_CHECKPOINT=1")
	}

	// Perform CRIU restore using go-criu, passing metadata for informed decisions
	_, criuSpan := tracing.Tracer().Start(rootCtx, "restore.criu_restore")
	pid, ps, err := criuRestoreWithMetadata(c, checkpointPath, bundlePath, meta)
	if err != nil {
		criuSpan.RecordError(err)
		criuSpan.SetStatus(codes.Error, "CRIU restore failed")
		criuSpan.End()
	} else {
		criuSpan.SetAttributes(attribute.Int("nvsnap.restored_pid", pid))
		criuSpan.End()
	}
	if err != nil {
		// CRIU might crash after successful restore (e.g., detaching from dead threads)
		// Check if we got a PID and if the process is actually running
		if pid > 0 {
			// Check if process exists
			proc, procErr := os.FindProcess(pid)
			if procErr == nil {
				// Try to signal it (0 = check if alive)
				if signalErr := proc.Signal(syscall.Signal(0)); signalErr == nil {
					fmt.Printf("WARNING: CRIU returned error but PID %d is running, continuing...\n", pid)
					fmt.Printf("CRIU error was: %v\n", err)
					// Continue with the restored process
					goto processRunning
				}
			}
		}
		fatal("CRIU restore failed: %v", err)
	}

processRunning:

	fmt.Printf("=== Process restored with PID: %d ===\n", pid)

	// Write PID file for external monitoring
	if err := os.WriteFile("/tmp/restore.pid", []byte(strconv.Itoa(pid)), 0o644); err != nil { //nolint:gosec // fixed-path PID file read by the node agent, not user input
		fmt.Printf("Warning: Could not write PID file: %v\n", err)
	}

	// GPU restore. NOTE: with the CUDA plugin, the heavy GPU work
	// (RESTORE_EXT_FILE + page replay) happens *inside* restore.criu_restore,
	// not here. This span covers cuda-checkpoint's --action restore +
	// --action unlock state-machine advance, which short-circuits to
	// near-zero when the checkpoint was captured without the
	// cuda-checkpoint CLI (D2H/interposition path, or RM-ioctl
	// replay path). Inspect the attributes to know which case ran.
	_, gpuSpan := tracing.Tracer().Start(rootCtx, "restore.gpu_restore")
	gpuSpan.SetAttributes(attribute.Int("nvsnap.restored_pid", pid))
	if meta != nil && meta.CUDA != nil {
		gpuSpan.SetAttributes(
			attribute.Bool("nvsnap.cuda.metadata.enabled", meta.CUDA.Enabled),
			attribute.Bool("nvsnap.cuda.metadata.interposition", meta.CUDA.Interposition),
		)
	} else {
		gpuSpan.SetAttributes(attribute.String("nvsnap.cuda.metadata", "absent"))
	}
	restoreGPU(bundlePath, checkpointPath, pid)
	gpuSpan.End()

	// End the root span here — main() ends with monitorProcess which
	// blocks forever waiting on the restored workload, so deferred
	// rootSpan.End() at the top never fires. Without an explicit End()
	// the root never lands in Jaeger and the child spans look like
	// orphans referencing a parent that doesn't exist. Flush the OTel
	// batch synchronously so the root span exports BEFORE we block.
	rootSpan.SetAttributes(attribute.Int("nvsnap.restored_pid", pid))
	rootSpan.End()
	if otelShutdown != nil {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = otelShutdown(flushCtx)
		flushCancel()
		otelShutdown = nil // prevent the top-level defer from double-shutdown
	}

	// Post-restore PID liveness check for deterministic diagnostics.
	postRestorePIDHealthCheck(checkpointPath, pid)

	// Wake all threads stuck in epoll_wait/epoll_pwait after CRIU restore.
	// Library I/O threads (libzmq, libuv) resume inside blocking syscalls on
	// stale epoll FDs. SIGUSR2 causes EINTR, letting the event loops continue
	// to their restore-detection checks which reinitialize kernel state.
	// Run in background — signaling 668+ threads is fast but shouldn't block
	// the restore path. The restored process is already running at this point.
	go wakeRestoredThreads(pid)

	fmt.Println("=== Restore Complete ===")

	// Close write ends so only the restored process holds them.
	// When the process exits, read ends will get EOF.
	var logWg sync.WaitGroup
	if ps != nil {
		ps.CloseWriteEnds()

		// Stream restored process stdout/stderr to container logs in real-time.
		// This makes output visible via `kubectl logs` without exec'ing into the pod.
		if ps.StdoutR != nil {
			logWg.Add(1)
			go streamPipe(ps.StdoutR, os.Stdout, &logWg)
		}
		if ps.StderrR != nil {
			logWg.Add(1)
			go streamPipe(ps.StderrR, os.Stderr, &logWg)
		}
	}

	// Set up signal forwarding
	cleanup := setupSignalForwarding(pid)
	defer cleanup()

	// Monitor the restored process (pass strace started during PostRestore)
	monitorProcess(pid, earlyStraceProc)

	// Wait for pipe streams to drain (process exited, EOF on pipes)
	logWg.Wait()

	// Optional keepalive to allow log collection after exit
	if keepalive := os.Getenv("NVSNAP_KEEPALIVE_SECONDS"); keepalive != "" {
		if secs, err := strconv.Atoi(keepalive); err == nil && secs > 0 {
			fmt.Printf("Keeping container alive for %d seconds for post-mortem inspection...\n", secs)
			time.Sleep(time.Duration(secs) * time.Second)
		}
	}
}

// ensureRuntimeFiles creates placeholder runtime files CRIU may expect.
func ensureRuntimeFiles() {
	// Try to touch any flashinfer jit log under cache.
	if matches, _ := filepath.Glob("/root/.cache/flashinfer/*/*/flashinfer_jit.log"); len(matches) > 0 {
		for _, path := range matches {
			if err := touchFile(path); err == nil {
				fmt.Printf("Ensured runtime file: %s\n", path)
			}
		}
		return
	}
	// Fallback to the common flashinfer path.
	_ = touchFile("/root/.cache/flashinfer/0.5.3/90a/flashinfer_jit.log")
}

func touchFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Printf("Warning: Could not create directory %s: %v\n", filepath.Dir(path), err)
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Printf("Warning: Could not create %s: %v\n", path, err)
		return err
	}
	_ = f.Close()
	return nil
}

// criuRestoreWithMetadata performs CRIU restore using go-criu library.
// Uses enhanced metadata from checkpoint to make informed decisions.
// This avoids the slow ptrace-based detachment that CLI CRIU does.
// pipeStreams holds the read ends of stdout/stderr pipes and a function to
// close the write ends. The caller is responsible for streaming from the
// readers and closing write ends after CRIU restore succeeds.
type pipeStreams struct {
	StdoutR        *os.File
	StderrR        *os.File
	CloseWriteEnds func()
}

func criuRestoreWithMetadata(c *criu.Criu, checkpointPath, bundlePath string, meta *CheckpointMetadata) (int, *pipeStreams, error) {
	// Open checkpoint directory - go-criu needs an FD
	imageDir, err := os.Open(checkpointPath)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open checkpoint directory: %w", err)
	}
	defer func() { _ = imageDir.Close() }()
	// CRITICAL: Clear CLOEXEC so FD is inherited by CRIU child process
	// go-criu's swrk mode passes FD number via RPC
	if _, ferr := unix.FcntlInt(imageDir.Fd(), unix.F_SETFD, 0); ferr != nil {
		return 0, nil, fmt.Errorf("failed to clear CLOEXEC on images dir: %w", ferr)
	}

	fmt.Printf("Opened checkpoint directory fd=%d\n", imageDir.Fd())

	// Generate external mount mappings
	extMounts, err := generateExtMountMaps(meta)
	if err != nil {
		fmt.Printf("Warning: Could not generate mount maps: %v\n", err)
	}
	// Ensure external mount targets exist inside the container rootfs before CRIU runs.
	// Bind mounts require the target path to exist (file vs directory) or restore fails with ENOENT.
	if eerr := ensureExtMountTargets(extMounts); eerr != nil {
		fmt.Printf("Warning: Could not fully prepare external mount targets: %v\n", eerr)
	}

	// Create pipes to replace broken stdout/stderr pipes.
	// After restore, the original pipes have no reader (container runtime is gone).
	// We pipe through to our own stdout/stderr so `kubectl logs` works in real-time.
	var stdoutPipeID, stderrPipeID string
	if meta != nil {
		stdoutPipeID = meta.StdoutPipeID
		stderrPipeID = meta.StderrPipeID
	}
	inheritFds, stdoutR, stderrR, closeWriteEnds := setupInheritedFds(stdoutPipeID, stderrPipeID)
	defer closeWriteEnds() // safety net for early-exit paths
	ps := &pipeStreams{StdoutR: stdoutR, StderrR: stderrR, CloseWriteEnds: closeWriteEnds}

	// Determine plugin directory
	pluginDir := os.Getenv("CRIU_PLUGIN_DIR")
	if pluginDir == "" {
		pluginDir = filepath.Join(bundlePath, "plugins")
	}

	// Log fds that should be skipped based on metadata
	// NOTE: skip-missing-fds was REMOVED because it breaks shared fd coordination
	// between processes (pipes, sockets). The metadata.Hints.SkipFds tells us which
	// fds were intentionally skipped during dump (unix/inet sockets) but CRIU
	// currently doesn't support skipping specific fds.
	if meta != nil && meta.Hints != nil && len(meta.Hints.SkipFds) > 0 {
		fmt.Printf("Note: Checkpoint skipped fds: %v (socket fds skipped during dump)\n", meta.Hints.SkipFds)
		fmt.Println("These fds may cause 'No file for fd' warnings but should not block restore")
	}

	// Config file for CRIU with plugin directory and flags for container restore
	// Following k8s-runc-bypass pattern + our custom flags
	// NOTE: skip-missing-fds REMOVED - it breaks shared fd (pipe/socket) coordination
	// NOTE: tcp-close REMOVED - it breaks intra-process TCP connections (e.g., vLLM TCPStore)
	//       The loopback alias for old pod IP allows these connections to work after restore
	configPath := "/tmp/restore-criu.conf"

	// Load CUDA plugin for GPU checkpoints — needed for NVIDIA FD handling
	// (RESTORE_EXT_FILE / UPDATE_VMA_MAP). Required both for standard cuda-checkpoint
	// path (GPUPID > 0) and for interposition path (Interposition = true).
	var configContent string
	needsCUDAPlugin := meta != nil && meta.CUDA != nil &&
		(meta.CUDA.GPUPID > 0 || meta.CUDA.Interposition)
	// allow-uprobes: symmetric with dump side. CRIU refuses to restore an
	// image taken with --allow-uprobes unless restore sets it too ("Dumped
	// with --allow-uprobes. Need to set it on restore as well."). Harmless
	// on clusters without uprobe instrumentation.
	if needsCUDAPlugin {
		configContent = fmt.Sprintf(`libdir %s
enable-external-masters
tcp-established
link-remap
allow-uprobes
timeout 120
`, pluginDir)
		fmt.Printf("GPU checkpoint detected (gpuPid=%d, interposition=%v) - loading CUDA plugin\n", meta.CUDA.GPUPID, meta.CUDA.Interposition)
	} else {
		configContent = `enable-external-masters
tcp-established
link-remap
allow-uprobes
timeout 120
`
		fmt.Println("Non-GPU checkpoint - skipping CUDA plugin")
	}
	if werr := os.WriteFile(configPath, []byte(configContent), 0o644); werr == nil {
		fmt.Printf("Created CRIU config: %s\n", configPath)
	}

	// Plan A4: Map the checkpoint's external netns to this pod's netns via InheritFd.
	// This avoids CRIU trying to restore veth links (which fails in Kubernetes).
	myPid := os.Getpid()
	netNsPath := fmt.Sprintf("/proc/%d/ns/net", myPid)
	netNsFile, err := os.Open(netNsPath)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open netns %s: %w", netNsPath, err)
	}
	defer func() { _ = netNsFile.Close() }()
	if _, ferr := unix.FcntlInt(netNsFile.Fd(), unix.F_SETFD, 0); ferr != nil {
		fmt.Printf("Warning: Could not clear CLOEXEC on netns fd: %v\n", ferr)
	}
	inheritFds = append(inheritFds, &criurpc.InheritFd{
		Key: proto.String("extNetNs"),
		Fd:  proto.Int32(int32(netNsFile.Fd())),
	})
	fmt.Printf("Will map external netns via InheritFd key=extNetNs fd=%d (%s)\n", netNsFile.Fd(), netNsPath)

	// Build CRIU restore options. The restore.log path moves with the
	// CRIU WorkDir set below — under phase 5b that's a writable
	// scratch dir, not the read-only PVC images dir.
	cgMode := criurpc.CriuCgMode_IGNORE

	// Phase 5b: when ImagesDir is the per-capture PVC mount (read-only),
	// CRIU's default WorkDir = ImagesDir means it can't write
	// restore.log, restore-side IPC sockets, or its own pid file. CRIU
	// then exits at first syscall with `criu swrk failed: exit status
	// 1` and a near-empty error response (we observed `err:56` in
	// 6.5ms). Point WorkDir at a writable scratch dir so logs/IPC live
	// on tmpfs and the dump images stay RO on the PVC.
	if merr := os.MkdirAll(criuRestoreWorkDir, 0o755); merr != nil {
		return 0, nil, fmt.Errorf("create CRIU work dir %s: %w", criuRestoreWorkDir, merr)
	}
	workDirFile, err := os.Open(criuRestoreWorkDir)
	if err != nil {
		return 0, nil, fmt.Errorf("open CRIU work dir %s: %w", criuRestoreWorkDir, err)
	}
	defer func() { _ = workDirFile.Close() }()
	if _, err := unix.FcntlInt(workDirFile.Fd(), unix.F_SETFD, 0); err != nil {
		fmt.Printf("Warning: could not clear CLOEXEC on work dir fd: %v\n", err)
	}
	fmt.Printf("CRIU WorkDir: %s (fd=%d) — restore.log + IPC will land here\n", criuRestoreWorkDir, workDirFile.Fd())

	criuOpts := &criurpc.CriuOpts{
		ImagesDirFd: proto.Int32(int32(imageDir.Fd())),
		WorkDirFd:   proto.Int32(int32(workDirFile.Fd())),
		LogLevel:    proto.Int32(4),
		// CRIU disallows subdirectories in log_file; use WorkDir + filename.
		LogFile: proto.String("restore.log"),

		// Root filesystem - use current container's root
		Root: proto.String("/"),

		// RstSibling: Restore processes as siblings of the swrk process
		// This allows proper detachment when swrk exits (like k8s-runc-bypass)
		RstSibling: proto.Bool(true),

		// Mount namespace compatibility mode for cross-container restore
		MntnsCompatMode: proto.Bool(true),

		// Network namespace handling:
		// InheritFd maps dump-time external netns to this pod's netns (key=extNetNs).

		// Standard options
		ShellJob: proto.Bool(true),
		// TCP handling:
		// - TcpEstablished=true: Required when checkpoint was made with tcp-established
		// - TcpClose=false: Preserve established connections - the loopback alias for old
		//   pod IP allows intra-process connections (e.g., vLLM TCPStore) to continue working
		TcpEstablished: proto.Bool(true),
		TcpClose:       proto.Bool(false),

		// External Unix socket handling
		ExtUnixSk: proto.Bool(true),

		// Cgroup management - ignore to avoid conflicts
		ManageCgroups:     proto.Bool(true),
		ManageCgroupsMode: &cgMode,

		// Device and inode handling
		EvasiveDevices: proto.Bool(true),
		ForceIrmap:     proto.Bool(true),

		// Allow link remaps for ghost files (semaphores, deleted files)
		LinkRemap: proto.Bool(true),

		// Skip file mode checks (restored files may have different permissions)
		SkipFileRwxCheck: proto.Bool(true),

		// External mount mappings (DEPRECATED but ExtMnt still works)
		ExtMnt: extMounts,

		// Auto-detect external mounts (handles mounts that CRIU can't recreate)
		AutoExtMnt: proto.Bool(true),

		// Enable external masters for mounts with slave propagation
		// This handles NVIDIA bind mounts that have master_id from the original host
		ExtMasters: proto.Bool(true),

		// Inherit file descriptors:
		// - replace broken stdout/stderr pipes
		// - map external netns (key=extNetNs)
		InheritFd: inheritFds,

		// External mounts - auto-detected NVIDIA paths
		External: discoverExternalMounts(),
	}

	// Enable config file (like k8s-runc-bypass)
	criuOpts.ConfigFile = proto.String(configPath)

	// Log key options for debugging
	fmt.Printf("CRIU options: RstSibling=%v, InheritFd=%d entries, ExtMasters=%v, MntnsCompatMode=%v\n",
		*criuOpts.RstSibling, len(criuOpts.InheritFd), *criuOpts.ExtMasters, *criuOpts.MntnsCompatMode)
	fmt.Printf("External mounts: %d entries\n", len(criuOpts.External))

	// Create restore marker files BEFORE CRIU restore
	// The intercept library and patched uvloop check for these files to detect restore.
	// Can't use env vars because CRIU preserves the original process environment.
	// NOTE: /run/criu-restored is created via nsenter in PostRestore (below) because
	// the restored process has its own mount namespace — /run is a different tmpfs.
	for _, markerPath := range []string{
		"/var/run/nvsnap/.restored", // Legacy NVSNAP marker
		"/nvsnap/.restored",         // Shared volume marker
		"/nvsnap-lib/.restored",     // Shared volume marker
	} {
		if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
			fmt.Printf("Warning: could not create marker dir for %s: %v\n", markerPath, err)
		} else if err := os.WriteFile(markerPath, []byte("1"), 0o644); err != nil {
			fmt.Printf("Warning: could not create marker %s: %v\n", markerPath, err)
		} else {
			fmt.Printf("Created restore marker: %s\n", markerPath)
		}
	}

	// Restore uvloop metadata if present (copy legacy and per-pid files)
	if entries, err := os.ReadDir(checkpointPath); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "uvloop_loops") || !strings.HasSuffix(name, ".json") {
				continue
			}
			srcPath := filepath.Join(checkpointPath, name)
			data, err := os.ReadFile(srcPath)
			if err != nil || len(data) == 0 {
				continue
			}
			for _, destDir := range []string{"/nvsnap-lib", "/var/run/nvsnap"} {
				dest := filepath.Join(destDir, name)
				if err := os.WriteFile(dest, data, 0o644); err == nil {
					fmt.Printf("Restored uvloop metadata to %s\n", dest)
				} else {
					fmt.Printf("Warning: could not write uvloop metadata to %s: %v\n", dest, err)
				}
			}
		}
	}

	// Optional debug/behavior markers for the intercept library
	if os.Getenv("NVSNAP_DEBUG_IO_URING") != "" {
		if err := os.WriteFile("/nvsnap-lib/.debug_io_uring", []byte("1"), 0o644); err != nil {
			fmt.Printf("Warning: could not create /nvsnap-lib/.debug_io_uring: %v\n", err)
		} else {
			fmt.Println("Created debug marker: /nvsnap-lib/.debug_io_uring")
		}
	}
	if os.Getenv("NVSNAP_DISABLE_IO_URING_REINIT") != "" {
		if err := os.WriteFile("/nvsnap-lib/.disable_io_uring_reinit", []byte("1"), 0o644); err != nil {
			fmt.Printf("Warning: could not create /nvsnap-lib/.disable_io_uring_reinit: %v\n", err)
		} else {
			fmt.Println("Created marker: /nvsnap-lib/.disable_io_uring_reinit")
		}
	}
	if isTruthyEnv("NVSNAP_FORCE_UVLOOP_FORK") {
		if err := os.WriteFile("/nvsnap/.force_uvloop_fork", []byte("1"), 0o644); err != nil {
			fmt.Printf("Warning: could not create /nvsnap/.force_uvloop_fork: %v\n", err)
		} else {
			fmt.Println("Created marker: /nvsnap/.force_uvloop_fork")
		}
		if err := os.WriteFile("/nvsnap-lib/.force_uvloop_fork", []byte("1"), 0o644); err != nil {
			fmt.Printf("Warning: could not create /nvsnap-lib/.force_uvloop_fork: %v\n", err)
		} else {
			fmt.Println("Created marker: /nvsnap-lib/.force_uvloop_fork")
		}
	}

	// Start in-process serve streamer if checkpoint has compression metadata
	var serveDone <-chan error
	if meta.Compression != nil {
		// Convert CompressionMeta → streamer.CompressionInfo
		compInfo := &streamer.CompressionInfo{
			Algorithm:           meta.Compression.Algorithm,
			Level:               meta.Compression.Level,
			TotalOriginalSize:   meta.Compression.TotalOriginalSize,
			TotalCompressedSize: meta.Compression.TotalCompressedSize,
			Ratio:               meta.Compression.Ratio,
			Files:               make(map[string]streamer.CompressedFile, len(meta.Compression.Files)),
		}
		for k, v := range meta.Compression.Files {
			compInfo.Files[k] = streamer.CompressedFile{
				OriginalSize:   v.OriginalSize,
				CompressedSize: v.CompressedSize,
			}
		}
		var err error
		serveDone, err = streamer.StartServe(checkpointPath, compInfo)
		if err != nil {
			fatal("Failed to start serve streamer: %v", err)
		}
		criuOpts.Stream = proto.Bool(true)
		fmt.Printf("Streaming restore enabled (compressed checkpoint, %.1fx ratio)\n", meta.Compression.Ratio)
	} else {
		// Fall back to old external streamer for legacy stream.lz4 checkpoints
		streamerCleanup, streamerErr := startRestoreStreamer(checkpointPath, bundlePath)
		if streamerErr != nil {
			fatal("Failed to start restore streamer: %v", streamerErr)
		}
		if streamerCleanup != nil {
			criuOpts.Stream = proto.Bool(true)
			defer streamerCleanup()
			fmt.Println("Streaming restore enabled (legacy stream.lz4)")
		}
	}

	// Create notification handler to capture restored PID
	notify := &restoreNotify{startTime: time.Now(), checkpointPath: checkpointPath}

	// Day-1 lazy-pages spike: when NVSNAP_LAZY_PAGES=1 is set, spawn the
	// `criu lazy-pages` daemon and tell the restorer to userfaultfd
	// pages on demand from it. The daemon serves CPU pages out of the
	// dump dir; CRIU's restore returns as soon as the process is mapped
	// (most pages absent). First-touch causes a uffd fault → daemon
	// reads the page from images → ioctl-copy back. Process is "Ready"
	// long before all pages are loaded.
	// Runtime override: NIM workloads regress 2.2x with lazy-pages due to
	// scatter-pattern memory access during startup (the lazy daemon ends
	// up serving each fault serially). Auto-disable for NIM containers
	// regardless of NVSNAP_LAZY_PAGES setting. NIM_CACHE_PATH is set in
	// every NVIDIA NIM container.
	lazyEnabled := os.Getenv("NVSNAP_LAZY_PAGES") == "1"
	if lazyEnabled && os.Getenv("NIM_CACHE_PATH") != "" {
		fmt.Println("[LAZY-PAGES] NIM container detected (NIM_CACHE_PATH set) — disabling lazy-pages (known regression)")
		lazyEnabled = false
	}
	if lazyEnabled {
		fmt.Println("[LAZY-PAGES] NVSNAP_LAZY_PAGES=1 set — spawning criu lazy-pages daemon")
		lpLogFile, _ := os.Create("/tmp/criu-lazy-pages.log") //nolint:gosec // fixed-path diagnostic log inside the restored container, not user input
		// Use criu-wrapper if present — it sets up the bundled
		// ld-linux + LD_LIBRARY_PATH so criu can find its deps.
		// Falling back to raw /nvsnap/criu would fail with
		// "no such file or directory" from the dynamic linker.
		lpBin := filepath.Join(bundlePath, "criu-wrapper")
		if _, err := os.Stat(lpBin); err != nil {
			lpBin = filepath.Join(bundlePath, "criu")
		}
		lpCmd := exec.Command(lpBin, //nolint:gosec // bundle binary path + internally computed checkpoint path, not user input
			"lazy-pages",
			"-D", checkpointPath,
			"--work-dir", criuRestoreWorkDir,
			"-v4", "-o", "lazy-pages.log",
		)
		lpCmd.Stdout = lpLogFile
		lpCmd.Stderr = lpLogFile
		// Detach so it survives any sub-context cancellation.
		lpCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := lpCmd.Start(); err != nil {
			fmt.Printf("[LAZY-PAGES] FAILED to start daemon: %v — falling back to eager restore\n", err)
		} else {
			fmt.Printf("[LAZY-PAGES] daemon pid=%d (log: /tmp/criu-lazy-pages.log, criu log: %s/lazy-pages.log)\n", lpCmd.Process.Pid, criuRestoreWorkDir)
			// Give the daemon a moment to bind its socket before the
			// restorer tries to connect. CRIU's default socket lives in
			// the work dir as `lazy-pages.socket`.
			for i := 0; i < 50; i++ {
				if _, err := os.Stat(filepath.Join(criuRestoreWorkDir, "lazy-pages.socket")); err == nil {
					fmt.Printf("[LAZY-PAGES] daemon socket ready after %d ms\n", i*20)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			criuOpts.LazyPages = proto.Bool(true)
			fmt.Println("[LAZY-PAGES] criuOpts.LazyPages=true — restore will fault pages on demand")
		}
	}

	fmt.Println("Executing CRIU restore via go-criu...")
	fmt.Println("[DEBUG] About to call c.Restore()...")
	// CRIU disallows subdirectories in LogFile; use checkpoint dir as CWD.
	if origCwd, err := os.Getwd(); err == nil {
		if err := os.Chdir(checkpointPath); err != nil {
			fmt.Printf("Warning: could not chdir to %s for CRIU logs: %v\n", checkpointPath, err)
		} else {
			if cwd, err := os.Getwd(); err == nil {
				fmt.Printf("CRIU log cwd: %s\n", cwd)
			}
			defer func() { _ = os.Chdir(origCwd) }()
		}
	}
	overallRestoreStart := time.Now()

	// Retry restore if CRIU reports missing files.
	const maxRestoreAttempts = 10
	var lastErr error
	var pendingMissingPath string
	for attempt := 1; attempt <= maxRestoreAttempts; attempt++ {
		attemptStart := time.Now()
		notify = &restoreNotify{startTime: attemptStart, pendingMissingPath: pendingMissingPath, checkpointPath: checkpointPath}
		restoreErr := runCriuRestore(c, criuOpts, notify, attemptStart)
		if restoreErr == nil {
			fmt.Printf("CRIU restore completed in %v\n", time.Since(overallRestoreStart))

			// Wait for serve streamer to finish (if active)
			if serveDone != nil {
				if err := <-serveDone; err != nil {
					fmt.Printf("Warning: serve streamer error: %v\n", err)
				}
			}

			// Explicitly cleanup go-criu to release traced processes
			fmt.Println("Cleaning up CRIU connection...")
			_ = c.Cleanup()

			if notify.restoredPID > 0 {
				return int(notify.restoredPID), ps, nil
			}
			return 0, nil, fmt.Errorf("could not determine restored process PID")
		}

		lastErr = restoreErr
		// CRIU's restore.log lives under WorkDir (criuRestoreWorkDir),
		// not under ImagesDir. Reading from checkpointPath was correct
		// pre-phase-5b when WorkDir defaulted to ImagesDir; now they
		// diverge.
		logPath := filepath.Join(criuRestoreWorkDir, "restore.log")
		logData, _ := os.ReadFile(logPath)
		issuePath, issueReason, issueSize := findRestorePathIssue(string(logData))
		if issuePath == "" {
			// Log CRIU errors — dump ALL Error/Warn lines first, then last 100
			if len(logData) > 0 {
				lines := splitLines(string(logData))
				fmt.Printf("\n=== CRIU Log: Error/Warn lines (%d total lines) ===\n", len(lines))
				for _, line := range lines {
					if strings.Contains(line, "Error") || strings.Contains(line, "Warn") ||
						strings.Contains(line, "Can't") || strings.Contains(line, "killed") {
						fmt.Println(line)
					}
				}
				fmt.Printf("\n=== CRIU Log (last 100 lines) ===\n")
				start := len(lines) - 100
				if start < 0 {
					start = 0
				}
				for _, line := range lines[start:] {
					fmt.Println(line)
				}
			}

			// CRITICAL: If post-resume callback fired, the restore DID succeed and
			// tasks ARE running. CRIU may have crashed during cleanup, but we should
			// NOT exit - the restored process needs us to stay alive as its parent.
			if notify.postResumed && notify.restoredPID > 0 {
				fmt.Printf("WARNING: CRIU exited with error but post-resume was received.\n")
				fmt.Printf("         Restore likely succeeded. Continuing with PID %d.\n", notify.restoredPID)
				fmt.Printf("         CRIU error was: %v\n", restoreErr)
				return int(notify.restoredPID), ps, nil
			}
			return 0, nil, fmt.Errorf("go-criu restore failed: %w", restoreErr)
		}

		fmt.Printf("Detected restore file issue (%s): %s\n", issueReason, issuePath)

		// CRITICAL: Even with a file issue in the log, if post-resume fired
		// then processes ARE running. The "Can't link" line is from early in
		// the restore, but CRIU may have recovered (e.g., ghost file for
		// deleted semaphores). Don't retry — accept the restore.
		if notify.postResumed && notify.restoredPID > 0 {
			fmt.Printf("WARNING: CRIU log shows file issue but post-resume was received.\n")
			fmt.Printf("         Restore succeeded. Continuing with PID %d.\n", notify.restoredPID)
			fmt.Printf("         File issue was: %s (%s)\n", issuePath, issueReason)

			fmt.Println("Cleaning up CRIU connection...")
			_ = c.Cleanup()
			return int(notify.restoredPID), ps, nil
		}

		if (issueReason == "missing" || issueReason == "missing_remap") && strings.HasPrefix(issuePath, "/dev/shm/") {
			pendingMissingPath = issuePath
		} else {
			pendingMissingPath = ""
		}
		fixed, fixErr := restoreSingleFile(checkpointPath, issuePath)
		if fixErr != nil {
			return 0, nil, fmt.Errorf("failed to restore file %s: %w", issuePath, fixErr)
		}
		if !fixed {
			allowMissing := isTruthyEnv("NVSNAP_ALLOW_MISSING_RESTORE_FILES")
			allowCache := allowMissing && (strings.HasPrefix(issuePath, "/root/.cache/") || strings.HasPrefix(issuePath, "/tmp/") || strings.HasPrefix(issuePath, "/nvsnap-lib/"))
			if issueReason == "bad_size" && !allowCache {
				return 0, nil, fmt.Errorf("restore requires file contents but not found in checkpoint: %s", issuePath)
			}
			if issueReason != "missing" && issueReason != "bad_size" && !allowCache {
				return 0, nil, fmt.Errorf("restore file mismatch (%s) and missing cache allowlist: %s", issueReason, issuePath)
			}
			if issueReason == "bad_size" && allowCache && issueSize > 0 {
				if err := createSparseFile(issuePath, issueSize); err != nil {
					return 0, nil, fmt.Errorf("failed to create placeholder file %s: %w", issuePath, err)
				}
				fmt.Printf("Created placeholder cache file with size %d: %s\n", issueSize, issuePath)
			} else {
				if err := touchFile(issuePath); err != nil {
					return 0, nil, fmt.Errorf("failed to create missing file %s: %w", issuePath, err)
				}
				if allowCache {
					fmt.Printf("Created empty placeholder for cache file: %s\n", issuePath)
				}
			}
		}
		if attempt >= maxRestoreAttempts {
			return 0, nil, fmt.Errorf("go-criu restore failed after retries: %w", lastErr)
		}
		// Clean up ghost files from failed restore attempt. CRIU creates
		// .ghost files in /dev/shm (for semaphores) during restore and
		// fails on retry if they already exist.
		if ghosts, err := filepath.Glob("/dev/shm/*.ghost"); err == nil {
			for _, g := range ghosts {
				_ = os.Remove(g)
			}
			if len(ghosts) > 0 {
				fmt.Printf("Cleaned up %d ghost files from /dev/shm\n", len(ghosts))
			}
		}
		fmt.Printf("Retrying CRIU restore (attempt %d/%d)...\n", attempt+1, maxRestoreAttempts)
	}
	return 0, nil, fmt.Errorf("go-criu restore failed after retries: %w", lastErr)
}

func runCriuRestore(c *criu.Criu, criuOpts *criurpc.CriuOpts, notify *restoreNotify, start time.Time) error {
	done := make(chan error, 1)
	go func() {
		done <- c.Restore(criuOpts, notify)
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case restoreErr := <-done:
			fmt.Printf("[DEBUG] c.Restore() returned after %v\n", time.Since(start))
			return restoreErr
		case <-ticker.C:
			fmt.Printf("[DEBUG] Still waiting for c.Restore()... elapsed=%v, restoredPID=%d\n",
				time.Since(start), notify.restoredPID)
		}
	}
}

func findRestorePathIssue(logData string) (issuePath, issueReason string, issueLine int64) {
	lines := splitLines(logData)
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		const prefix = "Can't link "
		const arrow = "->"
		if !strings.Contains(line, prefix) || !strings.Contains(line, arrow) {
			continue
		}
		idx := strings.Index(line, arrow)
		if idx < 0 {
			continue
		}
		target := strings.TrimSpace(line[idx+len(arrow):])
		if target == "" {
			continue
		}
		if strings.Contains(target, ":") {
			parts := strings.SplitN(target, ":", 2)
			target = strings.TrimSpace(parts[0])
		}
		if !strings.HasPrefix(target, "/") {
			target = "/" + target
		}
		return target, "missing", -1
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		const prefix = "Can't open file "
		const suffix = " on restore"
		if !strings.Contains(line, prefix) || !strings.Contains(line, suffix) {
			continue
		}
		start := strings.Index(line, prefix)
		if start < 0 {
			continue
		}
		start += len(prefix)
		end := strings.Index(line[start:], suffix)
		if end < 0 {
			continue
		}
		path := strings.TrimSpace(line[start : start+end])
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return path, "missing", -1
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		const prefix = "File "
		const suffix = " has bad size"
		if !strings.Contains(line, prefix) || !strings.Contains(line, suffix) {
			continue
		}
		start := strings.Index(line, prefix)
		if start < 0 {
			continue
		}
		start += len(prefix)
		end := strings.Index(line[start:], suffix)
		if end < 0 {
			continue
		}
		path := strings.TrimSpace(line[start : start+end])
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return path, "bad_size", parseExpectedSize(line)
	}
	return "", "", -1
}

func parseExpectedSize(line string) int64 {
	const marker = "expect "
	idx := strings.Index(line, marker)
	if idx < 0 {
		return -1
	}
	start := idx + len(marker)
	end := start
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	if end == start {
		return -1
	}
	val, err := strconv.ParseInt(line[start:end], 10, 64)
	if err != nil {
		return -1
	}
	return val
}

func restoreSingleFile(checkpointPath, path string) (bool, error) {
	rel := strings.TrimPrefix(path, "/")
	candidates := []string{
		filepath.Join(checkpointPath, "mapped-files", rel),
		filepath.Join(checkpointPath, "open-files", rel),
		filepath.Join(checkpointPath, "shm-backup", filepath.Base(path)),
	}
	for _, src := range candidates {
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			continue
		}
		if merr := os.MkdirAll(filepath.Dir(path), 0o755); merr != nil {
			return false, merr
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return false, err
		}
		if err := os.WriteFile(path, data, info.Mode().Perm()); err != nil {
			return false, err
		}
		fmt.Printf("Restored missing file contents from checkpoint: %s\n", path)
		return true, nil
	}
	return false, nil
}

func createSparseFile(path string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Truncate(size)
}

// restoreNotify implements criu.Notify for restore callbacks
type restoreNotify struct {
	criu.NoNotify
	restoredPID        int32
	startTime          time.Time
	postResumed        bool // Set when tasks have actually resumed (restore succeeded)
	pendingMissingPath string
	checkpointPath     string // For writing /dev/shm/nvsnap-checkpoint-dir before processes resume
}

func (n *restoreNotify) PreRestore() error {
	n.startTime = time.Now()
	fmt.Printf("[%v] CRIU notify: pre-restore\n", time.Since(n.startTime))
	return nil
}

func (n *restoreNotify) PostRestore(pid int32) error {
	n.restoredPID = pid
	fmt.Printf("[%v] CRIU notify: post-restore PID=%d\n", time.Since(n.startTime), pid)

	// Create /run/criu-restored inside the restored process's mount namespace.
	// The patched libuv (compiled into uvloop) checks this file in uv__io_poll()
	// to detect CRIU restore and call uv_loop_fork(). The container's /run is a
	// different tmpfs from the restored process's /run.
	//
	// Use /proc/<pid>/root/ to write into the restored filesystem directly.
	// This avoids nsenter/setns which fails with EINVAL when CRIU uses
	// MntnsCompatMode with hostPID (setns into restored mntns not allowed).
	if pid > 0 {
		markerPath := fmt.Sprintf("/proc/%d/root/run/criu-restored", pid)
		if err := os.MkdirAll(fmt.Sprintf("/proc/%d/root/run", pid), 0o755); err != nil {
			fmt.Printf("Warning: failed to mkdir /run in restored mntns: %v\n", err)
		}
		if err := os.WriteFile(markerPath, []byte("1"), 0o644); err != nil {
			fmt.Printf("Warning: failed to create marker in restored mntns: %v\n", err)
		} else {
			fmt.Printf("Created /run/criu-restored in restored process mount namespace (pid=%d)\n", pid)
		}
	}

	if delay := getEnvInt("NVSNAP_POST_RESTORE_DELAY_SECONDS", 0); delay > 0 {
		fmt.Printf("[%v] CRIU notify: post-restore delay %ds\n", time.Since(n.startTime), delay)
		time.Sleep(time.Duration(delay) * time.Second)
	}
	return nil
}

func (n *restoreNotify) PostResume() error {
	fmt.Printf("[%v] CRIU notify: post-resume - process running!\n", time.Since(n.startTime))
	n.postResumed = true // Mark that tasks have actually resumed

	// Start strace immediately after CRIU detaches ptrace and unfreezes the process.
	if n.restoredPID > 0 {
		earlyStraceProc = startStraceIfAvailable(int(n.restoredPID))
	}

	// GPU checkpoint-dir was written in SetupNamespaces (before processes resume).
	// Here we just create the restore marker + SIGUSR2 to wake threads.
	pid := n.restoredPID
	if pid > 0 && isTruthyEnv("NVSNAP_SKIP_CUDA_CHECKPOINT") {
		markerPath := fmt.Sprintf("/proc/%d/root/run/criu-restored", pid)
		if err := os.WriteFile(markerPath, []byte("1"), 0o644); err != nil {
			fmt.Printf("Warning: failed to create marker: %v\n", err)
		} else {
			fmt.Printf("Created /run/criu-restored (pid=%d)\n", pid)
		}
		if !isTruthyEnv("NVSNAP_SKIP_SIGUSR2") {
			sendSIGUSR2ToTree(int(pid))
		}
	}

	if delay := getEnvInt("NVSNAP_POST_RESUME_DELAY_SECONDS", 0); delay > 0 {
		fmt.Printf("[%v] CRIU notify: post-resume delay %ds\n", time.Since(n.startTime), delay)
		time.Sleep(time.Duration(delay) * time.Second)
	}
	return nil
}

func (n *restoreNotify) NetworkLock() error {
	fmt.Printf("[%v] CRIU notify: network-lock\n", time.Since(n.startTime))
	return nil
}

func (n *restoreNotify) NetworkUnlock() error {
	fmt.Printf("[%v] CRIU notify: network-unlock - callback starting\n", time.Since(n.startTime))
	// Per nvsnap_restore_hang_fix.md: CRIU blocks until we ACK this
	// go-criu should send NotifySuccess=true after we return nil
	fmt.Printf("[%v] CRIU notify: network-unlock - returning nil (ACK)\n", time.Since(n.startTime))
	return nil
}

func (n *restoreNotify) SetupNamespaces(pid int32) error {
	fmt.Printf("[%v] CRIU notify: setup-namespaces PID=%d\n", time.Since(n.startTime), pid)
	if n.pendingMissingPath != "" && strings.HasPrefix(n.pendingMissingPath, "/dev/shm/") {
		if err := ensurePathInMountNS(pid, n.pendingMissingPath); err != nil {
			fmt.Printf("Warning: failed to create %s in restore mount namespace: %v\n", n.pendingMissingPath, err)
		} else {
			fmt.Printf("Created %s in restore mount namespace\n", n.pendingMissingPath)
		}
	}

	// Write GPU checkpoint path into the restored container's /dev/shm BEFORE
	// processes resume. The library's reinit reads this file to know where
	// GPU data was saved. Must happen here (not PostResume) to avoid a race
	// where reinit runs before the file exists.
	if n.checkpointPath != "" {
		containerCheckpointPath := filepath.Join("/checkpoints", filepath.Base(n.checkpointPath)) //nolint:gocritic // "/checkpoints" is an absolute base path, not a joinable component
		shmPath := fmt.Sprintf("/proc/%d/root/dev/shm/nvsnap-checkpoint-dir", pid)
		if err := os.WriteFile(shmPath, []byte(containerCheckpointPath), 0o644); err != nil {
			// Fallback: try without /proc/pid/root
			shmPath = "/dev/shm/nvsnap-checkpoint-dir"
			_ = os.WriteFile(shmPath, []byte(containerCheckpointPath), 0o644)
		}
		fmt.Printf("Wrote GPU checkpoint path to %s: %s\n", shmPath, containerCheckpointPath)
	}

	return nil
}

func (n *restoreNotify) PostSetupNamespaces() error {
	fmt.Printf("[%v] CRIU notify: post-setup-namespaces\n", time.Since(n.startTime))
	return nil
}

func ensurePathInMountNS(pid int32, path string) error {
	// Try /proc/<pid>/root/ first — works even when setns fails with EINVAL
	// (MntnsCompatMode + hostPID combination rejects setns into restored mntns).
	procPath := fmt.Sprintf("/proc/%d/root%s", pid, path)
	if err := os.MkdirAll(filepath.Dir(procPath), 0o755); err == nil {
		if err := touchFile(procPath); err == nil {
			fmt.Printf("Created %s via /proc/%d/root/ (no setns needed)\n", path, pid)
			return nil
		}
	}

	// Fall back to setns approach
	targetMntFd, err := os.Open(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	if err != nil {
		return err
	}
	defer func() { _ = targetMntFd.Close() }()
	targetUserFd, err := os.Open(fmt.Sprintf("/proc/%d/ns/user", pid))
	if err != nil {
		return err
	}
	defer func() { _ = targetUserFd.Close() }()
	selfMntFd, err := os.Open("/proc/self/ns/mnt")
	if err != nil {
		return err
	}
	defer func() { _ = selfMntFd.Close() }()
	selfUserFd, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer func() { _ = selfUserFd.Close() }()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := unix.Setns(int(targetUserFd.Fd()), unix.CLONE_NEWUSER); err != nil {
		return ensurePathWithNsenter(pid, path, err)
	}
	if err := unix.Setns(int(targetMntFd.Fd()), unix.CLONE_NEWNS); err != nil {
		_ = unix.Setns(int(selfUserFd.Fd()), unix.CLONE_NEWUSER)
		return ensurePathWithNsenter(pid, path, err)
	}
	touchErr := touchFile(path)
	_ = unix.Setns(int(selfMntFd.Fd()), unix.CLONE_NEWNS)
	_ = unix.Setns(int(selfUserFd.Fd()), unix.CLONE_NEWUSER)
	if touchErr != nil {
		return touchErr
	}
	return nil
}

func ensurePathWithNsenter(pid int32, path string, rootErr error) error {
	nsenterPath := "/nvsnap/nsenter"
	if _, err := os.Stat(nsenterPath); err != nil {
		if rootErr != nil {
			return rootErr
		}
		// Best-effort fallback in current namespace
		return touchFile(path)
	}
	// Run mkdir + touch as argv (NOT `sh -c "...<path>..."`): `path` comes
	// from a restored process's socket inventory and must never be spliced
	// into a shell, or a crafted path could inject commands into this
	// privileged nsenter call (nvsnap#91).
	base := []string{"-t", strconv.Itoa(int(pid)), "-m", "--"}
	mkdir := exec.Command(nsenterPath, append(append([]string{}, base...), "mkdir", "-p", filepath.Dir(path))...) //nolint:gosec // argv form (not sh -c); path is passed as a separate arg, never shell-spliced
	if out, err := mkdir.CombinedOutput(); err != nil {
		if rootErr != nil {
			return fmt.Errorf("setns failed: %w; nsenter mkdir failed: %w (out=%s)", rootErr, err, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("nsenter mkdir failed: %w (out=%s)", err, strings.TrimSpace(string(out)))
	}
	touch := exec.Command(nsenterPath, append(append([]string{}, base...), "touch", path)...) //nolint:gosec // argv form (not sh -c); path is passed as a separate arg, never shell-spliced
	if out, err := touch.CombinedOutput(); err != nil {
		if rootErr != nil {
			return fmt.Errorf("setns failed: %w; nsenter touch failed: %w (out=%s)", rootErr, err, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("nsenter touch failed: %w (out=%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// discoverExternalMounts auto-detects external mount mappings for CRIU.
// Instead of hardcoding NVIDIA driver versions, we scan the filesystem.
func discoverExternalMounts() []string {
	var externals []string //nolint:prealloc // final length depends on runtime filesystem/device scans, not statically known

	// Static NVIDIA binary paths (version-independent)
	nvidiaBinaries := []string{
		"/usr/bin/nvidia-smi",
		"/usr/bin/nvidia-debugdump",
		"/usr/bin/nvidia-persistenced",
		"/usr/bin/nv-fabricmanager",
		"/usr/bin/nvidia-cuda-mps-control",
		"/usr/bin/nvidia-cuda-mps-server",
	}

	for _, path := range nvidiaBinaries {
		if _, err := os.Stat(path); err == nil {
			externals = append(externals, fmt.Sprintf("mnt[%s]:%s", path, path))
		}
	}

	// Static NVIDIA paths
	staticPaths := []string{
		"/usr/lib/firmware/nvidia",
		"/proc/driver/nvidia",
	}
	for _, path := range staticPaths {
		if _, err := os.Stat(path); err == nil {
			externals = append(externals, fmt.Sprintf("mnt[%s]:%s", path, path))
		}
	}

	// Auto-detect NVIDIA libraries (any version)
	// Scan common library directories for libcuda.so.*, libnvidia-*.so.*
	libDirs := []string{
		"/usr/lib/x86_64-linux-gnu",
		"/usr/lib64",
		"/usr/local/cuda/lib64",
	}

	libPatterns := []string{
		"libcuda.so.*",
		"libnvidia-*.so.*",
	}

	for _, dir := range libDirs {
		for _, pattern := range libPatterns {
			matches, err := filepath.Glob(filepath.Join(dir, pattern))
			if err != nil {
				continue
			}
			for _, match := range matches {
				// Skip symlinks, only include actual versioned files
				info, err := os.Lstat(match)
				if err != nil || info.Mode()&os.ModeSymlink != 0 {
					continue
				}
				externals = append(externals, fmt.Sprintf("mnt[%s]:%s", match, match))
			}
		}
	}

	// discoverNvidiaMajors is defined below the function that uses it.
	// Auto-detect ALL NVIDIA devices by scanning /dev and checking major numbers.
	// Reads /proc/devices to find nvidia major numbers dynamically — no hardcoded
	// device names. Catches nvidia0-N, nvidiactl, nvidia-uvm, nvidia-uvm-tools,
	// nvidia-modeset, nvidia-caps, nvidia-nvswitch, nvidia-nvlink, etc.
	nvMajors := discoverNvidiaMajors()
	fmt.Println("=== DEBUG: Discovering NVIDIA devices for CRIU external ===")
	fmt.Printf("  NVIDIA major numbers from /proc/devices: %v\n", nvMajors)

	devicesAdded := 0
	devEntries, _ := os.ReadDir("/dev")
	for _, entry := range devEntries {
		devPath := filepath.Join("/dev", entry.Name()) //nolint:gocritic // "/dev" is an absolute base path
		stat, err := os.Stat(devPath)
		if err != nil {
			continue
		}
		if stat.Mode()&os.ModeDevice == 0 || stat.Mode()&os.ModeCharDevice == 0 {
			continue
		}
		sysStat, ok := stat.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		major := unix.Major(sysStat.Rdev)
		minor := unix.Minor(sysStat.Rdev)
		if !nvMajors[major] {
			continue
		}
		name := entry.Name()
		devExternal := fmt.Sprintf("dev[%d/%d]:%s", major, minor, name)
		externals = append(externals, devExternal)
		devicesAdded++
		fmt.Printf("  Added NVIDIA device: %s -> %s\n", devPath, devExternal)
	}
	// Also scan /dev/nvidia-caps/ subdirectory
	capsEntries, _ := os.ReadDir("/dev/nvidia-caps")
	for _, entry := range capsEntries {
		devPath := filepath.Join("/dev/nvidia-caps", entry.Name()) //nolint:gocritic // "/dev/nvidia-caps" is an absolute base path
		stat, err := os.Stat(devPath)
		if err != nil {
			continue
		}
		sysStat, ok := stat.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		major := unix.Major(sysStat.Rdev)
		minor := unix.Minor(sysStat.Rdev)
		if !nvMajors[major] {
			continue
		}
		name := "nvidia-caps/" + entry.Name()
		devExternal := fmt.Sprintf("dev[%d/%d]:%s", major, minor, name)
		externals = append(externals, devExternal)
		devicesAdded++
		fmt.Printf("  Added NVIDIA device: %s -> %s\n", devPath, devExternal)
	}

	fmt.Printf("=== Added %d NVIDIA devices to externals ===\n", devicesAdded)
	fmt.Printf("Auto-detected %d external mount/device mappings (total)\n", len(externals))
	return externals
}

// setupInheritedFds creates pipes to replace broken stdout/stderr pipes.
// Returns InheritFd entries for CRIU, read ends for streaming, and a cleanup
// function that closes the write ends.
//
// Problem: After CRIU restore, the original stdout/stderr pipes have no reader
// (the container runtime's log collector is gone). Writes get EPIPE + SIGPIPE,
// crashing Python with "lost sys.stderr".
//
// Solution: Create os.Pipe() pairs. Pass write ends to CRIU via InheritFd.
// The caller reads from the read ends and copies to the entrypoint's own
// stdout/stderr, making output visible via `kubectl logs` in real-time.
func setupInheritedFds(stdoutPipeID, stderrPipeID string) (inheritFds []*criurpc.InheritFd, stdoutR, stderrR *os.File, closeWriteEnds func()) {
	var writers []*os.File

	if stdoutPipeID != "" {
		r, w, err := os.Pipe()
		if err != nil {
			fmt.Printf("Warning: Could not create stdout pipe: %v\n", err)
		} else {
			if _, err := unix.FcntlInt(w.Fd(), unix.F_SETFD, 0); err != nil {
				fmt.Printf("Warning: Could not clear CLOEXEC on stdout pipe: %v\n", err)
			}
			inheritFds = append(inheritFds, &criurpc.InheritFd{
				Key: proto.String(stdoutPipeID),
				Fd:  proto.Int32(int32(w.Fd())),
			})
			stdoutR = r
			writers = append(writers, w)
			fmt.Printf("Created stdout pipe (r=%d, w=%d, key=%s)\n", r.Fd(), w.Fd(), stdoutPipeID)
		}
	} else {
		fmt.Println("No stdout pipe ID in metadata - skipping stdout redirect")
	}

	if stderrPipeID != "" {
		r, w, err := os.Pipe()
		if err != nil {
			fmt.Printf("Warning: Could not create stderr pipe: %v\n", err)
		} else {
			if _, err := unix.FcntlInt(w.Fd(), unix.F_SETFD, 0); err != nil {
				fmt.Printf("Warning: Could not clear CLOEXEC on stderr pipe: %v\n", err)
			}
			inheritFds = append(inheritFds, &criurpc.InheritFd{
				Key: proto.String(stderrPipeID),
				Fd:  proto.Int32(int32(w.Fd())),
			})
			stderrR = r
			writers = append(writers, w)
			fmt.Printf("Created stderr pipe (r=%d, w=%d, key=%s)\n", r.Fd(), w.Fd(), stderrPipeID)
		}
	} else {
		fmt.Println("No stderr pipe ID in metadata - skipping stderr redirect")
	}

	closeWriteEnds = func() {
		for _, w := range writers {
			_ = w.Close()
		}
		writers = nil
	}

	return inheritFds, stdoutR, stderrR, closeWriteEnds
}

// streamPipe copies data from r to w until EOF, then closes r.
func streamPipe(r, w *os.File, wg *sync.WaitGroup) {
	defer wg.Done()
	defer func() { _ = r.Close() }()
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// generateExtMountMaps creates external mount mappings for CRIU
func generateExtMountMaps(meta *CheckpointMetadata) ([]*criurpc.ExtMountMap, error) {
	var maps []*criurpc.ExtMountMap //nolint:prealloc // count depends on runtime metadata/mount discovery, not statically known
	addedMounts := make(map[string]bool)

	// Root filesystem mapping
	maps = append(maps, &criurpc.ExtMountMap{
		Key: proto.String("/"),
		Val: proto.String("."),
	})
	addedMounts["/"] = true

	// Prefer dump-time mountpoints from metadata (Plan A) to ensure ExtMnt is consistent.
	// This avoids restore failures due to mismatched mount mapping expectations.
	var mounts []string
	if meta != nil && len(meta.DumpMountPoints) > 0 {
		mounts = meta.DumpMountPoints
	} else {
		// Fallback: parse current container's mounts
		var err error
		mounts, err = parseMountInfo("/proc/1/mountinfo")
		if err != nil {
			return maps, err
		}
	}

	for _, mp := range mounts {
		if addedMounts[mp] {
			continue
		}
		maps = append(maps, &criurpc.ExtMountMap{
			Key: proto.String(mp),
			Val: proto.String(mp),
		})
		addedMounts[mp] = true
	}

	// Standard masked paths
	maskedPaths := []string{
		"/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys",
		"/proc/sysrq-trigger", "/proc/acpi", "/proc/kcore",
		"/proc/keys", "/proc/latency_stats", "/proc/timer_list",
		"/proc/scsi", "/proc/interrupts", "/proc/asound",
		"/sys/firmware", "/sys/devices/virtual/powercap",
	}

	for _, path := range maskedPaths {
		if addedMounts[path] {
			continue
		}
		maps = append(maps, &criurpc.ExtMountMap{
			Key: proto.String(path),
			Val: proto.String(path),
		})
		addedMounts[path] = true
	}

	// Auto-detect NVIDIA bind mounts - mark as external so CRIU doesn't try
	// to recreate their slave propagation relationships
	nvidiaBinaries := []string{
		"/usr/bin/nvidia-smi",
		"/usr/bin/nvidia-debugdump",
		"/usr/bin/nvidia-persistenced",
		"/usr/bin/nvidia-cuda-mps-control",
		"/usr/bin/nvidia-cuda-mps-server",
		"/usr/lib/firmware/nvidia",
		"/proc/driver/nvidia",
	}

	for _, path := range nvidiaBinaries {
		if addedMounts[path] {
			continue
		}
		// Only add if path exists
		if _, err := os.Stat(path); err != nil {
			continue
		}
		maps = append(maps, &criurpc.ExtMountMap{
			Key: proto.String(path),
			Val: proto.String(path),
		})
		addedMounts[path] = true
	}

	// Auto-detect NVIDIA device nodes
	devPatterns := []string{"/dev/nvidia*", "/dev/nvidiactl", "/dev/nvidia-uvm*"}
	for _, pattern := range devPatterns {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			if addedMounts[path] {
				continue
			}
			maps = append(maps, &criurpc.ExtMountMap{
				Key: proto.String(path),
				Val: proto.String(path),
			})
			addedMounts[path] = true
		}
	}

	return maps, nil
}

// ensureExtMountTargets ensures that all external mount targets exist inside the container rootfs.
// CRIU bind-mount restore requires the target path to exist (file vs directory), otherwise it fails
// with ENOENT. This is particularly important for dynamically-created runtime paths.
func ensureExtMountTargets(maps []*criurpc.ExtMountMap) error {
	if len(maps) == 0 {
		return nil
	}
	var errs []string
	for _, m := range maps {
		if m == nil {
			continue
		}
		mp := m.GetKey()
		if mp == "" || mp == "/" || mp == "." {
			continue
		}
		// Never try to create targets under proc/sys pseudo-filesystems.
		// These are kernel-provided and not creatable with mkdir/touch.
		if mp == "/proc" || strings.HasPrefix(mp, "/proc/") || mp == "/sys" || strings.HasPrefix(mp, "/sys/") {
			continue
		}
		if _, err := os.Stat(mp); err == nil {
			continue
		}

		parent := filepath.Dir(mp)
		if parent != "" {
			if err := os.MkdirAll(parent, 0o755); err != nil {
				errs = append(errs, fmt.Sprintf("mkdirall(%s): %v", parent, err))
				continue
			}
		}

		base := filepath.Base(mp)
		isFile := false
		// Heuristics: if it looks like a file bind-mount target, create a placeholder file; otherwise a dir.
		if strings.HasPrefix(mp, "/dev/") {
			isFile = true
		}
		if strings.HasPrefix(mp, "/run/nvidia-container-devices/") {
			isFile = true
		}
		if strings.HasPrefix(base, "GPU-") {
			isFile = true
		}
		if ext := filepath.Ext(base); ext != "" {
			// Most single-file bind mounts have an extension (.so, .bin, etc).
			isFile = true
		}

		if isFile {
			f, err := os.OpenFile(mp, os.O_CREATE, 0o644)
			if err != nil {
				errs = append(errs, fmt.Sprintf("touch(%s): %v", mp, err))
				continue
			}
			_ = f.Close()
		} else {
			if err := os.MkdirAll(mp, 0o755); err != nil {
				errs = append(errs, fmt.Sprintf("mkdirall(%s): %v", mp, err))
				continue
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// parseMountInfo parses mountinfo and returns mount points
func parseMountInfo(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var mounts []string
	for _, line := range splitLines(string(data)) {
		fields := splitFields(line)
		if len(fields) >= 5 {
			mp := fields[4]
			if mp != "/" {
				mounts = append(mounts, mp)
			}
		}
	}
	return mounts, nil
}

// restoreGPU handles CUDA GPU state restoration
func restoreGPU(_, checkpointPath string, _ int) {
	// Check metadata for GPU restore strategy
	metadataPath := filepath.Join(checkpointPath, "metadata.json")
	metaBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		fmt.Println("No GPU state in checkpoint")
		return
	}

	var meta CheckpointMetadata
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		fmt.Println("No GPU state in checkpoint (metadata parse error)")
		return
	}

	if meta.CUDA == nil || !meta.CUDA.Enabled {
		fmt.Println("No GPU state in checkpoint")
		return
	}

	// CUDA interposition (D2H save): GPU memory is restored in-process by
	// nvsnap_checkpoint_restore_self() called from nvsnap_perform_restore_reinit()
	// in the intercept library. No external binary needed.
	if meta.CUDA.Interposition {
		fmt.Println("GPU state was saved via D2H during quiesce — will be restored in-process by intercept library")
		return
	}

	// Legacy nvsnap-gpu-restore binary path removed — D2H restore is in-process via
	// nvsnap_checkpoint_restore_self() called from nvsnap_perform_restore_reinit()

	// Standard path: GPU was restored by CRIU's cuda_plugin during restore
	if meta.CUDA.GPUPID > 0 {
		fmt.Printf("GPU state was in checkpoint (original GPU PID: %d) - restored by CRIU cuda_plugin\n", meta.CUDA.GPUPID)
	}
}

// setupSignalForwarding forwards signals to the restored process
func setupSignalForwarding(pid int) func() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)

	done := make(chan struct{})

	go func() {
		select {
		case sig := <-sigChan:
			fmt.Printf("Forwarding signal %v to PID %d\n", sig, pid)
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Signal(sig)
			}
		case <-done:
			return
		}
	}()

	return func() {
		signal.Stop(sigChan)
		close(done)
	}
}

func postRestorePIDHealthCheck(checkpointPath string, restoredPID int) {
	seconds := getEnvInt("NVSNAP_POST_RESTORE_PID_CHECK_SECONDS", 20)
	if seconds <= 0 {
		return
	}
	pids := collectRestoredPidsFromLog(checkpointPath)
	if restoredPID > 0 {
		pids[restoredPID] = true
	}
	if len(pids) == 0 {
		fmt.Println("Post-restore PID check: no PIDs parsed from restore log; skipping")
		return
	}
	fmt.Printf("Post-restore PID check: tracking %d PIDs for %ds\n", len(pids), seconds)

	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	missingLogged := make(map[int]bool)
	zombieLogged := make(map[int]bool)
	for time.Now().Before(deadline) {
		for pid := range pids {
			if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
				if !missingLogged[pid] {
					missingLogged[pid] = true
					logMissingRestoredPID(pid)
				}
				continue
			}
			if st, err := readProcState(pid); err == nil && st == "Z" {
				if !zombieLogged[pid] {
					zombieLogged[pid] = true
					logZombieRestoredPID(pid)
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	fmt.Println("Post-restore PID check complete")
}

// wakeRestoredThreads sends SIGUSR2 to all threads of the restored process tree.
// After CRIU restore, library I/O threads (libzmq, libuv) are stuck inside
// epoll_wait/epoll_pwait on stale kernel epoll state. SIGUSR2 causes these
// syscalls to return EINTR, allowing the library event loops to reach their
// CRIU restore detection checks and reinitialize.
// The LD_PRELOAD intercept library installs a no-op SIGUSR2 handler (without
// SA_RESTART) to ensure EINTR is delivered instead of process termination.
func wakeRestoredThreads(pid int) {
	// BFS traversal to collect ALL PIDs in the restored process tree.
	// Must be recursive: SGLang has grandchild processes (e.g., Scheduler is
	// a child of a multiprocessing fork server, not a direct child of root).
	// Without recursion, the Scheduler's libzmq IO thread never gets SIGUSR2
	// and stays stuck in epoll_wait on stale state.
	allPids := collectDescendants(pid)

	totalCount := 0
	unblocked := 0
	for _, p := range allPids {
		taskDir := fmt.Sprintf("/proc/%d/task", p)
		entries, err := os.ReadDir(taskDir)
		if err != nil {
			continue
		}
		count := 0
		for _, entry := range entries {
			tid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			// Check if SIGUSR2 is blocked by this thread. If so, use ptrace
			// to temporarily unblock it before signaling. libzmq IO threads
			// block all signals via sigfillset+pthread_sigmask, preventing
			// SIGUSR2 from interrupting their epoll_wait.
			if isSignalBlocked(p, tid, 12) { // 12 = SIGUSR2
				if unblockSignal(p, tid, 12) {
					unblocked++
				}
			}
			if err := syscall.Tgkill(p, tid, syscall.SIGUSR2); err == nil {
				count++
			}
		}
		fmt.Printf("wakeRestoredThreads: sent SIGUSR2 to %d/%d threads of PID %d\n",
			count, len(entries), p)
		totalCount += count
	}
	fmt.Printf("wakeRestoredThreads: total %d threads signaled across %d processes (unblocked %d)\n",
		totalCount, len(allPids), unblocked)
}

// isSignalBlocked checks if a specific signal is blocked by a thread.
func isSignalBlocked(pid, tid, signum int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/status", pid, tid))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "SigBlk:\t") {
			continue
		}
		mask := strings.TrimPrefix(line, "SigBlk:\t")
		mask = strings.TrimSpace(mask)
		// Parse hex mask; bit (signum-1) indicates blocked
		var val uint64
		_, _ = fmt.Sscanf(mask, "%x", &val)
		return val&(1<<uint(signum-1)) != 0 //nolint:gosec // signum is a valid signal number (1..64), signum-1 is non-negative
	}
	return false
}

// unblockSignal uses ptrace to unblock a signal in a thread's signal mask.
// This is needed for libzmq IO threads which block all signals.
func unblockSignal(_, tid, signum int) bool {
	// PTRACE_SEIZE doesn't stop the target (unlike PTRACE_ATTACH).
	// We then PTRACE_INTERRUPT to stop it, modify the mask, and continue.
	err := unix.PtraceSeize(tid)
	if err != nil {
		fmt.Printf("  ptrace SEIZE tid=%d failed: %v\n", tid, err)
		return false
	}
	defer func() { _ = unix.PtraceDetach(tid) }()

	// Interrupt the thread so we can modify its state
	err = unix.PtraceInterrupt(tid)
	if err != nil {
		fmt.Printf("  ptrace INTERRUPT tid=%d failed: %v\n", tid, err)
		return false
	}

	// Wait for the thread to stop, but DO NOT block indefinitely.
	// Threads can stay in TASK_UNINTERRUPTIBLE (D state) post-CRIU-restore
	// if they were inside a syscall that doesn't accept ptrace interruption
	// promptly (e.g., epoll_wait holding internal kernel locks during the
	// CRIU-restored fd table reinitialization). A blocking Wait4 there hangs
	// this goroutine indefinitely, which previously stalled the entire
	// wakeRestoredThreads outer loop and left the rest of the process tree
	// without SIGUSR2 — the symptom we hit on phase 5b vllm-small.
	//
	// Poll with WNOHANG up to 500ms; if the thread doesn't stop in that
	// window, give up and let the outer loop continue. The patched libzmq
	// has its own /run/criu-restored marker-check fallback that wakes IO
	// threads independently of SIGUSR2, so missing one ptrace unblock is
	// not catastrophic.
	var ws unix.WaitStatus
	deadline := time.Now().Add(500 * time.Millisecond)
	stopped := false
	for time.Now().Before(deadline) {
		gotPid, err := unix.Wait4(tid, &ws, unix.WNOHANG, nil)
		if err != nil {
			fmt.Printf("  wait4 tid=%d failed: %v\n", tid, err)
			return false
		}
		if gotPid == tid {
			stopped = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !stopped {
		fmt.Printf("  wait4 tid=%d timed out after 500ms — thread did not stop, skipping unblock\n", tid)
		return false
	}

	// Read current signal mask via PTRACE_GETSIGMASK (since Linux 3.11)
	// Syscall: ptrace(PTRACE_GETSIGMASK, tid, sizeof(sigset_t), &mask)
	const ptraceGetSigmask = 0x420a
	const ptraceSetSigmask = 0x420b
	var mask [8]byte // 64-bit signal mask
	_, _, errno := syscall.RawSyscall6(
		syscall.SYS_PTRACE, ptraceGetSigmask,
		uintptr(tid), 8, uintptr(unsafe.Pointer(&mask[0])), 0, 0) //nolint:gosec // ptrace requires a raw pointer to the signal-mask buffer
	if errno != 0 {
		fmt.Printf("  PTRACE_GETSIGMASK tid=%d failed: %v\n", tid, errno)
		return false
	}

	// Clear the bit for our signal (little-endian)
	byteIdx := (signum - 1) / 8
	bitIdx := uint((signum - 1) % 8) //nolint:gosec // signum is a valid signal number (1..64); the modulus is in [0,7]
	if mask[byteIdx]&(1<<bitIdx) == 0 {
		// Already unblocked
		return false
	}
	mask[byteIdx] &^= (1 << bitIdx)

	// Write modified mask back
	_, _, errno = syscall.RawSyscall6(
		syscall.SYS_PTRACE, ptraceSetSigmask,
		uintptr(tid), 8, uintptr(unsafe.Pointer(&mask[0])), 0, 0) //nolint:gosec // ptrace requires a raw pointer to the signal-mask buffer
	if errno != 0 {
		fmt.Printf("  PTRACE_SETSIGMASK tid=%d failed: %v\n", tid, errno)
		return false
	}

	return true
}

// collectDescendants returns all PIDs in the process tree rooted at pid,
// using BFS traversal of /proc/PID/task/TID/children.
func collectDescendants(rootPid int) []int {
	seen := map[int]bool{rootPid: true}
	queue := []int{rootPid}
	var allPids []int

	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		allPids = append(allPids, pid)

		// Read direct children of this process via /proc/PID/task/PID/children.
		// This file contains space-separated child PIDs.
		childrenPath := fmt.Sprintf("/proc/%d/task/%d/children", pid, pid)
		data, err := os.ReadFile(childrenPath)
		if err != nil || len(data) == 0 {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			cpid, err := strconv.Atoi(field)
			if err != nil || seen[cpid] {
				continue
			}
			seen[cpid] = true
			queue = append(queue, cpid)
		}
	}

	fmt.Printf("wakeRestoredThreads: discovered %d processes in tree rooted at PID %d\n",
		len(allPids), rootPid)
	return allPids
}

// startRestoreStreamer starts the lz4 → criu-image-streamer pipeline for
// streaming restore. Returns a cleanup function and nil error on success.
// If stream.lz4 doesn't exist or binaries are missing, returns nil cleanup
// and nil error (caller should fall back to non-streaming restore).
func startRestoreStreamer(checkpointPath, bundlePath string) (cleanup func(), err error) {
	streamFile := filepath.Join(checkpointPath, "stream.lz4")
	if _, serr := os.Stat(streamFile); os.IsNotExist(serr) {
		return nil, nil // no compressed stream, use normal restore
	}

	streamerBin := filepath.Join(bundlePath, "criu-image-streamer")
	lz4Bin := filepath.Join(bundlePath, "lz4")

	// Check binaries exist
	for _, bin := range []string{streamerBin, lz4Bin} {
		if _, serr := os.Stat(bin); os.IsNotExist(serr) {
			fmt.Printf("Streaming binary not found: %s, falling back to non-streaming restore\n", bin)
			return nil, nil
		}
	}

	fmt.Printf("Starting streaming restore: %s (%s)\n", streamFile, checkpointPath)

	// Pipeline: lz4 -d stream.lz4 | criu-image-streamer --images-dir DIR serve
	lz4Cmd := exec.Command(lz4Bin, "-d", streamFile, "-c")                            //nolint:gosec // bundle binary path + internally computed checkpoint path, not user input
	streamerCmd := exec.Command(streamerBin, "--images-dir", checkpointPath, "serve") //nolint:gosec // bundle binary path + internally computed checkpoint path, not user input

	// Clear LD_PRELOAD env so intercept library doesn't load into helper tools.
	// Note: /etc/ld.so.preload should NOT be set in restore manifests — the
	// restored process already has the library loaded from checkpoint.
	cleanEnv := filterEnv(os.Environ(), "LD_PRELOAD")
	lz4Cmd.Env = cleanEnv
	streamerCmd.Env = cleanEnv

	// Connect lz4 stdout → streamer stdin
	pipe, err := lz4Cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create lz4 stdout pipe: %w", err)
	}
	streamerCmd.Stdin = pipe
	streamerCmd.Stdout = os.Stdout
	streamerCmd.Stderr = os.Stderr
	lz4Cmd.Stderr = os.Stderr

	// Start both processes
	if err := lz4Cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start lz4: %w", err)
	}
	if err := streamerCmd.Start(); err != nil {
		_ = lz4Cmd.Process.Kill()
		_ = lz4Cmd.Wait()
		return nil, fmt.Errorf("failed to start criu-image-streamer: %w", err)
	}

	fmt.Printf("Streamer pipeline started (lz4 pid=%d, streamer pid=%d)\n",
		lz4Cmd.Process.Pid, streamerCmd.Process.Pid)

	// Wait for the serve socket to appear.
	// The streamer reads the entire checkpoint into RAM before creating the socket,
	// so this can take a while for large checkpoints (e.g., 3.5GB compressed = ~30s).
	socketPath := filepath.Join(checkpointPath, "streamer-serve.sock")
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			fmt.Printf("Streamer socket ready: %s\n", socketPath)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, serr := os.Stat(socketPath); os.IsNotExist(serr) {
		_ = lz4Cmd.Process.Kill()
		_ = streamerCmd.Process.Kill()
		_ = lz4Cmd.Wait()
		_ = streamerCmd.Wait()
		return nil, fmt.Errorf("streamer socket did not appear after 10s: %s", socketPath)
	}

	cleanup = func() {
		// Streamer exits naturally when CRIU closes the socket.
		// Wait for both processes to finish.
		_ = streamerCmd.Wait()
		_ = lz4Cmd.Wait()
		// Clean up socket
		_ = os.Remove(socketPath)
		fmt.Println("Streamer pipeline finished")
	}

	return cleanup, nil
}

// filterEnv returns a copy of env with the named variable removed.
func filterEnv(env []string, name string) []string {
	prefix := name + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func collectRestoredPidsFromLog(_ string) map[int]bool {
	pids := make(map[int]bool)
	// CRIU's restore.log lives under WorkDir (criuRestoreWorkDir).
	logPath := filepath.Join(criuRestoreWorkDir, "restore.log")
	data, err := os.ReadFile(logPath)
	if err != nil || len(data) == 0 {
		return pids
	}
	for _, line := range splitLines(string(data)) {
		const marker = "Restore on-core sigactions for "
		if !strings.Contains(line, marker) {
			continue
		}
		idx := strings.LastIndex(line, marker)
		if idx < 0 {
			continue
		}
		raw := strings.TrimSpace(line[idx+len(marker):])
		if raw == "" {
			continue
		}
		if pid, err := strconv.Atoi(raw); err == nil && pid > 0 {
			pids[pid] = true
		}
	}
	return pids
}

func logMissingRestoredPID(pid int) {
	fmt.Printf("Post-restore PID check: pid=%d missing from /proc\n", pid)
	dumpProcDetails(pid)
}

func logZombieRestoredPID(pid int) {
	fmt.Printf("Post-restore PID check: pid=%d is a ZOMBIE\n", pid)
	dumpProcDetails(pid)
}

func dumpProcDetails(pid int) {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	cmdlinePath := fmt.Sprintf("/proc/%d/cmdline", pid)
	wchanPath := fmt.Sprintf("/proc/%d/wchan", pid)
	if data, err := os.ReadFile(statusPath); err == nil {
		fmt.Printf("---- /proc/%d/status ----\n%s\n", pid, strings.TrimSpace(string(data)))
	}
	if data, err := os.ReadFile(cmdlinePath); err == nil && len(data) > 0 {
		cmd := strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
		if cmd != "" {
			fmt.Printf("---- /proc/%d/cmdline ----\n%s\n", pid, cmd)
		}
	}
	if data, err := os.ReadFile(wchanPath); err == nil && len(data) > 0 {
		fmt.Printf("---- /proc/%d/wchan ----\n%s\n", pid, strings.TrimSpace(string(data)))
	}
}

// monitorProcess waits for the restored process to exit and captures debug info.
// straceProc is the strace process started during PostRestore (may be nil).
func monitorProcess(pid int, straceProc *os.Process) {
	fmt.Printf("Monitoring restored process PID %d...\n", pid)

	// Start log capture goroutine
	logCapture := startLogCapture(pid)
	defer logCapture.Stop()

	// Clean up strace (started in PostRestore callback before process unfreezes)
	if straceProc != nil {
		defer func() {
			_ = straceProc.Signal(syscall.SIGTERM)
			_, _ = straceProc.Wait()
		}()
	}

	// Prefer wait4(WNOHANG) so we can reap the restored process if we are its parent
	// (which is typically true with RstSibling). If we are not the parent (ECHILD),
	// fall back to /proc-based liveness and zombie detection.

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()
	lastLogTime := startTime

	for {
		<-ticker.C

		// If we are the parent, reap the child and get an accurate exit status.
		var ws unix.WaitStatus
		wpid, werr := unix.Wait4(pid, &ws, unix.WNOHANG, nil)
		if wpid == pid {
			elapsed := time.Since(startTime)
			fmt.Printf("\n=== Restored process %d exited after %v ===\n", pid, elapsed)
			fmt.Printf("wait4 status: exit=%d signal=%d stopped=%v continued=%v core=%v\n",
				ws.ExitStatus(), ws.Signal(), ws.Stopped(), ws.Continued(), ws.CoreDump())
			logCapture.Dump()
			checkCoreDumps(pid)
			dumpStraceLog()
			break
		}
		if werr != nil && werr != unix.ECHILD && werr != unix.EINTR {
			fmt.Printf("Warning: wait4(%d) failed: %v\n", pid, werr)
		}

		// Check if process still exists
		if err := syscall.Kill(pid, 0); err != nil {
			elapsed := time.Since(startTime)
			fmt.Printf("\n=== Restored process %d exited after %v ===\n", pid, elapsed)

			// Try to get exit status from /proc/<pid>/stat (might be gone already)
			dumpProcessExitInfo(pid)

			// Dump any captured logs
			logCapture.Dump()

			// Check for core dumps
			checkCoreDumps(pid)

			// Dump strace log if we captured any
			dumpStraceLog()

			break
		}

		// If the process is a zombie, surface that clearly rather than reporting "still running".
		if st, stErr := readProcState(pid); stErr == nil && st == "Z" {
			elapsed := time.Since(startTime)
			fmt.Printf("\n=== Restored process %d is a ZOMBIE after %v ===\n", pid, elapsed.Round(time.Second))
			fmt.Println("This usually means the restored workload already crashed and needs to be reaped.")
			dumpProcessExitInfo(pid)
			logCapture.Dump()
			checkCoreDumps(pid)
			dumpStraceLog()
			break
		}

		// Periodic status update
		if time.Since(lastLogTime) >= 5*time.Minute {
			fmt.Printf("[%v] Process %d still running\n", time.Since(startTime).Round(time.Second), pid)
			lastLogTime = time.Now()

			// Show current thread state
			dumpThreadState(pid)
		}
	}
}

// logCapture captures output from a process by reading from /proc/<pid>/fd
type logCapture struct {
	pid      int
	stopCh   chan struct{}
	buffer   []string
	bufferMu sync.Mutex
}

func startLogCapture(pid int) *logCapture {
	lc := &logCapture{
		pid:    pid,
		stopCh: make(chan struct{}),
		buffer: make([]string, 0, 1000),
	}

	// Create log file for persistent capture
	logFile, err := os.Create("/tmp/nvsnap-output.log") //nolint:gosec // fixed-path diagnostic log inside the restored container, not user input
	if err != nil {
		fmt.Printf("Warning: Could not create log file: %v\n", err)
	} else {
		_ = logFile.Close()
		fmt.Println("Created /tmp/nvsnap-output.log for output capture")
	}

	// Try to capture stderr by reading /proc/<pid>/fd/2
	// This only works if stderr is a readable file/pipe
	go lc.captureLoop()

	return lc
}

func (lc *logCapture) captureLoop() {
	// Wait a bit for process to stabilize
	time.Sleep(2 * time.Second)

	stderrPath := fmt.Sprintf("/proc/%d/fd/2", lc.pid)

	// Check what stderr points to
	target, err := os.Readlink(stderrPath)
	if err != nil {
		fmt.Printf("Could not read stderr link: %v\n", err)
		return
	}
	fmt.Printf("Process stderr points to: %s\n", target)

	// If it's a pipe or file, we might be able to read it
	// For now, just log what we found
	if strings.HasPrefix(target, "pipe:") {
		fmt.Println("Note: stderr is a pipe - output capture limited")
	}

	// Watch for writes to /tmp by the process
	go lc.watchTmpFiles()
}

func (lc *logCapture) watchTmpFiles() {
	// Monitor /tmp for any files the process might create
	knownFiles := make(map[string]int64)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-lc.stopCh:
			return
		case <-ticker.C:
			entries, err := os.ReadDir("/tmp")
			if err != nil {
				continue
			}

			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				path := filepath.Join("/tmp", entry.Name()) //nolint:gocritic // "/tmp" is an absolute base path
				info, err := entry.Info()
				if err != nil {
					continue
				}

				// Check if file is new or grown
				size := info.Size()
				if oldSize, exists := knownFiles[path]; !exists || size > oldSize {
					if size > 0 && (strings.Contains(entry.Name(), "core") ||
						strings.Contains(entry.Name(), "nvsnap") ||
						strings.Contains(entry.Name(), "python") ||
						strings.HasSuffix(entry.Name(), ".log")) {
						fmt.Printf("New/updated file in /tmp: %s (size: %d)\n", entry.Name(), size)
					}
					knownFiles[path] = size
				}
			}
		}
	}
}

func (lc *logCapture) Stop() {
	close(lc.stopCh)
}

func (lc *logCapture) Dump() {
	lc.bufferMu.Lock()
	defer lc.bufferMu.Unlock()

	if len(lc.buffer) > 0 {
		fmt.Println("\n=== Captured Output ===")
		for _, line := range lc.buffer {
			fmt.Println(line)
		}
		fmt.Println("=======================")
	}

	// Also check for any log files
	logPath := "/tmp/nvsnap-output.log"
	if data, err := os.ReadFile(logPath); err == nil && len(data) > 0 {
		fmt.Printf("\n=== Contents of %s ===\n", logPath)
		fmt.Println(string(data))
		fmt.Println("=======================")
	}
	// Dump tails of the redirected stdout/stderr logs (these are the most useful signals).
	dumpFileTail("/tmp/nvsnap-stdout.log", 200)
	dumpFileTail("/tmp/nvsnap-stderr.log", 200)

}

func startStraceIfAvailable(pid int) *os.Process {
	// Strace uses ptrace which stops all threads — only enable when debugging
	if os.Getenv("NVSNAP_STRACE_ENABLED") != "1" {
		fmt.Println("strace disabled (set NVSNAP_STRACE_ENABLED=1 to enable)")
		return nil
	}

	// Check if strace is available
	stracePath, err := exec.LookPath("strace")
	if err != nil {
		fmt.Println("strace not available - syscall tracing disabled")
		fmt.Println("To enable: add strace to the container image")
		return nil
	}

	// Discover all PIDs in the process tree (restored processes are already running)
	// strace -f only follows NEW forks, not existing children
	pids := discoverDescendants(pid)
	fmt.Printf("Starting strace on %d PIDs: %v\n", len(pids), pids)

	logPath := "/tmp/nvsnap-strace.log"
	args := []string{
		"-f",          // Follow any new forks/threads
		"-tt",         // Timestamps
		"-T",          // Syscall duration
		"-o", logPath, // Output file
		"-e", "trace=!futex,nanosleep,clock_nanosleep,epoll_wait,ppoll", // Skip noisy syscalls
	}
	for _, p := range pids {
		args = append(args, "-p", strconv.Itoa(p))
	}

	cmd := exec.Command(stracePath, args...) //nolint:gosec // args are internally constructed (strace flags + discovered PIDs), not user input
	// Prevent intercept library from loading into strace
	cmd.Env = append(os.Environ(), "NVSNAP_ENABLED=0", "LD_PRELOAD=")

	if err := cmd.Start(); err != nil {
		fmt.Printf("Failed to start strace: %v\n", err)
		return nil
	}

	fmt.Printf("strace started with PID %d, output to %s\n", cmd.Process.Pid, logPath)
	return cmd.Process
}

// discoverDescendants returns pid and all its descendants by walking /proc.
func discoverDescendants(root int) []int {
	pids := []int{root}
	// Build parent→children map
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return pids
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		childPid, err := strconv.Atoi(e.Name())
		if err != nil || childPid == root {
			continue
		}
		// Read ppid from /proc/<pid>/stat
		statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", childPid))
		if err != nil {
			continue
		}
		// Format: pid (comm) state ppid ...
		// Find closing paren to skip comm (may contain spaces/parens)
		s := string(statData)
		idx := strings.LastIndex(s, ") ")
		if idx < 0 {
			continue
		}
		fields := strings.Fields(s[idx+2:])
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		// Check if this process is a descendant of root (walk up ppid chain)
		if isDescendantOf(childPid, ppid, root) {
			pids = append(pids, childPid)
		}
	}
	return pids
}

// isDescendantOf checks if a process (with known ppid) is a descendant of root.
func isDescendantOf(_, ppid, root int) bool {
	visited := make(map[int]bool)
	for ppid != 0 && ppid != 1 && !visited[ppid] {
		if ppid == root {
			return true
		}
		visited[ppid] = true
		statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", ppid))
		if err != nil {
			return false
		}
		s := string(statData)
		idx := strings.LastIndex(s, ") ")
		if idx < 0 {
			return false
		}
		fields := strings.Fields(s[idx+2:])
		if len(fields) < 2 {
			return false
		}
		ppid, err = strconv.Atoi(fields[1])
		if err != nil {
			return false
		}
	}
	return false
}

func dumpStraceLog() {
	logPath := "/tmp/nvsnap-strace.log"
	data, err := os.ReadFile(logPath)
	if err != nil {
		return
	}

	lines := splitLines(string(data))
	if len(lines) == 0 {
		return
	}

	fmt.Println("\n=== Strace Log (last 50 lines) ===")
	start := len(lines) - 50
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		fmt.Println(line)
	}
	fmt.Println("==================================")
}

func dumpProcessExitInfo(pid int) {
	// Try to read /proc/<pid>/status before it disappears
	statusPath := fmt.Sprintf("/proc/%d/status", pid)
	if data, err := os.ReadFile(statusPath); err == nil {
		fmt.Println("\n=== Process Status at Exit ===")
		fmt.Println(string(data))
	}

	// Check /proc/<pid>/fd to see what files were open
	fdPath := fmt.Sprintf("/proc/%d/fd", pid)
	if entries, err := os.ReadDir(fdPath); err == nil {
		fmt.Printf("\nOpen file descriptors: %d\n", len(entries))
	}
}

func dumpThreadState(pid int) {
	// Show thread states from /proc/<pid>/task
	taskPath := fmt.Sprintf("/proc/%d/task", pid)
	entries, err := os.ReadDir(taskPath)
	if err != nil {
		return
	}

	states := make(map[string]int)
	for _, entry := range entries {
		statPath := filepath.Join(taskPath, entry.Name(), "stat")
		if data, err := os.ReadFile(statPath); err == nil {
			// Parse state from stat - format: pid (comm) state ...
			fields := strings.Fields(string(data))
			if len(fields) >= 3 {
				state := fields[2]
				states[state]++
			}
		}
	}

	fmt.Printf("Thread states: ")
	for state, count := range states {
		fmt.Printf("%s=%d ", state, count)
	}
	fmt.Println()
}

func checkCoreDumps(_ int) {
	// Check common core dump locations
	patterns := []string{
		"/tmp/core.*",
		"/var/crash/*",
		"./core",
		"./core.*",
	}

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				continue
			}
			fmt.Printf("Found core dump: %s (size: %d bytes)\n", match, info.Size())
		}
	}
}

// readProcState returns the process state from /proc/<pid>/stat (e.g., "R", "S", "D", "Z").
func readProcState(pid int) (string, error) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return "", err
	}
	// /proc/<pid>/stat: pid (comm) state ...
	// comm can contain spaces, so locate the trailing ")".
	s := string(data)
	closeIdx := strings.LastIndex(s, ")")
	if closeIdx < 0 || closeIdx+2 >= len(s) {
		return "", fmt.Errorf("unexpected stat format")
	}
	rest := strings.TrimSpace(s[closeIdx+1:])
	fields := strings.Fields(rest)
	if len(fields) < 1 {
		return "", fmt.Errorf("unexpected stat format")
	}
	return fields[0], nil
}

// dumpFileTail prints the last N lines of a file to stdout.
func dumpFileTail(path string, maxLines int) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return
	}
	lines := splitLines(string(data))
	start := 0
	if maxLines > 0 && len(lines) > maxLines {
		start = len(lines) - maxLines
	}
	fmt.Printf("\n=== Tail of %s (last %d lines) ===\n", path, maxLines)
	for _, l := range lines[start:] {
		fmt.Println(l)
	}
	fmt.Println("===============================")
}

// Helper functions

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
	}
	return defaultValue
}

// sendSIGUSR2ToTree sends SIGUSR2 to the main thread of each process in the tree.
func sendSIGUSR2ToTree(rootPID int) {
	rootNS, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", rootPID))
	if err != nil {
		return
	}
	entries, _ := os.ReadDir("/proc")
	count := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		ns, _ := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", pid))
		if ns != rootNS {
			continue
		}
		if syscall.Kill(pid, syscall.Signal(12)) == nil {
			count++
		}
	}
	fmt.Printf("sendSIGUSR2ToTree: sent SIGUSR2 to %d processes\n", count)
}

func isTruthyEnv(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}

// attemptColdStartFallback decodes the webhook-injected
// NVSNAP_ORIG_COMMAND / NVSNAP_ORIG_ARGS env vars and syscall.Execs the
// workload's original entrypoint. Used when the restore plumbing put
// us in this container but the dump isn't readable — we'd rather
// cold-start the workload than crash-loop. The pod comes up, becomes
// Ready, and the operator sees the L2 miss in agent logs / catalog
// state (not in CrashLoopBackOff alerts that demand a human).
//
// Semantics:
//   - syscall.Exec replaces the current process on success and never
//     returns. The function therefore returns ONLY when the fallback
//     was inapplicable (env vars unset, JSON decode error, empty argv);
//     the caller continues with its own error-handling path.
//   - When the fallback IS applicable but syscall.Exec fails, the
//     function fatals — fatal() exits the process so we never return
//     to the caller mid-way through a half-replaced process state.
//
// Env-var contract (set by the mutating admission webhook in
// internal/webhook/restore_entrypoint.go):
//   - NVSNAP_ORIG_COMMAND: JSON-encoded []string of the workload pod's
//     spec.containers[main].command. argv[0] is the binary to exec.
//   - NVSNAP_ORIG_ARGS:    JSON-encoded []string of
//     spec.containers[main].args, appended after NVSNAP_ORIG_COMMAND.
//
// NVSNAP_ORIG_COMMAND unset → fallback inapplicable: without an argv[0]
// we can't exec anything safely (the workload image's ENTRYPOINT is
// opaque to us at this point in the lifecycle).
func attemptColdStartFallback(reason string) {
	cmdJSON := os.Getenv(envOrigCommand)
	if cmdJSON == "" {
		return
	}
	var cmd []string
	if err := json.Unmarshal([]byte(cmdJSON), &cmd); err != nil {
		fmt.Fprintf(os.Stderr, "cold-start fallback: failed to decode %s=%q: %v\n",
			envOrigCommand, cmdJSON, err)
		return
	}
	if len(cmd) == 0 || cmd[0] == "" {
		fmt.Fprintf(os.Stderr, "cold-start fallback: %s decoded to empty argv\n", envOrigCommand)
		return
	}
	var args []string
	if argsJSON := os.Getenv(envOrigArgs); argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			fmt.Fprintf(os.Stderr, "cold-start fallback: failed to decode %s=%q: %v (treating as no args)\n",
				envOrigArgs, argsJSON, err)
			args = nil
		}
	}
	full := append([]string{}, cmd...)
	full = append(full, args...)

	bin, err := exec.LookPath(full[0])
	if err != nil {
		// Try the literal path — full[0] might be absolute already.
		if _, statErr := os.Stat(full[0]); statErr == nil {
			bin = full[0]
		} else {
			fatal("cold-start fallback (%s): cannot resolve %q: %v", reason, full[0], err)
		}
	}

	fmt.Fprintf(os.Stderr,
		"cold-start fallback (%s): exec'ing original entrypoint %q with %d arg(s)\n",
		reason, bin, len(full)-1)

	// On success this never returns; the workload process inherits
	// our PID, env, fds. CHECKPOINT_PATH + friends carry through
	// unchanged — the workload will simply not see a checkpoint there
	// and behave as it would on a fresh pod.
	execErr := syscall.Exec(bin, full, os.Environ()) //nolint:gosec // bin/argv are the container's own configured entrypoint, not user input
	fatal("cold-start fallback (%s): syscall.Exec %q failed: %v", reason, bin, execErr)
}

// envOrigCommand and envOrigArgs mirror the constants in
// internal/webhook/restore_entrypoint.go. They're duplicated rather
// than imported because the webhook package has K8s dependencies the
// restore-entrypoint binary intentionally avoids (smaller binary, no
// admission-side runtime in the workload pod).
const (
	envOrigCommand = "NVSNAP_ORIG_COMMAND"
	envOrigArgs    = "NVSNAP_ORIG_ARGS"
)

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitFields(s string) []string {
	var fields []string
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if start >= 0 {
				fields = append(fields, s[start:i])
				start = -1
			}
		} else {
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		fields = append(fields, s[start:])
	}
	return fields
}

// restoreTempDirectories recreates temp directories that existed at checkpoint time.
// These directories may be referenced in Python's sys.path or other runtime state.
func restoreTempDirectories(checkpointPath string) error {
	manifestPath := filepath.Join(checkpointPath, "temp-directories.txt")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No temp-directories.txt found, skipping")
			return nil // No temp directories to restore
		}
		return err
	}

	fmt.Printf("Found temp-directories.txt with %d bytes\n", len(data))

	count := 0
	for _, line := range splitLines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fmt.Printf("  Creating temp dir: %s\n", line)

		// Create directory if it doesn't exist
		if err := os.MkdirAll(line, 0o755); err != nil {
			fmt.Printf("    ERROR: Could not create temp dir %s: %v\n", line, err)
			continue
		}

		// Verify it was created
		if info, err := os.Stat(line); err != nil {
			fmt.Printf("    WARNING: Created but can't stat %s: %v\n", line, err)
		} else {
			fmt.Printf("    OK: Created %s (mode=%v)\n", line, info.Mode())
		}
		count++
	}

	if count > 0 {
		fmt.Printf("Recreated %d temp directories\n", count)
	}

	// List /tmp to verify
	fmt.Println("Contents of /tmp after creating temp dirs:")
	entries, _ := os.ReadDir("/tmp")
	for _, e := range entries {
		fmt.Printf("  %s\n", e.Name())
	}

	return nil
}

// restoreTempFiles copies temp files saved during checkpoint into place.
func restoreTempFiles(checkpointPath string) error {
	tempFilesDir := filepath.Join(checkpointPath, "temp-files")
	if _, err := os.Stat(tempFilesDir); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No temp-files directory found, skipping")
			return nil
		}
		return err
	}

	fmt.Println("Restoring temp files from checkpoint...")
	count := 0
	err := filepath.Walk(tempFilesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(tempFilesDir, path)
		if err != nil {
			return nil
		}
		destPath := "/" + relPath
		destDir := filepath.Dir(destPath)
		if merr := os.MkdirAll(destDir, 0o755); merr != nil {
			fmt.Printf("  Warning: Cannot create directory %s: %v\n", destDir, merr)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("  Warning: Cannot read %s: %v\n", path, err)
			return nil
		}
		perm := info.Mode().Perm()
		if err := os.WriteFile(destPath, data, perm); err != nil {
			fmt.Printf("  Warning: Cannot write %s: %v\n", destPath, err)
			return nil
		}
		count++
		if count <= 10 || count%50 == 0 {
			fmt.Printf("  Restored temp file: %s\n", destPath)
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("Restored %d temp files\n", count)
	return nil
}

// restoreMappedFiles copies files from checkpoint's mapped-files/ directory
// to their original locations. CRIU saves file-backed memory mappings here
// (like JIT-compiled .so files, triton caches) but needs them at the original
// paths during restore.
func restoreMappedFiles(checkpointPath string) error {
	mappedFilesDir := filepath.Join(checkpointPath, "mapped-files")
	if _, err := os.Stat(mappedFilesDir); err != nil {
		// No mapped-files directory - that's OK
		return nil
	}

	fmt.Println("Restoring mapped files from checkpoint...")
	count := 0

	err := filepath.Walk(mappedFilesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if info.IsDir() {
			return nil // Skip directories (we create them as needed)
		}

		// Get the relative path from mapped-files/
		relPath, err := filepath.Rel(mappedFilesDir, path)
		if err != nil {
			return nil
		}

		// The path structure in mapped-files mirrors the original path
		// e.g., mapped-files/root/.cache/... should go to /root/.cache/...
		destPath := "/" + relPath

		// Create parent directory if needed
		destDir := filepath.Dir(destPath)
		if merr := os.MkdirAll(destDir, 0o755); merr != nil {
			fmt.Printf("  Warning: Cannot create directory %s: %v\n", destDir, merr)
			return nil
		}

		// Copy the file
		srcData, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("  Warning: Cannot read %s: %v\n", path, err)
			return nil
		}

		// Preserve file permissions from source
		perm := info.Mode().Perm()
		if err := os.WriteFile(destPath, srcData, perm); err != nil {
			fmt.Printf("  Warning: Cannot write %s: %v\n", destPath, err)
			return nil
		}

		count++
		if count <= 10 || count%50 == 0 {
			fmt.Printf("  Restored: %s\n", destPath)
		}
		return nil
	})

	fmt.Printf("Restored %d mapped files\n", count)
	return err
}

// restoreOpenFiles copies files from checkpoint's open-files/ directory
// to their original locations. These are files that were open at checkpoint
// time (like log files, cache files) that CRIU expects to exist at restore.
// Unlike mapped files (.so), these are regular files opened via open().
func restoreOpenFiles(checkpointPath string, meta *CheckpointMetadata) error {
	openFilesDir := filepath.Join(checkpointPath, "open-files")
	if _, err := os.Stat(openFilesDir); err != nil {
		// No open-files directory - check if we have file list in metadata
		if meta == nil || len(meta.OpenFiles) == 0 {
			return nil
		}
		// Metadata has files but no directory - create empty files
		fmt.Println("No open-files directory but metadata has file list - creating empty files...")
		for _, f := range meta.OpenFiles {
			destDir := filepath.Dir(f.SourcePath)
			if err := os.MkdirAll(destDir, 0o755); err != nil {
				fmt.Printf("  Warning: Cannot create directory %s: %v\n", destDir, err)
				continue
			}
			// Create empty file
			if err := os.WriteFile(f.SourcePath, []byte{}, 0o644); err != nil {
				fmt.Printf("  Warning: Cannot create %s: %v\n", f.SourcePath, err)
			} else {
				fmt.Printf("  Created (empty): %s\n", f.SourcePath)
			}
		}
		return nil
	}

	fmt.Println("Restoring open files (logs, caches) from checkpoint...")
	count := 0

	err := filepath.Walk(openFilesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if info.IsDir() {
			return nil // Skip directories (we create them as needed)
		}

		// Get the relative path from open-files/
		relPath, err := filepath.Rel(openFilesDir, path)
		if err != nil {
			return nil
		}

		// The path structure in open-files mirrors the original path
		// e.g., open-files/root/.cache/... should go to /root/.cache/...
		destPath := "/" + relPath

		// Create parent directory if needed
		destDir := filepath.Dir(destPath)
		if merr := os.MkdirAll(destDir, 0o755); merr != nil {
			fmt.Printf("  Warning: Cannot create directory %s: %v\n", destDir, merr)
			return nil
		}

		// Copy the file
		srcData, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("  Warning: Cannot read %s: %v\n", path, err)
			return nil
		}

		// Preserve file permissions from source
		perm := info.Mode().Perm()
		if err := os.WriteFile(destPath, srcData, perm); err != nil {
			fmt.Printf("  Warning: Cannot write %s: %v\n", destPath, err)
			return nil
		}

		count++
		if count <= 10 || count%50 == 0 {
			fmt.Printf("  Restored: %s\n", destPath)
		}
		return nil
	})

	fmt.Printf("Restored %d open files\n", count)
	return err
}

// discoverNvidiaMajors reads /proc/devices to find all nvidia device major numbers.
// Returns a map of major numbers. Falls back to {195: true} if /proc/devices unreadable.
func discoverNvidiaMajors() map[uint32]bool {
	majors := map[uint32]bool{195: true} // Always include 195 as fallback
	data, err := os.ReadFile("/proc/devices")
	if err != nil {
		return majors
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.Contains(fields[1], "nvidia") {
			continue
		}
		maj, err := strconv.Atoi(fields[0])
		if err != nil || maj < 0 || maj > math.MaxUint32 {
			continue
		}
		majors[uint32(maj)] = true //nolint:gosec // bounded by the maj range check above
	}
	return majors
}

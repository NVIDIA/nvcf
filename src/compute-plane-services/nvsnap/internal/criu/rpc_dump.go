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

package criu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	criu "github.com/balajinvda/go-criu/v8"
	criurpc "github.com/balajinvda/go-criu/v8/rpc"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

// ldLibraryPathOnce ensures we only set LD_LIBRARY_PATH once per process lifetime.
// This prevents unbounded growth when DumpRPC is called multiple times.
var ldLibraryPathOnce sync.Once

// pathOnce ensures we only prepend to PATH once per process lifetime.
var pathOnce sync.Once

// DumpRPCOptions configures a CRIU dump via go-criu RPC.
type DumpRPCOptions struct {
	PID       int
	ImagesDir string
	Root      string // Root filesystem to use for path resolution

	LeaveRunning bool
	ShellJob     bool

	// Plugins / config
	PluginDir string // libdir for CUDA plugin etc.

	// Externalization
	ExtMnt   []ExtMountMap
	External []string
	// Mounts to skip during dump (written to config file because RPC doesn't expose it)
	SkipMounts []string

	// Common flags
	TCPEstab     bool
	TcpClose     bool //nolint:revive // exported name kept for API stability
	FileLocks    bool
	LinkRemap    bool
	ExtUnixSk    bool
	ExtMasters   bool
	SkipFsnotify bool
	SkipInFlight bool
	// Network lock method (e.g. "skip") written to config file because RPC doesn't expose it.
	NetworkLockMethod string
	OrphanPtsMaster   bool

	// AllowUprobes: written to config file because RPC doesn't expose it.
	// Needed on kernels where processes accumulate [uprobes] VMAs (e.g., some
	// OCI CRI-O cluster configurations).
	AllowUprobes bool

	Timeout    uint32
	GhostLimit uint32

	LogLevel int32

	// Stream enables criu-image-streamer for compressed checkpoint.
	// When true, images are piped through lz4 compression.
	// Requires criu-image-streamer and lz4 binaries in the CRIU bundle dir.
	Stream bool
}

// ExtMountMap is a minimal wrapper to avoid importing proto types at call sites.
type ExtMountMap struct {
	Key string
	Val string
}

// DumpRPC performs a CRIU dump using the RPC interface (go-criu).
//
// This is the "clean" path needed for Kubernetes GPU nodes: it supports explicit ExtMnt mappings
// derived from mountinfo (k8s-runc-bypass style), avoiding mount propagation validation failures.
func (m *Manager) DumpRPC(ctx context.Context, opts DumpRPCOptions) error {
	if err := os.MkdirAll(opts.ImagesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create images dir: %w", err)
	}

	imageDir, err := os.Open(opts.ImagesDir)
	if err != nil {
		return fmt.Errorf("failed to open images dir: %w", err)
	}
	defer func() { _ = imageDir.Close() }()
	// Ensure images dir fd is inherited by CRIU child process.
	if _, err := unix.FcntlInt(imageDir.Fd(), unix.F_SETFD, 0); err != nil {
		return fmt.Errorf("failed to clear CLOEXEC on images dir fd: %w", err)
	}

	// Write a CRIU config file for options not reliably supported via RPC.
	// Keep this minimal to reduce the chance of CRIU swrk crashes; add only plugin libdir here.
	cfgPath := filepath.Join(opts.ImagesDir, "criu.conf")
	cfgLines := []string{}
	if opts.PluginDir != "" {
		cfgLines = append(cfgLines, fmt.Sprintf("libdir %s", opts.PluginDir))
	}
	for _, mnt := range opts.SkipMounts {
		mnt = strings.TrimSpace(mnt)
		if mnt == "" {
			continue
		}
		cfgLines = append(cfgLines, fmt.Sprintf("skip-mnt %s", mnt))
	}
	for _, ext := range opts.External {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		cfgLines = append(cfgLines, fmt.Sprintf("external %s", ext))
	}
	if opts.AllowUprobes {
		cfgLines = append(cfgLines, "allow-uprobes")
	}
	if len(cfgLines) > 0 {
		cfgContent := ""
		for _, l := range cfgLines {
			cfgContent += l + "\n"
		}
		if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
			return fmt.Errorf("failed to write CRIU config file: %w", err)
		}
	}

	cgMode := criurpc.CriuCgMode_IGNORE
	criuOpts := &criurpc.CriuOpts{
		Pid:               proto.Int32(int32(opts.PID)), //nolint:gosec // PID is bounded by kernel PID_MAX, fits in int32
		ImagesDirFd:       proto.Int32(int32(imageDir.Fd())),
		LogLevel:          proto.Int32(maxI32(opts.LogLevel, 4)),
		LogFile:           proto.String("dump.log"),
		ManageCgroups:     proto.Bool(true),
		ManageCgroupsMode: &cgMode,
		ShellJob:          proto.Bool(opts.ShellJob),
		LeaveRunning:      proto.Bool(opts.LeaveRunning),

		TcpEstablished: proto.Bool(opts.TCPEstab),
		TcpClose:       proto.Bool(opts.TcpClose),
		FileLocks:      proto.Bool(opts.FileLocks),
		LinkRemap:      proto.Bool(opts.LinkRemap),

		ExtUnixSk:       proto.Bool(opts.ExtUnixSk),
		ExtMasters:      proto.Bool(opts.ExtMasters),
		OrphanPtsMaster: proto.Bool(opts.OrphanPtsMaster),

		TcpSkipInFlight: proto.Bool(opts.SkipInFlight),
	}

	if opts.Root != "" {
		criuOpts.Root = proto.String(opts.Root)
	}
	if len(opts.External) > 0 {
		criuOpts.External = opts.External
	}
	if opts.Timeout > 0 {
		criuOpts.Timeout = proto.Uint32(opts.Timeout)
	}
	if opts.GhostLimit > 0 {
		criuOpts.GhostLimit = proto.Uint32(opts.GhostLimit)
	}
	if len(opts.ExtMnt) > 0 {
		for _, em := range opts.ExtMnt {
			// CRIU expects Key/Val mountpoints.
			k := em.Key
			v := em.Val
			criuOpts.ExtMnt = append(criuOpts.ExtMnt, &criurpc.ExtMountMap{
				Key: proto.String(k),
				Val: proto.String(v),
			})
		}
	}
	if len(cfgLines) > 0 {
		criuOpts.ConfigFile = proto.String(cfgPath)
	}

	c := criu.MakeCriu()
	// Ensure go-criu uses the CRIU binary we configured for this manager (typically /criu-bundle/criu).
	// If unset, go-criu will fall back to resolving "criu" from PATH, which may not exist in the agent image.
	if m.criuPath != "" {
		c.SetCriuPath(m.criuPath)
	}

	// Set LD_LIBRARY_PATH for CUDA plugin (it calls cuda-checkpoint which needs libcuda.so)
	// go-criu spawns "criu swrk" which inherits this environment
	// Include both container paths (/host/...) and host paths for compatibility
	// Use sync.Once to prevent unbounded growth on repeated calls.
	//
	// ORDER MATTERS: the container's own libdir must come FIRST. The driver
	// container tree ships its own libc.so.6; if it precedes the container
	// libdir, any exec'd binary built against a newer glibc dies in the
	// loader ("undefined symbol __tunable_is_initialized" → exit 127; this
	// killed criu swrk). libcuda.so only exists in the driver tree, so it
	// still resolves from there.
	ldLibraryPathOnce.Do(func() {
		ldPath := "/usr/lib/x86_64-linux-gnu:/usr/local/nvidia/lib64:" +
			"/host/run/nvidia/driver/usr/lib/x86_64-linux-gnu:/host/usr/local/nvidia/lib64:" +
			"/run/nvidia/driver/usr/lib/x86_64-linux-gnu"
		if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
			ldPath = ldPath + ":" + existing
		}
		_ = os.Setenv("LD_LIBRARY_PATH", ldPath)
	})

	// Add CRIU binary directory to PATH so CRIU's swrk process can find
	// cuda-checkpoint (co-located with CRIU in /criu-bundle/).
	// Without this, the CUDA plugin falls back to "external GPU checkpoint"
	// mode and doesn't track per-process CUDA contexts during dump.
	pathOnce.Do(func() {
		if m.criuPath != "" {
			criuDir := filepath.Dir(m.criuPath)
			if existing := os.Getenv("PATH"); existing != "" {
				_ = os.Setenv("PATH", criuDir+":"+existing)
			} else {
				_ = os.Setenv("PATH", criuDir)
			}
		}
	})

	// Enable CRIU streaming mode if requested.
	// The actual streamer (internal/streamer) is started by the caller.
	if opts.Stream {
		criuOpts.Stream = proto.Bool(true)
	}

	// go-criu doesn't accept context; rely on CRIU timeout for now.
	if err := c.Dump(criuOpts, nil); err != nil {
		return fmt.Errorf("CRIU RPC dump failed: %w", err)
	}
	return nil
}

// startDumpStreamer starts the criu-image-streamer capture | lz4 pipeline.
// Returns a cleanup function that waits for the pipeline to finish.
func startDumpStreamer(imagesDir, bundleDir string) (cleanup func(), err error) {
	streamerBin := filepath.Join(bundleDir, "criu-image-streamer")
	lz4Bin := filepath.Join(bundleDir, "lz4")

	for _, bin := range []string{streamerBin, lz4Bin} {
		if _, statErr := os.Stat(bin); os.IsNotExist(statErr) {
			fmt.Printf("Streaming binary not found: %s, falling back to non-streaming dump\n", bin)
			return nil, nil
		}
	}

	streamFile := filepath.Join(imagesDir, "stream.lz4")
	outFile, err := os.Create(streamFile)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s: %w", streamFile, err)
	}

	// Pipeline: criu-image-streamer --images-dir DIR capture | lz4 > stream.lz4
	streamerCmd := exec.Command(streamerBin, "--images-dir", imagesDir, "capture") //nolint:gosec // bin paths are internally constructed from bundleDir, not user input
	lz4Cmd := exec.Command(lz4Bin, "-1", "-")                                      //nolint:gosec // bin paths are internally constructed from bundleDir, not user input

	// Clear LD_PRELOAD so intercept library doesn't load into streamer/lz4
	cleanEnv := filterEnv(os.Environ(), "LD_PRELOAD")
	streamerCmd.Env = cleanEnv
	lz4Cmd.Env = cleanEnv

	// Connect streamer stdout → lz4 stdin
	pipe, err := streamerCmd.StdoutPipe()
	if err != nil {
		_ = outFile.Close()
		return nil, fmt.Errorf("failed to create streamer stdout pipe: %w", err)
	}
	lz4Cmd.Stdin = pipe
	lz4Cmd.Stdout = outFile
	lz4Cmd.Stderr = os.Stderr
	streamerCmd.Stderr = os.Stderr

	if err := streamerCmd.Start(); err != nil {
		_ = outFile.Close()
		return nil, fmt.Errorf("failed to start criu-image-streamer: %w", err)
	}
	if err := lz4Cmd.Start(); err != nil {
		_ = streamerCmd.Process.Kill()
		_ = streamerCmd.Wait()
		_ = outFile.Close()
		return nil, fmt.Errorf("failed to start lz4: %w", err)
	}

	// Wait for the capture socket to appear
	socketPath := filepath.Join(imagesDir, "streamer-capture.sock")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		_ = streamerCmd.Process.Kill()
		_ = lz4Cmd.Process.Kill()
		_ = streamerCmd.Wait()
		_ = lz4Cmd.Wait()
		_ = outFile.Close()
		return nil, fmt.Errorf("streamer capture socket did not appear: %s", socketPath)
	}

	fmt.Printf("Dump streamer pipeline started (streamer pid=%d, lz4 pid=%d)\n",
		streamerCmd.Process.Pid, lz4Cmd.Process.Pid)

	cleanup = func() {
		// Kill streamer/lz4 if still running (CRIU may not have connected)
		done := make(chan struct{})
		go func() {
			_ = streamerCmd.Wait()
			_ = lz4Cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			fmt.Println("Streamer pipeline did not exit in 10s, killing...")
			_ = streamerCmd.Process.Kill()
			_ = lz4Cmd.Process.Kill()
			<-done
		}
		_ = outFile.Close()
		_ = os.Remove(socketPath)
		if fi, err := os.Stat(streamFile); err == nil {
			fmt.Printf("Compressed checkpoint: %s (%.1f GB)\n", streamFile, float64(fi.Size())/(1024*1024*1024))
		}
	}
	return cleanup, nil
}

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

func maxI32(v, def int32) int32 {
	if v == 0 {
		return def
	}
	return v
}

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

// Package criu wraps CRIU (Checkpoint/Restore In Userspace) dump and restore
// operations, including the cuda-checkpoint plugin integration used by NVSNAP.
package criu

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

// Manager handles CRIU operations
// When running in a container (agent pod), CRIU runs directly but targets host PIDs
// because the agent runs with hostPID=true
type Manager struct {
	criuPath    string
	inContainer bool
	log         *logrus.Logger
}

// New creates a new CRIU manager
// criuPath: path to CRIU binary (inside the agent container, e.g., /app/criu-wrapper)
// inContainer: true if running as containerized agent
func New(log *logrus.Logger, criuPath string, inContainer bool) (*Manager, error) {
	if criuPath == "" {
		criuPath = "/app/criu-wrapper"
	}

	manager := &Manager{
		criuPath:    criuPath,
		log:         log,
		inContainer: inContainer,
	}

	// Check CRIU version
	cmd := exec.Command(criuPath, "--version")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get CRIU version: %w (path: %s)", err, criuPath)
	}

	version := strings.TrimSpace(string(output))
	log.WithFields(logrus.Fields{
		"version":     version,
		"path":        criuPath,
		"inContainer": inContainer,
	}).Info("CRIU initialized")

	return manager, nil
}

// DumpOptions configures a process dump
type DumpOptions struct {
	PID          int
	ImagesDir    string
	LeaveRunning bool
	ShellJob     bool
	External     []string // External resources like "net[]"
	LogLevel     int
	Timeout      int // --timeout: seconds to wait for tasks (default 10, use 1200 for vLLM)

	// Plugin directory for CUDA plugin
	PluginDir string // --libdir: directory containing CRIU plugins

	// TCP options
	TCPEstab bool // --tcp-established: checkpoint established TCP connections
	TcpClose bool //nolint:revive // exported name kept for API stability (--tcp-close)

	// Filesystem options
	LinkRemap bool // --link-remap: allow link remaps for deleted files
	FileLocks bool // --file-locks: dump file locks

	// Socket options
	SkipUnixSockets bool // --skip-unix-sockets: skip unix domain sockets

	// Network options
	NetworkLockMethod string // --network-lock: skip, iptables, nftables

	// Standard CRIU options for portable checkpoints
	SkipFsnotify     bool     // --skip-fsnotify: skip inotify/fanotify
	SkipInFlight     bool     // --skip-in-flight: skip in-flight io_uring operations
	EnableExtMasters bool     // --enable-external-masters: handle mounts with master propagation (NVIDIA)
	SkipMounts       []string // --skip-mnt: mountpoints to skip during dump

	// AllowUprobes: dump processes even if they have [uprobes] VMAs. Some
	// clusters (e.g., OCI CRI-O nodes with kernel-level observability) attach
	// uprobes to running processes. Without this flag, CRIU aborts with
	// "PID N has uprobes vma. Consider using --allow-uprobes."
	// Safe to always enable: CRIU simply skips the uprobe VMA during dump.
	AllowUprobes bool
}

// Dump checkpoints a process
func (m *Manager) Dump(ctx context.Context, opts DumpOptions) error {
	if err := os.MkdirAll(opts.ImagesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create images dir: %w", err)
	}

	args := []string{
		"dump",
		"-t", strconv.Itoa(opts.PID),
		"-D", opts.ImagesDir,
		"--log-file", "dump.log",
	}

	if opts.LogLevel > 0 {
		args = append(args, fmt.Sprintf("-v%d", opts.LogLevel))
	} else {
		args = append(args, "-v4")
	}

	// Timeout for collecting tasks - important for GPU workloads with cuda-checkpoint
	// Default is 10 seconds which is too short for vLLM; use 1200 (20 min) for large models
	if opts.Timeout > 0 {
		args = append(args, "--timeout", strconv.Itoa(opts.Timeout))
	}

	if opts.LeaveRunning {
		args = append(args, "-R") // --leave-running
	}
	if opts.ShellJob {
		args = append(args, "--shell-job")
	}
	if opts.TCPEstab {
		args = append(args, "--tcp-established")
	}
	if opts.TcpClose {
		args = append(args, "--tcp-close")
	}
	if opts.LinkRemap {
		args = append(args, "--link-remap")
	}
	if opts.FileLocks {
		args = append(args, "--file-locks")
	}
	if opts.SkipUnixSockets {
		args = append(args, "--skip-unix-sockets")
	}
	if opts.NetworkLockMethod != "" {
		args = append(args, "--network-lock", opts.NetworkLockMethod)
	}

	// External resources
	for _, ext := range opts.External {
		args = append(args, "--external", ext)
	}

	// Plugin directory for CUDA support
	if opts.PluginDir != "" {
		args = append(args, "--libdir", opts.PluginDir)
	}

	// Standard CRIU options for portable checkpoints
	if opts.SkipFsnotify {
		args = append(args, "--skip-fsnotify")
	}
	if opts.SkipInFlight {
		args = append(args, "--skip-in-flight")
	}
	if opts.EnableExtMasters {
		args = append(args, "--enable-external-masters")
	}
	for _, mnt := range opts.SkipMounts {
		args = append(args, "--skip-mnt", mnt)
	}
	if opts.AllowUprobes {
		args = append(args, "--allow-uprobes")
	}

	m.log.WithFields(logrus.Fields{
		"pid":       opts.PID,
		"imagesDir": opts.ImagesDir,
		"cmd":       m.criuPath + " " + strings.Join(args, " "),
	}).Info("Running CRIU dump")

	cmd := exec.CommandContext(ctx, m.criuPath, args...) //nolint:gosec // args are internally constructed, not user input

	// Pass LD_LIBRARY_PATH for CUDA plugin (it calls cuda-checkpoint which needs libcuda.so)
	// When running in container: use /host/run paths (mounted from host)
	// On bare metal: use /run paths directly
	cmd.Env = os.Environ()
	// ORDER MATTERS: container libdir first — the driver tree ships its own
	// libc.so.6 and putting it first kills newer-glibc binaries in the loader
	// (exit 127). libcuda.so only exists in the driver tree so it still resolves.
	if m.inContainer {
		// Container has /host/run mounted from host's /run
		cmd.Env = append(cmd.Env, "LD_LIBRARY_PATH=/usr/lib/x86_64-linux-gnu:/usr/local/nvidia/lib64:/host/run/nvidia/driver/usr/lib/x86_64-linux-gnu")
	} else {
		cmd.Env = append(cmd.Env, "LD_LIBRARY_PATH=/usr/lib/x86_64-linux-gnu:/usr/local/nvidia/lib64:/run/nvidia/driver/usr/lib/x86_64-linux-gnu")
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Read log file for details
		logPath := filepath.Join(opts.ImagesDir, "dump.log")
		logContent, _ := os.ReadFile(logPath)
		return fmt.Errorf("CRIU dump failed: %w\nstderr: %s\nlog:\n%s",
			err, stderr.String(), string(logContent))
	}

	m.log.WithField("imagesDir", opts.ImagesDir).Info("CRIU dump completed")
	return nil
}

// RestoreOptions configures a process restore
type RestoreOptions struct {
	ImagesDir      string
	RootFS         string // --root: use this as filesystem root
	ShellJob       bool
	Detached       bool
	PidFile        string
	JoinNamespace  map[string]string // --join-ns type:path
	EmptyNs        []string          // --empty-ns: create empty namespace
	ManageCgroups  string            // --manage-cgroups: ignore, none, etc.
	TcpClose       bool              //nolint:revive // exported name kept for API stability (--tcp-close)
	TcpEstablished bool              //nolint:revive // exported name kept for API stability (--tcp-established)
	LogLevel       int
	PluginDir      string // --libdir: directory containing CRIU plugins

	// Standard CRIU options
	MntnsCompatMode bool // --mntns-compat-mode: portable mount namespace handling

	// ExtMounts pairs external mount keys (recorded during dump) with the
	// destination mountpoint to bind against. Emitted as `--ext-mount-map
	// KEY:VAL` per entry. Must match the dump-time `--external mnt[KEY]:...`
	// / ExtMountMap entries for CRIU to restore the mount table correctly.
	// The ExtMountMap type is defined in rpc_dump.go.
	ExtMounts []ExtMountMap

	// InheritFds provides open file descriptors to CRIU to satisfy
	// `--external` keys recorded at dump time (e.g., netns referenced via
	// `--external net[]`, default key "extNetNs"). Paths are opened by the
	// agent; CRIU gets them via ExtraFiles. Emitted as
	// `--inherit-fd fd[N]:KEY` where N is the child-side fd number.
	InheritFds []InheritFd

	// InheritFiles: pre-opened *os.File handles to pass to CRIU as
	// inherit-fd resources. Use this when the file isn't path-addressable
	// (e.g., the write end of a pipe pair the caller created for
	// post-restore stdout/stderr streaming). Emitted after InheritFds so
	// fd numbering is path-fds first, then file-fds.
	InheritFiles []InheritFile

	// AllowUprobes: must be set at restore time to match the dump-side flag.
	// CRIU refuses to restore an image made with --allow-uprobes unless this
	// is set, emitting "Dumped with --allow-uprobes. Need to set it on
	// restore as well." Safe to always set.
	AllowUprobes bool

	// PlaceholderMntnsPID: when > 0, run CRIU inside that PID's mount
	// namespace via the `nvsnap-agent restore-helper` subcommand. The helper
	// grafts the CRIU bundle and checkpoints dir into the placeholder's
	// mntns (open_tree + setns + move_mount), then execs CRIU there so
	// --root=/ resolves to the placeholder's rootfs with submounts intact.
	PlaceholderMntnsPID int

	// HelperExecPath: path to the nvsnap-restore-helper binary. Defaults to
	// /criu-bundle/nvsnap-restore-helper when empty. The helper must be a
	// single-threaded C binary because setns(CLONE_NEWNS) refuses
	// multi-threaded callers — Go is multi-threaded by default.
	HelperExecPath string

	// HelperBundlePath: host-mntns path to the CRIU bundle directory (the
	// one containing the criu binary, plugins/, lib/). Grafted into the
	// placeholder as /tmp/nvsnap-bundle by the helper.
	HelperBundlePath string

	// HelperCheckpointRoot: host-mntns path to the root of the checkpoints
	// store (the parent of the per-checkpoint-id directories). Grafted
	// into the placeholder as /tmp/nvsnap-checkpoints by the helper.
	HelperCheckpointRoot string
}

// InheritFd is a file-descriptor-based external resource mapping for CRIU
// restore. Path is opened (read-only), passed to CRIU's subprocess via the
// ExtraFiles mechanism, and referenced in CRIU's --inherit-fd flag by Key.
//
// Example: for a dump that used `--external net[]` (netns saved as external
// with default key "extNetNs"), pass:
//
//	InheritFds: []InheritFd{{Path: "/proc/1234/ns/net", Key: "extNetNs"}}
type InheritFd struct {
	Path string
	Key  string
}

// InheritFile is a pre-opened *os.File-based external resource mapping
// for CRIU restore. Same shape as InheritFd but the caller provides an
// already-open file (e.g., the write end of a freshly-created pipe). The
// file is passed to CRIU's subprocess via ExtraFiles and referenced in
// CRIU's --inherit-fd flag by Key.
type InheritFile struct {
	File *os.File
	Key  string
}

// Restore restores a checkpointed process
func (m *Manager) Restore(opts RestoreOptions) (int, error) {
	// Ensure pid file directory exists
	if opts.PidFile == "" {
		opts.PidFile = filepath.Join(opts.ImagesDir, "restore.pid")
	}
	if err := os.MkdirAll(filepath.Dir(opts.PidFile), 0o755); err != nil {
		return 0, fmt.Errorf("failed to create pidfile dir: %w", err)
	}

	// The helper grafts bundle/checkpoints into the placeholder's mntns at
	// the SAME paths the agent uses, so paths embedded in CRIU args (and
	// CRIU's own RPATH) resolve without translation.
	if opts.PlaceholderMntnsPID > 0 {
		if opts.HelperBundlePath == "" || opts.HelperCheckpointRoot == "" {
			return 0, fmt.Errorf("placeholder-mntns restore requires HelperBundlePath and HelperCheckpointRoot")
		}
	}

	args := []string{
		"restore",
		"-D", opts.ImagesDir,
		"--log-file", "restore.log",
		"--pidfile", opts.PidFile,
	}

	if opts.LogLevel > 0 {
		args = append(args, fmt.Sprintf("-v%d", opts.LogLevel))
	} else {
		args = append(args, "-v4")
	}

	if opts.ShellJob {
		args = append(args, "--shell-job")
	}
	if opts.Detached {
		args = append(args, "--restore-detach")
	}
	if opts.RootFS != "" {
		args = append(args, "--root", opts.RootFS)
	}
	if opts.TcpClose {
		args = append(args, "--tcp-close")
	}
	if opts.TcpEstablished {
		args = append(args, "--tcp-established")
	}
	if opts.ManageCgroups != "" {
		args = append(args, "--manage-cgroups="+opts.ManageCgroups)
	}
	if opts.PluginDir != "" {
		args = append(args, "--libdir", opts.PluginDir)
	}

	// Empty namespaces
	for _, ns := range opts.EmptyNs {
		args = append(args, "--empty-ns", ns)
	}

	// Join namespaces
	for nsType, nsPath := range opts.JoinNamespace {
		args = append(args, "--join-ns", nsType+":"+nsPath)
	}

	// Standard CRIU options
	if opts.MntnsCompatMode {
		args = append(args, "--mntns-compat-mode")
	}
	if opts.AllowUprobes {
		args = append(args, "--allow-uprobes")
	}

	// External mount mappings: must match what `criu dump --external mnt[K]:V`
	// recorded. For NVSNAP's symmetric Plan A (Key=Val=mountpoint), this replays
	// each mount from the checkpoint against the same path in the restored
	// namespace — works whether the placeholder's mount IDs match or not.
	for _, em := range opts.ExtMounts {
		args = append(args, "--ext-mount-map", em.Key+":"+em.Val)
	}

	// External resources by fd: open each requested path and pass as
	// ExtraFiles to CRIU. Go's exec assigns them fd 3, 4, 5, ... in the
	// child. Emit matching --inherit-fd flags.
	inheritedFiles := make([]*os.File, 0, len(opts.InheritFds)+len(opts.InheritFiles))
	pathOpened := make([]*os.File, 0, len(opts.InheritFds)) // close after exec; caller-supplied InheritFiles closed by caller
	defer func() {
		for _, f := range pathOpened {
			_ = f.Close()
		}
	}()
	for i, inh := range opts.InheritFds {
		f, err := os.Open(inh.Path)
		if err != nil {
			return 0, fmt.Errorf("open inherit-fd path %s: %w", inh.Path, err)
		}
		inheritedFiles = append(inheritedFiles, f)
		pathOpened = append(pathOpened, f)
		childFd := 3 + i // exec.Cmd.ExtraFiles starts at fd 3
		args = append(args, "--inherit-fd", fmt.Sprintf("fd[%d]:%s", childFd, inh.Key))
	}
	// Pre-opened files (e.g., write ends of pipes for stdout/stderr
	// streaming). Caller owns lifecycle of these files. They get child
	// fd numbers immediately after InheritFds.
	pathFdCount := len(opts.InheritFds)
	for i, inh := range opts.InheritFiles {
		if inh.File == nil {
			continue
		}
		inheritedFiles = append(inheritedFiles, inh.File)
		childFd := 3 + pathFdCount + i
		args = append(args, "--inherit-fd", fmt.Sprintf("fd[%d]:%s", childFd, inh.Key))
	}

	// Build the command. Placeholder-mntns restore wraps CRIU in the
	// restore-helper subcommand: helper inherits the agent's mntns, does
	// open_tree on bundle + checkpoints, setns into placeholder's mntns,
	// move_mount the detached trees into /tmp/nvsnap-bundle and
	// /tmp/nvsnap-checkpoints, then execs CRIU with the translated paths.
	var cmd *exec.Cmd
	if opts.PlaceholderMntnsPID > 0 {
		helperExec := opts.HelperExecPath
		if helperExec == "" {
			helperExec = "/criu-bundle/nvsnap-restore-helper"
		}
		helperArgs := []string{
			fmt.Sprintf("--placeholder-pid=%d", opts.PlaceholderMntnsPID),
			fmt.Sprintf("--bundle-src=%s", opts.HelperBundlePath),
			fmt.Sprintf("--checkpoints-src=%s", opts.HelperCheckpointRoot),
			"--",
			m.criuPath,
		}
		helperArgs = append(helperArgs, args...)
		cmd = exec.Command(helperExec, helperArgs...) //nolint:gosec // args are internally constructed, not user input
		m.log.WithFields(logrus.Fields{
			"imagesDir":      opts.ImagesDir,
			"placeholderPid": opts.PlaceholderMntnsPID,
			"cmd":            helperExec + " " + strings.Join(helperArgs, " "),
		}).Info("Running CRIU restore via placeholder-mntns helper")
	} else {
		cmd = exec.Command(m.criuPath, args...) //nolint:gosec // args are internally constructed, not user input
		m.log.WithFields(logrus.Fields{
			"imagesDir": opts.ImagesDir,
			"rootfs":    opts.RootFS,
			"cmd":       m.criuPath + " " + strings.Join(args, " "),
		}).Info("Running CRIU restore")
	}
	if len(inheritedFiles) > 0 {
		cmd.ExtraFiles = inheritedFiles
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logPath := filepath.Join(opts.ImagesDir, "restore.log")
		logContent, _ := os.ReadFile(logPath)
		return 0, fmt.Errorf("CRIU restore failed: %w\nstderr: %s\nlog:\n%s",
			err, stderr.String(), string(logContent))
	}

	// Read restored PID. When run via the helper, the pidfile path inside
	// the placeholder mntns refers to the same hostPath inode the agent sees.
	pidBytes, err := os.ReadFile(opts.PidFile)
	if err != nil {
		return 0, fmt.Errorf("failed to read pid file: %w", err)
	}

	var pid int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &pid)

	m.log.WithField("pid", pid).Info("CRIU restore completed")
	return pid, nil
}

// Check verifies CRIU can run
func (m *Manager) Check(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, m.criuPath, "check") //nolint:gosec // criuPath is internally configured, not user input
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("CRIU check failed: %w\n%s", err, string(output))
	}
	return nil
}

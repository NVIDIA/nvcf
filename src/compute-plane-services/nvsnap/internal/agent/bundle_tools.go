// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// The tools a capture or restore runs inside a workload's mount namespace
// (CRIU, nvsnap-gpu-suspend, tar, mkdir, the pid-namespace restore helper)
// run as root, from the node bundle the webhook mounts read-only at
// v2BinDirInContainer: a version under <bundle root>/versions, staged by the
// agent DaemonSet and never changed once staged
// (docs/design/VERSIONED-NODE-BUNDLE.md). Nothing is copied into the
// workload's filesystem, which the workload can write. Before a tool runs,
// the agent checks that the path is that mount: a read-only mount of one of
// the node's versions, in a workload that cannot change its mounts.

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// bundleTools is the node bundle as a workload's mount namespace has it.
type bundleTools struct {
	// pid is a process in that mount namespace (host pid).
	pid int
	// version is the bundle version mounted there.
	version string
}

// capSysAdmin is CAP_SYS_ADMIN's bit in /proc/<pid>/status's CapEff.
const capSysAdmin = 21

// bundleToolsOf checks that host pid's mount namespace has the node bundle
// at v2BinDirInContainer, and returns it. versionsDir holds the node's
// staged versions.
func bundleToolsOf(procBase string, pid int, versionsDir string) (bundleTools, error) {
	base := filepath.Join(procBase, strconv.Itoa(pid))
	if admin, err := hasCapability(filepath.Join(base, "status"), capSysAdmin); err != nil {
		return bundleTools{}, err
	} else if admin {
		return bundleTools{}, fmt.Errorf("the workload holds CAP_SYS_ADMIN and can change its mounts, so the tools mounted at %s cannot be trusted there; a privileged workload is not captured or restored", v2BinDirInContainer)
	}
	ro, mounted := false, false
	for _, m := range readMountinfo(filepath.Join(base, "mountinfo")) {
		if m.point == v2BinDirInContainer {
			mounted, ro = true, m.readOnly // the last one mounted there is the one seen
		}
	}
	if !mounted {
		return bundleTools{}, fmt.Errorf("the pod has no node bundle mounted at %s; the webhook mounts it into pods admitted for capture and restore (recreate a pod admitted before)", v2BinDirInContainer)
	}
	if !ro {
		return bundleTools{}, fmt.Errorf("the node bundle at %s is not mounted read-only", v2BinDirInContainer)
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(filepath.Join(base, "root", strings.TrimPrefix(v2BinDirInContainer, "/")), &st); err != nil {
		return bundleTools{}, fmt.Errorf("stat %s in the workload: %w", v2BinDirInContainer, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return bundleTools{}, fmt.Errorf("%s in the workload is not a directory", v2BinDirInContainer)
	}
	version, err := bundleVersionOf(versionsDir, st.Dev, st.Ino)
	if err != nil {
		return bundleTools{}, err
	}
	return bundleTools{pid: pid, version: version}, nil
}

// bundleVersionOf names the staged version whose directory is dev:ino.
func bundleVersionOf(versionsDir string, dev, ino uint64) (string, error) {
	ents, err := os.ReadDir(versionsDir)
	if err != nil {
		return "", fmt.Errorf("read the node's bundle versions: %w", err)
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		var st syscall.Stat_t
		if syscall.Lstat(filepath.Join(versionsDir, e.Name()), &st) == nil && st.Dev == dev && st.Ino == ino {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("the directory mounted at %s is not one of the node's bundle versions", v2BinDirInContainer)
}

// hasCapability reports whether the effective capabilities in a
// /proc/<pid>/status file include bit.
func hasCapability(statusPath string, bit uint) (bool, error) {
	f, err := os.Open(statusPath) //nolint:gosec // a /proc status file
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "CapEff:"); ok {
			caps, err := strconv.ParseUint(strings.TrimSpace(rest), 16, 64)
			if err != nil {
				return false, fmt.Errorf("%s: CapEff: %w", statusPath, err)
			}
			return caps&(1<<bit) != 0, nil
		}
	}
	if err := sc.Err(); err != nil {
		return false, err
	}
	return false, fmt.Errorf("%s has no CapEff", statusPath)
}

// path is where tool runs inside the mount namespace.
func (t bundleTools) path(tool string) string { return v2BinDirInContainer + "/" + tool }

// has reports whether the mounted version carries tool.
func (t bundleTools) has(procBase, tool string) bool {
	return fileExists(filepath.Join(procBase, strconv.Itoa(t.pid), "root", strings.TrimPrefix(t.path(tool), "/")))
}

// pluginDir is where the mounted version keeps CRIU's plugins.
func (t bundleTools) pluginDir(procBase string) string {
	for _, d := range []string{"plugins/cuda", "plugins"} {
		if fileExists(filepath.Join(procBase, strconv.Itoa(t.pid), "root", strings.TrimPrefix(v2BinDirInContainer, "/"), d)) {
			return v2BinDirInContainer + "/" + d
		}
	}
	return v2BinDirInContainer + "/plugins"
}

// usageCache holds each version's tool usage text: a version never changes.
var usageCache sync.Map

// usage is tool's usage text in the mounted version (with arg, its help
// flag; without, the text a tool prints when given no action), read once
// per version.
func (t bundleTools) usage(tool string, arg ...string) string {
	key := t.version + "\x00" + tool
	if v, ok := usageCache.Load(key); ok {
		return v.(string)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := append([]string{"-t", strconv.Itoa(t.pid), "-m", "--", t.path(tool)}, arg...)
	out, _ := exec.CommandContext(ctx, "nsenter", args...).CombinedOutput() //nolint:gosec // a bundle tool at a fixed path
	if ctx.Err() != nil {
		return string(out) // not cached: a timeout says nothing about the version
	}
	usageCache.Store(key, string(out))
	return string(out)
}

// criuHelp is the mounted CRIU's --help.
func (t bundleTools) criuHelp() string { return t.usage("criu", "--help") }

func (t bundleTools) directImageIO() bool { return criuSupportsDirectImageIO(t.criuHelp()) }

func (t bundleTools) netMigration() bool { return criuSupportsNetMigration(t.criuHelp()) }

func (t bundleTools) timensBudget() bool { return criuSupportsTimensBudget(t.criuHelp()) }

// compressBlockFlag is the mounted CRIU's name for block compression.
func (t bundleTools) compressBlockFlag() string { return criuCompressBlockFlag(t.criuHelp()) }

// clockThroughGPU reports whether a restore with these tools keeps the GPU
// reload out of the restored clocks (restore_clock_gpu.go).
func (t bundleTools) clockThroughGPU() bool {
	return clockThroughGPUSupported(t.criuHelp(), t.usage(gpushareToolName))
}

// bundlePATH is the PATH a bundle tool runs with inside a workload: the
// bundle first. CRIU's CUDA plugin runs cuda-checkpoint from PATH, and
// in-container/ holds it without the wrapper the agent's own container
// needs (the workload's driver libraries are already in its loader path).
const bundlePATH = v2BinDirInContainer + "/in-container:" + v2BinDirInContainer + ":/usr/sbin:/usr/bin:/sbin:/bin"

var (
	agentCRIUHelpOnce sync.Once
	agentCRIUHelpText string
)

// agentCRIUHelp is the --help of the CRIU in the agent's own container,
// which a pid-namespace dump runs from the host side.
func (a *Agent) agentCRIUHelp() string {
	agentCRIUHelpOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, a.config.CRIUPath, "--help").CombinedOutput() //nolint:gosec // the agent's own CRIU
		agentCRIUHelpText = string(out)
	})
	return agentCRIUHelpText
}

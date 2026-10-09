// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Capture and restore of a workload that runs as its container's pid 1, the
// normal shape of a container (`exec vllm serve`, or a shell that runs the
// engine and waits). CRIU records a pid namespace only when it runs outside
// it, so the in-namespace dump of such a tree cannot be restored into the
// placeholder's existing namespace:
//
//	Error (criu/cr-restore.c:2104): This process tree can only be restored
//	in a new pid namespace.
//
// So pid 1 is dumped from the agent's namespaces with --root, the container's
// network namespace and every mount CRIU cannot rebuild itself declared
// external. The restore runs inside the placeholder: CRIU creates a nested
// pid namespace for the tree (whose pid 1 is the workload again) and binds
// each external mount to the same path in the placeholder, which has the
// same pod spec. The placeholder's network namespace is passed in, so the
// workload keeps the pod's address and its probes reach it.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

// pidNSMarkerFile, in the checkpoint directory, marks a checkpoint that
// carries its pid namespace and lists its external mounts.
const pidNSMarkerFile = "pidns-restore.json"

// pidNSCapture is the content of pidNSMarkerFile.
type pidNSCapture struct {
	// Mounts are the mountpoints declared external at dump, each keyed by
	// its own path; restore binds the placeholder's mount at that path.
	Mounts []string `json:"mounts"`
}

// criuNativeFS are filesystems CRIU recreates itself when they are mounted
// at their own root: their contents are virtual or dumped (tmpfs).
var criuNativeFS = map[string]bool{"proc": true, "sysfs": true, "devpts": true, "mqueue": true, "tmpfs": true}

// mountinfoEntry is one line of /proc/<pid>/mountinfo.
type mountinfoEntry struct {
	Dev, Root, MountPoint, FSType string
}

func parseMountinfo(data string) []mountinfoEntry {
	var out []mountinfoEntry
	sc := bufio.NewScanner(strings.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := -1
		for i, w := range f {
			if w == "-" {
				sep = i
				break
			}
		}
		if sep < 5 || sep+1 >= len(f) {
			continue
		}
		out = append(out, mountinfoEntry{Dev: f[2], Root: unescapeMountinfo(f[3]), MountPoint: unescapeMountinfo(f[4]), FSType: f[sep+1]})
	}
	return out
}

// unescapeMountinfo decodes the octal escapes mountinfo uses for space, tab,
// newline and backslash.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// pidNSExternalMounts returns the mountpoints of a container that CRIU must
// treat as external: everything but the root and the filesystems CRIU
// recreates itself. CRIU recreates a proc, sysfs, devpts, mqueue or tmpfs
// mount when that filesystem is mounted at its own root in the container;
// a bind of part of it (the runtime's masks of /proc/kcore and friends,
// bound from /dev's tmpfs) is rebuilt from that mount. A tmpfs bound from
// the node is not: the NVIDIA driver's firmware files on GB300 come from a
// tmpfs on the host, and CRIU refuses them ("doesn't have a proper root
// mount"). Those, the node's bind mounts (kubelet's /etc/hosts, volumes,
// the CDI driver files) and the cgroup mount are external; restore binds
// the placeholder's copy of each.
func pidNSExternalMounts(entries []mountinfoEntry) []string {
	rootOf := map[string]bool{} // devices mounted at their own root here
	for _, e := range entries {
		if e.Root == "/" {
			rootOf[e.Dev] = true
		}
	}
	var out []string
	for _, e := range entries {
		if e.MountPoint == "/" {
			continue
		}
		if criuNativeFS[e.FSType] && rootOf[e.Dev] {
			continue
		}
		out = append(out, e.MountPoint)
	}
	return out
}

// pidNSDumpArgs is the criu dump argv for a pid-1 workload, run from the
// agent's namespaces against the container's host pid.
func pidNSDumpArgs(hostPID int, procBase, imgsDir, pluginDir string, netNSInode uint64, mounts, deviceExternals []string, leaveRunning, gpushare, directIO bool) []string {
	args := []string{
		"dump",
		"-t", strconv.Itoa(hostPID),
		"--root", filepath.Join(procBase, strconv.Itoa(hostPID), "root"),
		"-D", imgsDir,
		"-o", "dump.log", "-v4",
		"--shell-job", "--tcp-established", "--ext-unix-sk",
		"--link-remap", "--ghost-links", "--ghost-limit", "1073741824",
		"--skip-in-flight",
		"--network-lock", "nftables",
		"--manage-cgroups=ignore",
		"--timeout", "1200",
		"--file-locks",
		// The pod's network stays the pod's: restore joins the placeholder's.
		"--external", fmt.Sprintf("net[%d]:%s", netNSInode, pidNSExtNetKey),
	}
	if !gpushare {
		args = append(args, "--libdir", pluginDir)
	}
	if directIO {
		args = append(args, "--image-io-mode", "direct")
	}
	if leaveRunning {
		args = append(args, "--leave-running")
	}
	for _, mp := range mounts {
		args = append(args, "--external", fmt.Sprintf("mnt[%s]:%s", mp, mp))
	}
	for _, e := range deviceExternals {
		args = append(args, "--external", e)
	}
	return args
}

// pidNSExtNetKey names the external network namespace in the images.
const pidNSExtNetKey = "extNetNs"

// pidNSRestoreArgs is the criu restore argv for a checkpoint that carries
// its pid namespace, run inside the placeholder's mount and pid namespaces
// (behind the pidns-restore-exec helper, which hands it the network
// namespace on fd 3).
func pidNSRestoreArgs(imgsInContainer string, mounts []string, gpushare, directIO bool, inetAddrMap string) []string {
	args := []string{
		"restore",
		"-D", imgsInContainer,
		"-o", "restore.log", "-v4",
		"--root", "/",
		"--shell-job", "--tcp-established", "--ext-unix-sk", "--link-remap",
		"--network-lock", "nftables",
		"--manage-cgroups=ignore",
		"--restore-detached",
		"--inherit-fd", "fd[3]:" + pidNSExtNetKey,
		// SysV IPC is the pod's; the hostname stays the source pod's, in a
		// nested UTS namespace (the placeholder's /proc/sys is read-only).
		"--join-ns", "ipc:/proc/1/ns/ipc",
	}
	if !gpushare {
		args = append(args, "--libdir", v2BinDirInContainer)
	}
	if directIO {
		args = append(args, "--image-io-mode", "direct")
	}
	if inetAddrMap != "" {
		args = append(args, "--inet-addr-map", inetAddrMap, "--keep-network-lock")
	}
	for _, mp := range mounts {
		args = append(args, "--external", fmt.Sprintf("mnt[%s]:%s", mp, mp))
	}
	return args
}

func writePIDNSMarker(checkpointDir string, c pidNSCapture) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(checkpointDir, pidNSMarkerFile), b, 0o644)
}

// readPIDNSMarker returns the marker of a checkpoint that carries its pid
// namespace; ok is false for an in-namespace checkpoint.
func readPIDNSMarker(checkpointDir string) (pidNSCapture, bool, error) {
	var c pidNSCapture
	b, err := os.ReadFile(filepath.Join(checkpointDir, pidNSMarkerFile))
	if os.IsNotExist(err) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	return c, true, json.Unmarshal(b, &c)
}

// nestedInitHostPID returns the host pid of the restored tree's pid 1: a
// process in a pid namespace one level below the placeholder's whose pid
// there is 1.
func nestedInitHostPID(procBase string, placeholderHostPID int) (int, error) {
	depth, err := nsPIDDepth(procBase, placeholderHostPID)
	if err != nil {
		return 0, err
	}
	ents, err := os.ReadDir(procBase)
	if err != nil {
		return 0, err
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ns, err := nsPIDs(procBase, pid)
		if err != nil || len(ns) != depth+1 || ns[len(ns)-1] != 1 {
			continue
		}
		// CRIU restores detached, so the restored init is reparented to the
		// placeholder's pid 1.
		if ppid, err := parentPID(procBase, pid); err == nil && ppid == placeholderHostPID {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no restored pid 1 below the pid namespace of %d", placeholderHostPID)
}

func nsPIDs(procBase string, pid int) ([]int, error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "status"))
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		var out []int
		for _, f := range strings.Fields(strings.TrimPrefix(line, "NSpid:")) {
			n, err := strconv.Atoi(f)
			if err != nil {
				return nil, err
			}
			out = append(out, n)
		}
		return out, nil
	}
	return nil, fmt.Errorf("no NSpid in %d/status", pid)
}

func nsPIDDepth(procBase string, pid int) (int, error) {
	ns, err := nsPIDs(procBase, pid)
	return len(ns), err
}

func parentPID(procBase string, pid int) (int, error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			return strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return 0, fmt.Errorf("no PPid in %d/status", pid)
}

// dumpPIDNamespace dumps the container whose pid 1 is hostPID into
// checkpointDir, running the agent's criu from the agent's namespaces, and
// records the external mounts for the restore.
func (a *Agent) dumpPIDNamespace(ctx context.Context, procBase string, hostPID int, checkpointDir string, deviceExternals []string, leaveRunning, gpushare bool, log *logrus.Entry) error {
	mi, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(hostPID), "mountinfo"))
	if err != nil {
		return fmt.Errorf("pid-namespace dump: read mounts: %w", err)
	}
	mounts := pidNSExternalMounts(parseMountinfo(string(mi)))
	var st syscall.Stat_t
	if err := syscall.Stat(filepath.Join(procBase, strconv.Itoa(hostPID), "ns", "net"), &st); err != nil {
		return fmt.Errorf("pid-namespace dump: network namespace: %w", err)
	}
	args := pidNSDumpArgs(hostPID, procBase, checkpointDir, resolveCRIUPluginDir(a.config.CRIUPath, log), st.Ino,
		mounts, deviceExternals, leaveRunning, gpushare, gpushare && bundledSupportsDirectImageIO())
	log.WithFields(logrus.Fields{"argv": a.config.CRIUPath + " " + strings.Join(args, " "), "externalMounts": len(mounts)}).
		Info("criu-v2: dumping the pid namespace from outside the container")
	dctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(dctx, a.config.CRIUPath, args...)
	cmd.Env = []string{"PATH=" + filepath.Dir(a.config.CRIUPath) + ":/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root"}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("criu-v2 pid-namespace dump: %w (output: %s; dump.log tail: %s)", err,
			strings.TrimSpace(string(out)), tailOfFile(filepath.Join(checkpointDir, "dump.log"), 6))
	}
	return writePIDNSMarker(checkpointDir, pidNSCapture{Mounts: mounts})
}

// pidNSRestoreHelperName is the agent binary staged into the placeholder's
// bundle directory to run pidns-restore-exec there.
const pidNSRestoreHelperName = "nvsnap-agent"

// stagePIDNSRestoreHelper copies the agent binary (static) into the
// placeholder's bundle directory.
func (a *Agent) stagePIDNSRestoreHelper(root string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("pid-namespace restore: locate the agent binary: %w", err)
	}
	dst := filepath.Join(root, strings.TrimPrefix(v2BinDirInContainer, "/"), pidNSRestoreHelperName)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("pid-namespace restore: %w", err)
	}
	if err := copyFileExec(self, dst); err != nil {
		return fmt.Errorf("pid-namespace restore: stage the helper: %w", err)
	}
	return nil
}

// readPIDNSMarkerOrFalse reads the marker; a checkpoint without one is an
// in-namespace checkpoint.
func readPIDNSMarkerOrFalse(checkpointDir string) (bool, pidNSCapture, error) {
	c, ok, err := readPIDNSMarker(checkpointDir)
	if err != nil {
		return false, c, fmt.Errorf("read %s: %w", pidNSMarkerFile, err)
	}
	return ok, c, nil
}

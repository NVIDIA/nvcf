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

// criu-v2 capture path: in-namespace CRIU dump.
//
// Instead of running "criu swrk" from the agent's own mount namespace
// (which requires Root/ExtMnt reconstruction, skip-mnt lists, and
// LD_LIBRARY_PATH gymnastics — the failure classes of the legacy path),
// criu-v2 stages a small bundle (criu + cuda_plugin.so + cuda-checkpoint)
// into the target container's rootfs and executes CRIU dump INSIDE the
// container's mnt/pid/net/ipc/uts namespaces via nsenter (full namespace
// fidelity — a partial join makes CRIU serialize the container's ipc/uts
// as foreign namespaces and recreate them isolated at restore, which
// breaks the GPU driver's resume). CRIU then sees the container's mount
// tree natively (no ExtMnt) and binds unix sockets in its own namespace
// on restore (no setns). This removes the whole userspace interception
// stack (libnvsnap_intercept, patched uvloop/libuv/libzmq, sitecustomize,
// restore-entrypoint) that the legacy path injected into every workload.
//
// SCOPE: this does NOT yet restore io_uring / libuv event-loop kernel
// state. vLLM's uvloop aborts post-restore with io_uring enabled, so the
// vllm-small manifest still sets USE_LIBUV=0 / UV_USE_IO_URING=0 and
// preloads nvsnap_cr.so (verified: with those levers removed the restored
// process aborts in uvloop.run, 2026-07-13). Restoring the rings at the
// CRIU layer to drop those levers is tracked separately (NVCF-9641,
// io_uring ring-restore work item).
//
// The images land in <container>/opt/nvsnap-imgs (overlay upperdir) and are
// moved host-side into the standard checkpoint directory afterwards, so
// the artifact layout, metadata, rootfs-diff mirror, and upload contract
// are identical to the legacy path — NVCA and the server never know which
// engine ran. The staged /criu-bundle intentionally stays in the upperdir:
// the rootfs-diff replay delivers it into the restore container for free.

package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/containerd"
)

// CapturePathCRIUV2 is the request value selecting the in-namespace CRIU
// engine. Kept as a plain string to match CheckpointRequest.CapturePath.
const CapturePathCRIUV2 = "criu-v2"

func isCRIUV2(capturePath string) bool {
	// criu-v2 is the only CRIU engine now: the legacy LD_PRELOAD injection
	// path is gone, so a plain "criu" request uses the in-namespace engine
	// too. "rootfs" (multi-GPU / arm) stays on the rootfs path — never route
	// it through dumpV2, which would stamp CapturePath=criu-v2 and make
	// restore call restoreV2 (needs a placeholder + /checkpoints mount that a
	// rootfs capture never has).
	if capturePath != "" {
		return capturePath == CapturePathCRIUV2 || capturePath == "criu"
	}
	// Unspecified capture on a CRIU-default node uses v2; a rootfs-default
	// node redirects to rootfs upstream (RootfsIsDefault) before reaching here.
	return os.Getenv("NVSNAP_CRIU_V2") != "0" && !RootfsIsDefault()
}

// v2ImagesDirInContainer is where CRIU writes images inside the container
// (lands in the overlay upperdir; moved to the checkpoint dir afterwards).
const v2ImagesDirInContainer = "/opt/nvsnap-imgs"

// v2BinDirInContainer is where the criu bundle is staged inside the
// container. It MUST be /criu-bundle: the bundle's criu is built with
// INTERP=/criu-bundle/lib/ld-linux-x86-64.so.2 and RUNPATH=/criu-bundle/lib
// (self-contained loader), so staging anywhere else makes execve fail with
// ENOENT inside the container (first criu-v2 e2e, 2026-07-12).
const v2BinDirInContainer = "/criu-bundle"

// stageV2Bundle copies the criu bundle into the container rootfs at
// /criu-bundle, including lib/ (criu's INTERP and RUNPATH point there —
// it runs against its own staged glibc, never the container's).
// cuda-checkpoint.real is staged under the plain name the CUDA plugin
// execs from PATH; inside the container the driver libs are
// runtime-injected so no wrapper is needed.
func (a *Agent) stageV2Bundle(root string, log *logrus.Entry) error {
	bundleDir := filepath.Dir(a.config.CRIUPath)
	stageDir := filepath.Join(root, strings.TrimPrefix(v2BinDirInContainer, "/"))
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("stage dir: %w", err)
	}
	stage := map[string]string{
		filepath.Join(bundleDir, "criu"):                                 "criu",
		resolveCRIUPluginDir(a.config.CRIUPath, log) + "/cuda_plugin.so": "cuda_plugin.so",
		filepath.Join(bundleDir, "cuda-checkpoint.real"):                 "cuda-checkpoint",
	}
	for src, name := range stage {
		if err := copyFileExec(src, filepath.Join(stageDir, name)); err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
	}
	libDst := filepath.Join(stageDir, "lib")
	if err := os.MkdirAll(libDst, 0o755); err != nil {
		return fmt.Errorf("stage lib dir: %w", err)
	}
	libs, err := os.ReadDir(filepath.Join(bundleDir, "lib"))
	if err != nil {
		return fmt.Errorf("read bundle lib dir: %w", err)
	}
	for _, e := range libs {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(bundleDir, "lib", e.Name())
		if err := copyFileExec(src, filepath.Join(libDst, e.Name())); err != nil {
			return fmt.Errorf("stage lib/%s: %w", e.Name(), err)
		}
	}
	return nil
}

// dumpV2 stages the bundle and runs CRIU dump inside the container's
// namespaces. On success the image files have been moved into checkpointDir.
func (a *Agent) dumpV2(ctx context.Context, containerInfo *containerd.ContainerInfo, checkpointDir, sourceUpperdir string, gpuPIDs []int, leaveRunning bool, log *logrus.Entry) error {
	hostPID := int(containerInfo.PID)
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	root := filepath.Join(procBase, strconv.Itoa(hostPID), "root")

	// 1. Stage the bundle into the container rootfs.
	if err := a.stageV2Bundle(root, log); err != nil {
		return err
	}

	// 2. Fresh in-container images dir.
	imgsDir := filepath.Join(root, strings.TrimPrefix(v2ImagesDirInContainer, "/"))
	_ = os.RemoveAll(imgsDir)
	if err := os.MkdirAll(imgsDir, 0o755); err != nil {
		return fmt.Errorf("images dir: %w", err)
	}

	// 3. NVIDIA device externals from the container's /dev view.
	externals, err := nvidiaDevExternals(filepath.Join(root, "dev"))
	if err != nil {
		return fmt.Errorf("device externals: %w", err)
	}

	// 4. Dump target: CRIU's -t is resolved in the entered pid namespace.
	// Use the GPU leader's session leader when available (PoC convention:
	// workload launched via setsid, tree root != container init); fall back
	// to the container init's in-namespace pid.
	targetHostPID := hostPID
	if len(gpuPIDs) > 0 {
		if sid, err := sessionID(procBase, gpuPIDs[0]); err == nil && sid > 1 {
			targetHostPID = sid
		}
	}
	nsPID, err := nsPidOf(procBase, targetHostPID)
	if err != nil {
		return fmt.Errorf("resolve ns pid of %d: %w", targetHostPID, err)
	}
	log.WithFields(logrus.Fields{
		"targetHostPID": targetHostPID,
		"nsPID":         nsPID,
		"externals":     len(externals),
	}).Info("criu-v2: dumping in-namespace")

	// 5. nsenter into the container's mnt/pid/net/ipc/uts namespaces and
	// dump. Environment is deliberately minimal: PATH covers the staged bundle so
	// the CUDA plugin finds cuda-checkpoint, and LD_LIBRARY_PATH points at
	// the bundle's own glibc stack — criu's RUNPATH covers only its direct
	// deps, not transitive ones (libnftables -> libmnl failed without it).
	// No agent driver-lib paths (those are poison inside the container);
	// /criu-bundle/lib carries no driver libs, so cuda-checkpoint still
	// resolves libcuda from the container's own search paths.
	// -r/-w: root and cwd must follow the entered mount namespace — without
	// them nsenter keeps the agent's root and the staged bundle path
	// resolves against the wrong filesystem ("No such file or directory").
	args := []string{
		"-t", strconv.Itoa(hostPID), "-m", "-p", "-n", "-i", "-u", "-r", "-w", "--",
		v2BinDirInContainer + "/criu", "dump",
		"-t", strconv.Itoa(nsPID),
		"-D", v2ImagesDirInContainer,
		"-o", "dump.log", "-v4",
		"--shell-job", "--tcp-established", "--ext-unix-sk",
		"--link-remap", "--ghost-links", "--ghost-limit", "1073741824",
		"--libdir", v2BinDirInContainer,
		// The kubelet readiness probe leaves half-open connections in the
		// listen backlog at dump time; skip them like the legacy engine
		// does (the probe just retries after restore).
		"--skip-in-flight",
		// TCP locking must not shell out to iptables: workload images
		// don't ship it (netfilter.c sh -c "iptables ..." exited 127).
		// The bundled criu links libnftables, so lock in-process.
		"--network-lock", "nftables",
		// Don't serialize cgroup membership: restore must not resurrect
		// the source pod's (deleted) kubepods cgroup — see restore_v2.go.
		"--manage-cgroups=ignore",
		// CRIU's default 10s task timeout fires while the CUDA plugin is
		// still interrogating the tree (the no-CUDA resource_tracker query
		// alone can eat the window); the alarm EINTRs the plugin's
		// --get-restore-tid read for the real GPU proc, which then gets
		// misclassified and fails CHECKPOINT_DEVICES ("Failed to track").
		// Same value the legacy engine uses for vLLM.
		"--timeout", "1200",
	}
	// Optional LZ4 page compression (upstream CRIU #2895), OFF by default.
	// Measured ~1.0x on the dominant cost — the cuda-checkpoint GPU-memory
	// image, which the CUDA plugin writes outside CRIU's compressible pagemap,
	// so --compress only touches sparse CPU-side pages. Not worth the dump-time
	// CPU by default. Opt in per node via NVSNAP_CRIU_V2_COMPRESS:
	//   "page"          per-page LZ4
	//   "region[:SIZE]" region LZ4 (SIZE default 256K, max 4M)
	//   "" / "off"      no compression (default)
	switch mode := os.Getenv("NVSNAP_CRIU_V2_COMPRESS"); {
	case mode == "page":
		args = append(args, "--compress")
	case mode == "region" || mode == "region:":
		args = append(args, "--compress-region", "256K")
	case strings.HasPrefix(mode, "region:"):
		args = append(args, "--compress-region", strings.TrimPrefix(mode, "region:"))
	default: // "" / "off": no compression
	}
	if leaveRunning {
		args = append(args, "--leave-running")
	}
	for _, e := range externals {
		args = append(args, "--external", e)
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(dctx, "nsenter", args...)
	cmd.Env = []string{
		"PATH=" + v2BinDirInContainer + ":/usr/sbin:/usr/bin:/sbin:/bin",
		"LD_LIBRARY_PATH=" + v2BinDirInContainer + "/lib",
		"HOME=/root",
	}
	out, runErr := cmd.CombinedOutput()

	// 6. Move images host-side into the standard checkpoint dir. After a
	// non-leave-running dump the target tree is gone and /proc/<pid>/root
	// with it — read the images from the overlay upperdir instead, which
	// persists until the runtime tears the container down.
	imgsHost := filepath.Join(sourceUpperdir, strings.TrimPrefix(v2ImagesDirInContainer, "/"))
	if _, statErr := os.Stat(imgsHost); statErr != nil {
		// leave-running (tree alive): the /proc root view still works.
		imgsHost = imgsDir
	}
	moveErr := moveDirContents(imgsHost, checkpointDir)
	_ = os.RemoveAll(imgsHost) // keep the upperdir mirror free of image files

	if runErr != nil {
		tail := tailOfFile(filepath.Join(checkpointDir, "dump.log"), 6)
		return fmt.Errorf("criu-v2 dump: %w (output: %s; dump.log tail: %s)", runErr, strings.TrimSpace(string(out)), tail)
	}
	if moveErr != nil {
		return fmt.Errorf("criu-v2: move images: %w", moveErr)
	}
	log.Info("criu-v2: dump complete, images moved to checkpoint dir")
	return nil
}

// nvidiaDevExternals builds CRIU --external dev[maj/min]:name entries for
// every /dev/nvidia* character device visible in the container.
func nvidiaDevExternals(devDir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(devDir, "nvidia*"))
	if err != nil {
		return nil, err
	}
	var exts []string
	for _, m := range matches {
		var st syscall.Stat_t
		if err := syscall.Stat(m, &st); err != nil {
			continue
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFCHR {
			continue
		}
		maj := uint32(st.Rdev >> 8 & 0xfff) //nolint:gosec // masked device-number bits (same idiom as scanCharDeviceFDs)
		min := uint32(st.Rdev & 0xff)       //nolint:gosec // masked device-number bits
		exts = append(exts, fmt.Sprintf("dev[%d/%d]:%s", maj, min, filepath.Base(m)))
	}
	if len(exts) == 0 {
		return nil, fmt.Errorf("no nvidia devices under %s", devDir)
	}
	return exts, nil
}

// sessionID returns the session id (host pid view) of pid.
func sessionID(procBase string, pid int) (int, error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	// field 6 (1-indexed) after the comm field; comm may contain spaces —
	// cut at the closing paren first.
	s := string(b)
	i := strings.LastIndex(s, ") ")
	if i < 0 {
		return 0, fmt.Errorf("malformed stat")
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 4 {
		return 0, fmt.Errorf("short stat")
	}
	return strconv.Atoi(fields[3]) // sid
}

// nsPidOf returns pid as seen inside its innermost pid namespace (last
// entry of the NSpid line).
func nsPidOf(procBase string, pid int) (int, error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			f := strings.Fields(line)
			if len(f) < 2 {
				break
			}
			return strconv.Atoi(f[len(f)-1])
		}
	}
	return 0, fmt.Errorf("no NSpid line for %d", pid)
}

func copyFileExec(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// moveDirContents moves every entry of src into dst (rename with copy
// fallback for cross-filesystem moves).
func moveDirContents(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if err := os.Rename(from, to); err != nil {
			// Rename failed (typically EXDEV: src upperdir and dst
			// checkpoint dir on different filesystems). Copy instead.
			// Recurse for directories — copyFileExec only handles files
			// (CRIU image dirs are flat today, but don't assume it).
			if e.IsDir() {
				if mkErr := os.MkdirAll(to, 0o755); mkErr != nil {
					return fmt.Errorf("move dir %s: %w", e.Name(), mkErr)
				}
				if rErr := moveDirContents(from, to); rErr != nil {
					return rErr
				}
				_ = os.RemoveAll(from)
			} else {
				if cErr := copyFileExec(from, to); cErr != nil {
					return fmt.Errorf("move %s: %w", e.Name(), cErr)
				}
				_ = os.Remove(from)
			}
		}
	}
	return nil
}

func tailOfFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(no dump.log)"
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

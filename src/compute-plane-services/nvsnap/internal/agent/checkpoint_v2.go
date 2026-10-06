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
// on restore (no setns). No userspace interception is injected into the
// workload; the retired legacy engine needed a preload library and
// patched event-loop libraries for that.
//
// io_uring rings are dumped and restored by the bundled CRIU (quiesced
// ring state, SQPOLL, worker threads and the SQ-array identity map), so
// engines run with stock libuv and uvloop and no preload or event-loop
// environment override.
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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	// gpushare's driver tool, when the bundle carries it (base images before
	// gpushare do not; only a gpushare capture needs it).
	if tool := filepath.Join(bundleDir, gpushareToolName); fileExists(tool) {
		stage[tool] = gpushareToolName
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
func (a *Agent) dumpV2(ctx context.Context, containerInfo *containerd.ContainerInfo, checkpointDir, sourceUpperdir string, gpuPIDs []int, leaveRunning bool, log *logrus.Entry) (*GPUShareInfo, error) {
	hostPID := int(containerInfo.PID)
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	root := filepath.Join(procBase, strconv.Itoa(hostPID), "root")

	// 1. Stage the bundle into the container rootfs.
	if err := a.stageV2Bundle(root, log); err != nil {
		return nil, err
	}

	// 2. Fresh in-container images dir.
	imgsDir := filepath.Join(root, strings.TrimPrefix(v2ImagesDirInContainer, "/"))
	_ = os.RemoveAll(imgsDir)
	if err := os.MkdirAll(imgsDir, 0o755); err != nil {
		return nil, fmt.Errorf("images dir: %w", err)
	}

	// 3. NVIDIA device externals from the container's /dev view.
	externals, err := nvidiaDevExternals(filepath.Join(root, "dev"))
	if err != nil {
		return nil, fmt.Errorf("device externals: %w", err)
	}

	// 4. Dump target: CRIU's -t is resolved in the entered pid namespace.
	//
	// Two choices, and they decide whether restore can ever be reliable:
	//
	//   session leader (default today): dumps a SUBTREE. CRIU only writes a
	//     pidns image when the target is the namespace root, so a subtree dump
	//     has none. Restore then cannot create a namespace -- it must recreate
	//     the original PIDs inside the placeholder's existing one, because PIDs
	//     are baked into the memory image (cached getpid, pthread TCBs, robust
	//     futexes). If the placeholder already used one of them, clone3(set_tid)
	//     fails with EEXIST and the restore dies. Whether that happens depends
	//     on where the placeholder's PID counter sits, which is why this looks
	//     like flakiness rather than a bug.
	//
	//   namespace init (this option): dumps the whole container tree, so CRIU
	//     records the pid namespace and restore recreates it fresh. Every PID
	//     is free by construction and the collision cannot occur.
	//
	// Off by default until validated end to end on every workload: it changes
	// what a capture contains, so it must not switch silently under anyone.
	targetHostPID := hostPID
	if len(gpuPIDs) > 0 {
		if sid, err := sessionID(procBase, gpuPIDs[0]); err == nil && sid > 1 {
			targetHostPID = sid
		}
	}
	nsPID, err := nsPidOf(procBase, targetHostPID)
	if err != nil {
		return nil, fmt.Errorf("resolve ns pid of %d: %w", targetHostPID, err)
	}
	log.WithFields(logrus.Fields{
		"targetHostPID": targetHostPID,
		"nsPID":         nsPID,
		"externals":     len(externals),
	}).Info("criu-v2: dumping in-namespace")

	// gpushare: when the workload runs under libnvsnap_gpushare.so, its GPU
	// state is saved by nvsnap-gpu-suspend before the dump and CRIU dumps the
	// CPU side only, without the CUDA plugin.
	gsPIDs, gsLib, gsOn, gsErr := gpushareTargets(procBase, targetHostPID)
	if gsErr != nil {
		return nil, gsErr
	}
	var gsInfo *GPUShareInfo
	var gpuMap string
	if gsOn {
		if !fileExists(filepath.Join(root, strings.TrimPrefix(GPUShareStoreInContainer, "/"))) {
			return nil, fmt.Errorf("gpushare: the workload loads %s but has no chunk store at %s "+
				"(the webhook mounts one into pods annotated %s=true)", gpushareLibName, GPUShareStoreInContainer, "nvsnap.io/gpushare")
		}
		if !fileExists(filepath.Join(root, strings.TrimPrefix(v2BinDirInContainer, "/"), gpushareToolName)) {
			return nil, fmt.Errorf("gpushare: %s is not in the agent bundle; the agent base image predates gpushare", gpushareToolName)
		}
		gm, err := gpushareSuspend(ctx, hostPID, gsPIDs, log)
		if err != nil {
			return nil, fmt.Errorf("gpushare suspend: %w", err)
		}
		gpuMap = gm
		gsInfo = &GPUShareInfo{PIDs: gsPIDs, StorePath: GPUShareStoreInContainer, LibPath: gsLib}
	}

	// 5. nsenter into the container's mnt/pid/net/ipc/uts namespaces and
	// dump. Environment is deliberately minimal: PATH covers the staged bundle
	// so the CUDA plugin finds cuda-checkpoint. LD_LIBRARY_PATH is deliberately
	// NOT set — the bundle's libraries carry RPATH=$ORIGIN (see Dockerfile.base),
	// so criu resolves its whole dependency graph, transitive deps included,
	// from /criu-bundle/lib on its own. Setting it here would leak the bundle's
	// glibc into cuda-checkpoint and abort restore into newer-glibc containers.
	// -r/-w: root and cwd must follow the entered mount namespace — without
	// them nsenter keeps the agent's root and the staged bundle path
	// resolves against the wrong filesystem ("No such file or directory").
	args := dumpV2Args(hostPID, nsPID, externals, leaveRunning, gsOn, gsOn && bundledSupportsDirectImageIO(), os.Getenv("NVSNAP_CRIU_V2_COMPRESS"))
	log.WithField("argv", "nsenter "+strings.Join(args, " ")).Info("criu-v2: dump argv")
	dctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(dctx, "nsenter", args...)
	// No LD_LIBRARY_PATH: the bundle's libraries carry RPATH=$ORIGIN (see
	// Dockerfile.base), so criu resolves its whole dependency graph from
	// /criu-bundle/lib on its own. Setting LD_LIBRARY_PATH here would be
	// inherited by cuda-checkpoint, forcing it onto the bundle's glibc instead
	// of the target container's -- which aborts it on newer-glibc images.
	cmd.Env = []string{
		"PATH=" + v2BinDirInContainer + ":/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
	out, runErr := cmd.CombinedOutput()

	// 6. Move images host-side into the standard checkpoint dir. After a
	// non-leave-running dump the target tree is gone and /proc/<pid>/root
	// with it — read the images from the overlay upperdir instead, which
	// persists until the runtime tears the container down.
	// Harvest through the overlay mount whenever the tree is still alive, and
	// fall back to the upperdir only once it is gone.
	//
	// Mutating a mounted overlay's upperdir directly is undefined behavior
	// (Documentation/filesystems/overlayfs.rst, "Changes to underlying
	// filesystems"): the kernel caches its own dentries for the merged view,
	// so removing the images directory underneath a live mount leaves a
	// zombie behind -- it still lists, with st_nlink == 0, but every openat
	// inside it fails ENOENT. That does not break the capture that did it; it
	// breaks the NEXT capture on the same pod, which cannot write images:
	//   Error (criu/image.c:748): Unable to open filelocks.img: No such file
	// and the state is not repairable, because a fresh mkdir through the
	// overlay inherits the poisoned dentry. Only restarting the container
	// clears it.
	//
	// The /proc/<pid>/root path goes through the overlay itself, so the mount
	// stays consistent. After a successful non-leave-running dump the tree is
	// gone and /proc/<pid>/root with it; the upperdir is then both the only
	// option and a safe one, since that container is already being torn down.
	imgsHost := imgsDir
	if _, statErr := os.Stat(imgsDir); statErr != nil {
		imgsHost = filepath.Join(sourceUpperdir, strings.TrimPrefix(v2ImagesDirInContainer, "/"))
	}
	moveErr := moveDirContents(imgsHost, checkpointDir)
	// Only clear the mirror once its contents are safely moved. Removing it
	// unconditionally destroys dump.log on any move failure, which is exactly
	// when it is the only account of what went wrong.
	if moveErr == nil {
		_ = os.RemoveAll(imgsHost)
	}

	if runErr != nil && gsOn {
		// CRIU puts a failed dump's tasks back as they were: stopped, GPU
		// state checkpointed. Bring the workload back rather than leave it
		// frozen; the capture still fails.
		if rerr := gpushareResume(ctx, hostPID, gsPIDs, "", log); rerr != nil {
			log.WithError(rerr).Error("gpushare: resuming the source after a failed dump failed; the workload stays suspended")
		}
	}
	if runErr != nil {
		tail := tailOfFile(filepath.Join(checkpointDir, "dump.log"), 6)
		if strings.TrimSpace(tail) == "" {
			// Move failed or never ran: read it where CRIU wrote it.
			tail = tailOfFile(filepath.Join(imgsHost, "dump.log"), 6)
		}
		// moveErr matters here too: "no dump.log" caused by a failed harvest
		// is a different problem from a dump that never wrote one, and
		// reporting only runErr makes the two indistinguishable.
		if moveErr != nil {
			// Join rather than format moveErr with %v: a caller inspecting
			// this with errors.Is/As needs to reach both the dump failure and
			// the harvest failure, not just the first one.
			return nil, fmt.Errorf("criu-v2 dump (output: %s; dump.log tail: %s): %w",
				strings.TrimSpace(string(out)), tail, errors.Join(runErr, moveErr))
		}
		return nil, fmt.Errorf("criu-v2 dump: %w (output: %s; dump.log tail: %s)", runErr, strings.TrimSpace(string(out)), tail)
	}
	if moveErr != nil {
		return nil, fmt.Errorf("criu-v2: move images: %w", moveErr)
	}
	if gsOn {
		if leaveRunning {
			if err := gpushareResume(ctx, hostPID, gsPIDs, "", log); err != nil {
				return nil, fmt.Errorf("gpushare: resume the source after the dump: %w", err)
			}
		}
		if err := a.gpushareCollectStore(containerInfo, root, checkpointDir, gpuMap); err != nil {
			return nil, err
		}
	}
	log.Info("criu-v2: dump complete, images moved to checkpoint dir")
	return gsInfo, nil
}

// gpuDevPatterns are the character devices a GPU workload may hold open that
// CRIU cannot dump itself and must be told to treat as external.
//
// gdrdrv is the GPUDirect RDMA (gdrcopy) node. It does not match nvidia*, so
// globbing only that prefix left it undeclared and any workload holding an fd
// on it failed the dump with "Can't dump file N of that type (chr 506:0)" --
// observed on NIM, which uses gdrcopy where the other engines do not. Match on
// the device names rather than major numbers: the NVIDIA majors are
// dynamically allocated and differ per node (nvidia-uvm was 507 on one host
// and is documented as 511 elsewhere), while the names are stable.
var gpuDevPatterns = []string{"nvidia*", "gdrdrv"}

// nvidiaDevExternals builds CRIU --external dev[maj/min]:name entries for
// every GPU character device visible in the container.
func nvidiaDevExternals(devDir string) ([]string, error) {
	var matches []string
	for _, pat := range gpuDevPatterns {
		m, err := filepath.Glob(filepath.Join(devDir, pat))
		if err != nil {
			return nil, fmt.Errorf("glob GPU device pattern %q in %s: %w", pat, devDir, err)
		}
		matches = append(matches, m...)
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
		return nil, fmt.Errorf("no GPU devices under %s", devDir)
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

// criuCompressBlockFlag returns the block-compression option the given
// `criu --help` text advertises: "--compress-block" on CRIU with the unified
// compression mode, "--compress-region" on the earlier spelling.
func criuCompressBlockFlag(help string) string {
	if strings.Contains(help, "--compress-block") {
		return "--compress-block"
	}
	return "--compress-region"
}

var (
	bundledCompressFlagOnce sync.Once
	bundledCompressFlag     string
)

// bundledCompressBlockFlag probes the bundled criu once for the spelling it
// understands. A failed probe falls back to the earlier name, which the
// binaries shipped before the upstream rename accept.
func bundledCompressBlockFlag() string {
	bundledCompressFlagOnce.Do(func() {
		out, _ := exec.Command(v2BinDirInContainer+"/criu", "--help").CombinedOutput()
		bundledCompressFlag = criuCompressBlockFlag(string(out))
	})
	return bundledCompressFlag
}

// dumpV2Args builds the nsenter + criu dump argv. A gpushare dump omits the
// CUDA plugin: nvsnap-gpu-suspend has already checkpointed the GPU state.
// directIO writes the pages image with O_DIRECT (--image-io-mode direct).
func dumpV2Args(hostPID, nsPID int, externals []string, leaveRunning, gpushare, directIO bool, compress string) []string {
	args := []string{
		"-t", strconv.Itoa(hostPID), "-m", "-p", "-n", "-i", "-u", "-r", "-w", "--",
		v2BinDirInContainer + "/criu", "dump",
		"-t", strconv.Itoa(nsPID),
		"-D", v2ImagesDirInContainer,
		"-o", "dump.log", "-v4",
		"--shell-job", "--tcp-established", "--ext-unix-sk",
		"--link-remap", "--ghost-links", "--ghost-limit", "1073741824",
	}
	if !gpushare {
		args = append(args, "--libdir", v2BinDirInContainer)
	}
	args = append(args,
		// The kubelet readiness probe leaves half-open connections in the
		// listen backlog at dump time; skip them (the probe just retries).
		"--skip-in-flight",
		// TCP locking must not shell out to iptables: workload images don't
		// ship it.
		"--network-lock", "nftables",
		"--manage-cgroups=ignore",
		"--timeout", "1200",
		"--file-locks",
	)
	if directIO {
		args = append(args, "--image-io-mode", "direct")
	}
	switch mode := compress; {
	case mode == "page":
		args = append(args, "--compress")
	case mode == "region" || mode == "region:" || mode == "block" || mode == "block:":
		args = append(args, bundledCompressBlockFlag(), "256K")
	case strings.HasPrefix(mode, "region:"):
		args = append(args, bundledCompressBlockFlag(), strings.TrimPrefix(mode, "region:"))
	case strings.HasPrefix(mode, "block:"):
		args = append(args, bundledCompressBlockFlag(), strings.TrimPrefix(mode, "block:"))
	default: // "" / "off": no compression
	}
	if leaveRunning {
		args = append(args, "--leave-running")
	}
	for _, e := range externals {
		args = append(args, "--external", e)
	}
	return args
}

var (
	bundledDirectIOOnce sync.Once
	bundledDirectIO     bool
)

// bundledSupportsDirectImageIO reports whether the bundled CRIU accepts
// --image-io-mode (direct I/O for the pages image; base v0.0.22 and later).
func bundledSupportsDirectImageIO() bool {
	bundledDirectIOOnce.Do(func() {
		out, _ := exec.Command(v2BinDirInContainer+"/criu", "--help").CombinedOutput()
		bundledDirectIO = criuSupportsDirectImageIO(string(out))
	})
	return bundledDirectIO
}

func criuSupportsDirectImageIO(help string) bool {
	return strings.Contains(help, "--image-io-mode")
}

// gpushareCollectStore moves the workload's chunk store into the checkpoint
// (checkpointDir/gpushare). Entries are moved, not the directory: the
// workload's mount still points at that directory, and a later capture of
// the same pod must start empty rather than write into this checkpoint.
func (a *Agent) gpushareCollectStore(containerInfo *containerd.ContainerInfo, root, checkpointDir, gpuMap string) error {
	dst := filepath.Join(checkpointDir, GPUShareCheckpointSubdir)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("gpushare: %w", err)
	}
	// The pod's store as the agent sees it: same filesystem as the
	// checkpoint dir, so each entry moves by rename. Fall back to the
	// container's view (a copy across mounts) when the pod uid is unknown.
	src := ""
	if uid := containerInfo.Labels["io.kubernetes.pod.uid"]; uid != "" {
		src = filepath.Join(a.config.CheckpointDir, GPUSharePodStoresSubdir, uid)
	}
	if src == "" || !fileExists(src) {
		src = filepath.Join(root, strings.TrimPrefix(GPUShareStoreInContainer, "/"))
	}
	if err := moveDirContents(src, dst); err != nil {
		return fmt.Errorf("gpushare: move chunk store %s to the checkpoint: %w", src, err)
	}
	// The GPU map goes in from the agent's side, into the checkpoint it owns:
	// the agent cannot create files inside a container's mounts through
	// /proc/<pid>/root. Restore finds it at <store>/ckpt/gpus because the
	// placeholder mounts this directory at the store path.
	ckpt := filepath.Join(dst, gpushareCkptSubdir)
	if err := os.MkdirAll(ckpt, 0o755); err != nil {
		return fmt.Errorf("gpushare: %w", err)
	}
	if err := os.WriteFile(filepath.Join(ckpt, "gpus"), []byte(gpuMap), 0o644); err != nil { //nolint:gosec // GPU UUIDs, read back by the restore tool
		return fmt.Errorf("gpushare: write GPU map: %w", err)
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

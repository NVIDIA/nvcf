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
// criu-v2 runs the node bundle (criu + cuda_plugin.so + cuda-checkpoint),
// which the webhook mounts read-only in the target container
// (bundle_tools.go), and executes CRIU dump INSIDE the
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
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
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
const v2BinDirInContainer = webhook.BundleToolsMountPath

// dumpFinal is what a dump leaves of the dumped tree.
type dumpFinal int

const (
	// dumpKill: the tree is killed (criu's default).
	dumpKill dumpFinal = iota
	// dumpLeaveRunning: the tree runs on.
	dumpLeaveRunning
)

func (f dumpFinal) flag() string {
	switch f {
	case dumpLeaveRunning:
		return "--leave-running"
	}
	return ""
}

func finalOf(leaveRunning bool) dumpFinal {
	if leaveRunning {
		return dumpLeaveRunning
	}
	return dumpKill
}

// dumpV2 runs CRIU dump inside the container's namespaces, with the
// node bundle mounted there. On success the image files have been moved into checkpointDir.
// resumeSource brings a source back after its dump, under its own deadline:
// the capture's may have run out, which is often why the dump failed.
func resumeSource(ctx context.Context, hostPID int, pids []int, fabricDir string, log *logrus.Entry) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gpushareResumeTimeout)
	defer cancel()
	return gpushareResume(rctx, hostPID, pids, "", fabricDir, log)
}

// gpushareResumeTimeout bounds bringing a source back after its dump.
const gpushareResumeTimeout = 10 * time.Minute

// ownGPUShareStore makes the workload's chunk store (and the fabric
// session directory in it, when there is one) owned by the user the
// workload's process runs as, so it can write its GPU memory there. The
// store is the workload's mount point, which it cannot replace; the
// session directory is reached through its parent (fabricSessionDir).
func ownGPUShareStore(procBase string, hostPID int, root string, fabric *fabricSessionDir) error {
	uid, gid, err := procOwner(procBase, hostPID)
	if err != nil {
		return err
	}
	if err := os.Lchown(filepath.Join(root, strings.TrimPrefix(GPUShareStoreInContainer, "/")), uid, gid); err != nil {
		return err
	}
	if fabric != nil {
		return fabric.parent.root.Lchown(fabric.name, uid, gid)
	}
	return nil
}

// procOwner returns the real uid and gid of process pid.
func procOwner(procBase string, pid int) (uid, gid int, err error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, 0, err
	}
	uid, gid = -1, -1
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "Uid:":
			uid, err = strconv.Atoi(f[1])
		case "Gid:":
			gid, err = strconv.Atoi(f[1])
		}
		if err != nil {
			return 0, 0, err
		}
	}
	if uid < 0 || gid < 0 {
		return 0, 0, fmt.Errorf("no Uid/Gid in %d/status", pid)
	}
	return uid, gid, nil
}

// errSourceLeftSuspended marks a capture whose source workload could not
// be resumed: it holds no GPU memory and serves nothing, while its pod
// still reads Ready.
var errSourceLeftSuspended = errors.New("gpushare: the source workload could not be resumed and stays suspended")

// errSourceLeftLocked marks a capture whose dump was killed before CRIU
// released the workload's network lock (an nftables table in the pod's
// network namespace, named by a run id only CRIU knew): the workload no
// longer reaches its peers, and the caller replaces it.
var errSourceLeftLocked = errors.New("criu: the dump was killed before it released the workload's network lock")

// sourceMustBeReplaced reports whether a capture left its source unusable
// (suspended, or with its network locked): its pods are replaced.
func sourceMustBeReplaced(err error) bool {
	return errors.Is(err, errSourceLeftSuspended) || errors.Is(err, errSourceLeftLocked)
}

// sourceMustBeReplacedText is sourceMustBeReplaced on an error's text, as
// it comes back from another node's agent.
func sourceMustBeReplacedText(s string) bool {
	return strings.Contains(s, errSourceLeftSuspended.Error()) || strings.Contains(s, errSourceLeftLocked.Error())
}

// runDump runs a CRIU dump (name args...) to its end, under its own
// deadline rather than its caller's context: killed half-way, CRIU leaves
// the workload stopped behind its network lock. A dump the deadline kills
// is reported as errSourceLeftLocked.
func runDump(ctx context.Context, deadline time.Duration, env []string, name string, args ...string) ([]byte, error) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deadline)
	defer cancel()
	cmd := exec.CommandContext(dctx, name, args...) //nolint:gosec // nsenter and CRIU, with arguments the agent builds
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil && dctx.Err() != nil {
		return out, fmt.Errorf("%w (after %s): %w", errSourceLeftLocked, deadline, err)
	}
	return out, err
}

// dumpV2Opts are the choices of one dumpV2.
type dumpV2Opts struct {
	LeaveRunning  bool
	FabricSession string
	// Freeze, for a member of a group capture, returns once every member
	// of the group is ready to be dumped (group_freeze.go); it runs right
	// before criu starts, so the members stop together.
	Freeze func(context.Context) error
	// HeldCgroup, for a held member, is its frozen container cgroup (or "")
	// once it is past the freeze point; ThawTarget thaws that container.
	HeldCgroup func() string
	ThawTarget func()
	// PostDumpDir, for a member of a group capture, is its post-dump
	// barrier: criu's post-dump hook waits there, the tree still stopped,
	// until every member is dumped (group_freeze.go).
	PostDumpDir string
	// Snapshot copies what a restore puts back beside the dump (the replay
	// mounts). dumpV2 runs it from criu's post-dump hook, the tree stopped,
	// so the copy matches the dumped memory (dump_snapshot.go); a dump whose
	// snapshot was not taken fails. criuSaves names the mountpoints the
	// dump saves itself, contents included (nil when it saves none).
	Snapshot func(criuSaves map[string]bool) error
}

func (a *Agent) dumpV2(ctx context.Context, containerInfo *containerd.ContainerInfo, checkpointDir string, gpuPIDs []int, o dumpV2Opts, log *logrus.Entry) (_ *GPUShareInfo, retErr error) {
	leaveRunning, fabricSession := o.LeaveRunning, o.FabricSession
	hostPID := int(containerInfo.PID)
	procBase := "/proc"
	if _, err := os.Stat("/host/proc"); err == nil {
		procBase = "/host/proc"
	}
	root := filepath.Join(procBase, strconv.Itoa(hostPID), "root")

	// 1. The tools run from the node bundle mounted read-only in the
	// container (bundle_tools.go), never from a copy in its filesystem.
	tools, err := bundleToolsOf(procBase, hostPID, a.bundleVersionsDir())
	if err != nil {
		return nil, fmt.Errorf("criu-v2: %w", err)
	}

	// 2. Fresh in-container images dir, made and later harvested through
	// the container's root (confined_fs.go): the workload can write its
	// own filesystem. The descriptors stay valid after the tree is gone.
	croot, err := openWorkloadDir(procBase, hostPID, "/")
	if err != nil {
		return nil, fmt.Errorf("images dir: %w", err)
	}
	defer func() { _ = croot.Close() }()
	imgsName := strings.TrimPrefix(v2ImagesDirInContainer, "/")
	_ = croot.root.RemoveAll(imgsName)
	if merr := croot.root.Mkdir(imgsName, 0o755); merr != nil {
		return nil, fmt.Errorf("images dir: %w", merr)
	}
	imgs, err := openWorkloadDir(procBase, hostPID, v2ImagesDirInContainer)
	if err != nil {
		return nil, fmt.Errorf("images dir: %w", err)
	}
	defer func() { _ = imgs.Close() }()

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
	var guard suspendGuard
	if fabricSession != "" && !gsOn {
		return nil, fmt.Errorf("gpushare: fabric session %s, but no process of this pod runs on the GPU under %s", fabricSession, gpushareLibName)
	}
	// A multi-node session's directory lives in the pod's store and must be
	// gone before the store is collected into the checkpoint.
	fabricDir, closeFabric := "", func() {}
	var fabric *fabricSessionDir
	if fabricSession != "" {
		s, closeFn, err := a.openFabricSession(fabricSession, containerInfo.Labels["io.kubernetes.pod.namespace"],
			containerInfo.Labels["io.kubernetes.pod.name"], containerInfo.Labels["io.kubernetes.pod.uid"])
		if err != nil {
			return nil, err
		}
		var once sync.Once
		fabric, fabricDir, closeFabric = s, s.container, func() { once.Do(closeFn) }
		defer closeFabric()
		log = log.WithField("fabricSession", fabricSession)
	}
	if gsOn {
		if !fileExists(filepath.Join(root, strings.TrimPrefix(GPUShareStoreInContainer, "/"))) {
			return nil, fmt.Errorf("gpushare: the workload loads %s but has no chunk store at %s "+
				"(the webhook mounts one into pods annotated %s=true)", gpushareLibName, GPUShareStoreInContainer, "nvsnap.io/gpushare")
		}
		if !tools.has(procBase, gpushareToolName) {
			return nil, fmt.Errorf("gpushare: %s is not in the pod's node bundle (version %s predates gpushare)", gpushareToolName, tools.version)
		}
		// The store is created by the kubelet, as root: an engine that runs
		// as another user cannot write its GPU memory there.
		if err := ownGPUShareStore(procBase, targetHostPID, root, fabric); err != nil {
			return nil, fmt.Errorf("gpushare: give the workload its chunk store: %w", err)
		}
		gm, suspended, err := gpushareSuspend(ctx, hostPID, gsPIDs, fabricDir, log)
		if suspended {
			// The source is stopped with its GPU memory released from
			// here, even when the suspend's last step failed. The dump
			// settles it (settleSource); any return before the dump brings
			// it back through the guard. Deferred after the fabric
			// session's close, so it runs while the session is still open.
			guard.arm(func() error { return resumeSource(ctx, hostPID, gsPIDs, fabricDir, log) }, func() {
				if o.PostDumpDir != "" {
					markPostDumpFailed(o.PostDumpDir, log)
				}
			})
			defer func() { retErr = guard.settle(retErr) }()
		}
		if err != nil {
			return nil, fmt.Errorf("gpushare suspend: %w", err)
		}
		gpuMap = gm
		gsInfo = &GPUShareInfo{PIDs: gsPIDs, StorePath: GPUShareStoreInContainer, LibPath: gsLib}
	}

	final := finalOf(leaveRunning)
	freeze := func() error {
		if o.Freeze == nil {
			return nil
		}
		return o.Freeze(ctx)
	}
	if nsPID == 1 {
		hooks := pidnsDumpHooks{Before: freeze, FreezeCgroup: o.HeldCgroup}
		// criu runs from the agent here: its action script sees the
		// agent's paths.
		var snapDir string
		if o.Snapshot != nil {
			snapDir = filepath.Join(checkpointDir, dumpHookDir)
			if err := os.MkdirAll(snapDir, 0o755); err != nil {
				return nil, fmt.Errorf("dump snapshot hook: %w", err)
			}
			defer func() { _ = os.RemoveAll(snapDir) }()
		}
		if o.PostDumpDir != "" {
			if err := os.MkdirAll(o.PostDumpDir, 0o755); err != nil {
				return nil, fmt.Errorf("post-dump barrier: %w", err)
			}
			defer func() { _ = os.RemoveAll(o.PostDumpDir) }()
		}
		if snapDir != "" || o.PostDumpDir != "" {
			dir := snapDir
			if dir == "" {
				dir = o.PostDumpDir
			}
			script := filepath.Join(dir, "action.sh")
			if err := writeDumpActionScript(script, snapDir, o.PostDumpDir); err != nil {
				return nil, fmt.Errorf("dump action script: %w", err)
			}
			hooks.ActionScript = script
		}
		stopSnapshot := func() (bool, error) { return true, nil }
		if snapDir != "" {
			// The pid-namespace dump saves the tmpfs mounts it recreates
			// (pidns_capture.go); the snapshot need not copy them too.
			entries, external, gpu, err := pidNSMounts(procBase, targetHostPID)
			if err != nil {
				return nil, fmt.Errorf("dump snapshot: %w", err)
			}
			saved := criuSavedTmpfs(entries, external, gpu)
			snap, err := openConfinedDir(snapDir)
			if err != nil {
				return nil, fmt.Errorf("dump snapshot hook: %w", err)
			}
			defer func() { _ = snap.Close() }()
			stopSnapshot = serveSnapshot(ctx, snap, func() error { return o.Snapshot(saved) })
		}
		// The workload is the container's pid 1: dump it with its pid
		// namespace, from outside (pidns_capture.go).
		_ = croot.root.RemoveAll(imgsName)
		err := a.dumpPIDNamespace(ctx, procBase, targetHostPID, checkpointDir, externals, final, gsOn, hooks, log)
		if taken, serr := stopSnapshot(); err == nil && !taken {
			err = snapshotNotTaken(serr)
		}
		if err != nil && o.PostDumpDir != "" {
			// The others wait, stopped, for this member's dump; told it
			// failed, the group's driver aborts them now, and a fabric
			// resume, which needs them, can proceed.
			markPostDumpFailed(o.PostDumpDir, log)
		}
		guard.handOff()
		resumeErr := settleSource(gsOn, leaveRunning, err != nil, func() error {
			return resumeSource(ctx, hostPID, gsPIDs, fabricDir, log)
		})
		if err != nil || resumeErr != nil {
			return nil, errors.Join(err, resumeErr)
		}
		if gsOn {
			closeFabric()
			if err := a.gpushareCollectStore(containerInfo, checkpointDir, gpuMap); err != nil {
				return nil, err
			}
		}
		log.Info("criu-v2: pid-namespace dump complete")
		return gsInfo, nil
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
	args := dumpV2Args(hostPID, nsPID, externals, final, gsOn, gsOn && tools.directImageIO(), os.Getenv("NVSNAP_CRIU_V2_COMPRESS"),
		tools.compressBlockFlag(), tools.pluginDir(procBase), a.ghostLimit())
	// criu runs in the container's mount namespace here. Its action script
	// is the bundle's dump hook (the agent binary, read-only), never a
	// script, which would run the workload's own shell; the snapshot
	// request lives in the images dir.
	stopSnapshot := func() (bool, error) { return true, nil }
	var hookEnv []string
	if o.Snapshot != nil {
		hookTool := "in-container/" + DumpHookName
		if !tools.has(procBase, hookTool) {
			return nil, fmt.Errorf("dump snapshot hook: the pod's node bundle (version %s) has no %s", tools.version, hookTool)
		}
		if err := imgs.root.Mkdir(dumpHookDir, 0o755); err != nil {
			return nil, fmt.Errorf("dump snapshot hook: %w", err)
		}
		hookRoot, err := imgs.root.OpenRoot(dumpHookDir)
		if err != nil {
			return nil, fmt.Errorf("dump snapshot hook: %w", err)
		}
		hook := &confinedDir{root: hookRoot, host: imgs.host + "/" + dumpHookDir}
		defer func() { _ = hook.Close() }()
		args = append(args, "--action-script", tools.path(hookTool))
		hookEnv = []string{DumpHookDirEnv + "=" + v2ImagesDirInContainer + "/" + dumpHookDir}
		// Dumped inside the container's mount namespace, criu saves no
		// mount: the snapshot copies every replay mount.
		stopSnapshot = serveSnapshot(ctx, hook, func() error { return o.Snapshot(nil) })
	}
	log.WithField("argv", "nsenter "+strings.Join(args, " ")).Info("criu-v2: dump argv")
	dctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(dctx, "nsenter", args...)
	// No LD_LIBRARY_PATH: the bundle's libraries carry RPATH=$ORIGIN (see
	// Dockerfile.base), so criu resolves its whole dependency graph from
	// /criu-bundle/lib on its own. Setting LD_LIBRARY_PATH here would be
	// inherited by cuda-checkpoint, forcing it onto the bundle's glibc instead
	// of the target container's -- which aborts it on newer-glibc images.
	cmd.Env = append([]string{"PATH=" + bundlePATH, "HOME=/root"}, hookEnv...)
	// The tree's stdio, read before the dump takes the tree away
	// (restore_stdio.go).
	stdio := readStdio(procBase, targetHostPID)
	var out []byte
	runErr := freeze()
	if runErr == nil && o.ThawTarget != nil {
		// criu runs inside the container here; it seizes running tasks.
		o.ThawTarget()
	}
	if runErr == nil {
		out, runErr = cmd.CombinedOutput()
		if runErr == nil && o.PostDumpDir != "" {
			// No post-dump hook here (criu runs in the container's mount
			// namespace): this member resumed with its dump, and only says
			// so, for the group's driver not to wait for it.
			markPostDumpReached(o.PostDumpDir, log)
		}
	}
	if taken, serr := stopSnapshot(); runErr == nil && !taken {
		runErr = snapshotNotTaken(serr)
	}

	// 6. Harvest the images through the descriptors opened before the dump,
	// through the container's overlay (confined_fs.go): they stay valid
	// when a dump that does not leave the tree running takes the tree, and
	// /proc/<pid>/root, away. (Changing a mounted overlay's upperdir
	// directly is undefined: it left a directory every later open failed
	// in, and broke the next capture of the same pod.) Only clear the images
	// dir once its contents are moved: on a failed move dump.log is the only
	// account of what went wrong.
	_ = imgs.root.RemoveAll(dumpHookDir)
	moveErr := harvestInto(imgs, checkpointDir)
	if moveErr == nil {
		_ = croot.root.Remove(imgsName)
	}

	var tail string
	if runErr != nil {
		tail = tailOfFile(filepath.Join(checkpointDir, "dump.log"), 6)
		if strings.TrimSpace(tail) == "" || strings.HasPrefix(tail, "(no ") {
			// Move failed or never ran: read it where CRIU wrote it.
			if b, terr := imgs.readTail("dump.log", restoreLogTail); terr == nil {
				tail = tailOfText(string(b), 6)
			}
		}
	}
	guard.handOff()
	if err := finishDump(runErr, moveErr, out, tail, gsOn, leaveRunning, func() error {
		return resumeSource(ctx, hostPID, gsPIDs, fabricDir, log)
	}); err != nil {
		return nil, err
	}
	if err := writeStdioMarker(checkpointDir, stdio); err != nil {
		// The restore then logs to a pipe nobody reads; the workload is fine.
		log.WithError(err).Warn("criu-v2: could not record the workload's stdio; its restore will not log to the new pod")
	}
	if gsOn {
		closeFabric()
		if err := a.gpushareCollectStore(containerInfo, checkpointDir, gpuMap); err != nil {
			return nil, err
		}
	}
	log.Info("criu-v2: dump complete, images moved to checkpoint dir")
	return gsInfo, nil
}

// settleSource brings back a source whose GPU state a dump suspended,
// whatever became of the dump: CRIU puts a failed dump's tasks back
// stopped, and a leave-running dump's tree holds no GPU memory until it is
// resumed, whether or not its images were then harvested. Only a whole
// dump of a tree CRIU went on to kill leaves nothing to resume. A resume
// that fails is reported as errSourceLeftSuspended, for the caller to
// replace the workload.
func settleSource(suspended, leaveRunning, dumpFailed bool, resume func() error) error {
	if !suspended || (!dumpFailed && !leaveRunning) {
		return nil
	}
	if err := resume(); err != nil {
		return fmt.Errorf("%w: resume after the dump: %w", errSourceLeftSuspended, err)
	}
	return nil
}

// suspendGuard settles a source the capture suspended when the capture
// returns before its dump could: a preparation step between the suspend
// and the dump that fails (no space for the snapshot hook, an action
// script that cannot be written) would otherwise leave the workload
// stopped with its GPU memory released, and its pod still Ready. Armed
// right after the suspend and handed off to the dump's own settlement
// (settleSource) once the dump has run.
type suspendGuard struct {
	resume func() error
	// abort tells a group capture's driver this member failed, so the
	// other members stop waiting for it (and a fabric resume can run).
	abort func()
}

func (g *suspendGuard) arm(resume func() error, abort func()) { g.resume, g.abort = resume, abort }

// handOff disarms the guard: the dump settles the source from here.
func (g *suspendGuard) handOff() { g.resume, g.abort = nil, nil }

// settle resumes a source no dump settled and returns the capture's err,
// joined with a failed resume as errSourceLeftSuspended for the caller to
// replace the workload.
func (g *suspendGuard) settle(err error) error {
	if g.resume == nil {
		return err
	}
	resume, abort := g.resume, g.abort
	g.handOff()
	if abort != nil {
		abort()
	}
	if rerr := resume(); rerr != nil {
		return errors.Join(err, fmt.Errorf("%w: resume after the capture failed before its dump: %w", errSourceLeftSuspended, rerr))
	}
	return err
}

// finishDump settles the source after an in-namespace dump and reports how
// the dump went: CRIU's error (runErr, with its output and the tail of its
// log), the error of harvesting its images (moveErr), and the source's
// resume, joined so that errors.Is reaches each.
func finishDump(runErr, moveErr error, out []byte, tail string, suspended, leaveRunning bool, resume func() error) error {
	resumeErr := settleSource(suspended, leaveRunning, runErr != nil, resume)
	output := strings.TrimSpace(string(out))
	switch {
	case runErr != nil && moveErr != nil:
		// "no dump.log" caused by a failed harvest is a different problem
		// from a dump that never wrote one: report both.
		return fmt.Errorf("criu-v2 dump (output: %s; dump.log tail: %s): %w", output, tail, errors.Join(runErr, moveErr, resumeErr))
	case runErr != nil:
		return fmt.Errorf("criu-v2 dump: %w (output: %s; dump.log tail: %s)", errors.Join(runErr, resumeErr), output, tail)
	case moveErr != nil:
		return errors.Join(fmt.Errorf("criu-v2: move images: %w", moveErr), resumeErr)
	}
	return resumeErr
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
// every GPU character device visible in the container, none for a
// container without GPUs.
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
	// A container without GPUs (an instance's companion) has none to
	// declare.
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

func tailOfFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(no " + filepath.Base(path) + ")"
	}
	return tailOfText(string(b), n)
}

// tailOfText is tailOfFile on a log's content.
func tailOfText(content string, n int) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	// CRIU's first errors name the cause; the tail is how it gave up
	// ("Unfreezing tasks", "Dumping FAILED"). Report both.
	var first []string
	for i, l := range lines {
		if i >= len(lines)-n {
			break
		}
		if strings.Contains(l, "Error (") {
			first = append(first, l)
			if len(first) == 3 {
				break
			}
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	tail := strings.Join(lines, " | ")
	if len(first) > 0 {
		return "first errors: " + strings.Join(first, " | ") + " ... " + tail
	}
	return tail
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

// dumpV2Args builds the nsenter + criu dump argv. A gpushare dump omits the
// CUDA plugin: nvsnap-gpu-suspend has already checkpointed the GPU state.
// directIO writes the pages image with O_DIRECT (--image-io-mode direct).
// pluginDir is where the mounted bundle keeps CRIU's plugins, and
// compressFlag its CRIU's name for block compression.
func dumpV2Args(hostPID, nsPID int, externals []string, final dumpFinal, gpushare, directIO bool, compress, compressFlag, pluginDir string, ghostLimit int64) []string {
	args := []string{
		"-t", strconv.Itoa(hostPID), "-m", "-p", "-n", "-i", "-u", "-r", "-w", "--",
		v2BinDirInContainer + "/criu", "dump",
		"-t", strconv.Itoa(nsPID),
		"-D", v2ImagesDirInContainer,
		"-o", "dump.log", "-v4",
		"--shell-job", "--tcp-established", "--ext-unix-sk",
		"--link-remap", "--ghost-links", "--ghost-limit", strconv.FormatInt(ghostLimit, 10),
	}
	// The CUDA plugin only for a tree with GPU devices (pidNSDumpArgs).
	if !gpushare && len(externals) > 0 {
		args = append(args, "--libdir", pluginDir)
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
		args = append(args, compressFlag, "256K")
	case strings.HasPrefix(mode, "region:"):
		args = append(args, compressFlag, strings.TrimPrefix(mode, "region:"))
	case strings.HasPrefix(mode, "block:"):
		args = append(args, compressFlag, strings.TrimPrefix(mode, "block:"))
	default: // "" / "off": no compression
	}
	if f := final.flag(); f != "" {
		args = append(args, f)
	}
	for _, e := range externals {
		args = append(args, "--external", e)
	}
	return args
}

func criuSupportsDirectImageIO(help string) bool {
	return strings.Contains(help, "--image-io-mode")
}

// gpushareCollectStore moves the workload's chunk store into the checkpoint
// (checkpointDir/gpushare). Entries are moved, not the directory: the
// workload's mount still points at that directory, and a later capture of
// the same pod must start empty rather than write into this checkpoint.
func (a *Agent) gpushareCollectStore(containerInfo *containerd.ContainerInfo, checkpointDir, gpuMap string) error {
	dst := filepath.Join(checkpointDir, GPUShareCheckpointSubdir)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("gpushare: %w", err)
	}
	// The pod's store as the agent sees it: same filesystem as the
	// checkpoint dir, so each entry moves by rename. Fall back to the
	// container's view (a copy across mounts) when the pod uid is unknown.
	// The workload writes the store: it is harvested through descriptors,
	// files only (confined_fs.go).
	var src *confinedDir
	if uid := containerInfo.Labels["io.kubernetes.pod.uid"]; uid != "" {
		if d, err := openConfinedDir(filepath.Join(a.config.CheckpointDir, GPUSharePodStoresSubdir, uid)); err == nil {
			src = d
		}
	}
	if src == nil {
		d, err := openWorkloadDir(hostProcBase(), int(containerInfo.PID), GPUShareStoreInContainer)
		if err != nil {
			return fmt.Errorf("gpushare: open the chunk store: %w", err)
		}
		src = d
	}
	defer func() { _ = src.Close() }()
	if err := harvestInto(src, dst); err != nil {
		return fmt.Errorf("gpushare: move chunk store %s to the checkpoint: %w", src.host, err)
	}
	// The GPU map goes in from the agent's side, into the checkpoint it owns:
	// the agent cannot create files inside a container's mounts through
	// /proc/<pid>/root. Restore finds it at <store>/gpus because the
	// placeholder mounts this directory at the store path.
	if err := os.WriteFile(filepath.Join(dst, gpushareGPUMapFile), []byte(gpuMap), 0o644); err != nil { //nolint:gosec // GPU UUIDs, read back by the restore tool
		return fmt.Errorf("gpushare: write GPU map: %w", err)
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// defaultGhostLimit is the largest deleted file a dump saves by default.
// Engines keep large buffers in deleted shared-memory files: a vLLM CPU
// KV-cache tier (--kv-offloading-size) is one such file, hundreds of GiB,
// part of the engine's state like its GPU memory.
const defaultGhostLimit int64 = 1 << 40

// ghostLimit is the agent's criu --ghost-limit.
func (a *Agent) ghostLimit() int64 {
	if a.config.CRIUGhostLimit > 0 {
		return a.config.CRIUGhostLimit
	}
	return defaultGhostLimit
}

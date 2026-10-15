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

// Command nvsnap-rootfs-restore is the B′ rootfs-restore shim (see
// docs/proposals/rootfs-overlay-mount-scale.md). The webhook injects it
// as the workload container's command for rootfs (non-CRIU) warm
// restores. It reconstructs the warm rootfs as a SINGLE overlay and
// pivot_roots the workload into it, then execs the workload's original
// command. This keeps the Pod object O(1) — one captured-tree mount —
// regardless of how many paths the capture touched (gpt-oss: 1055;
// DeepSeek: more). The per-path mount topology never enters the Pod
// spec, so the etcd object-size ceiling is never approached.
//
// Why the overlay is built HERE (workload), not in an init container:
// this process runs as PID 1 in the workload whose own `/` IS the
// image rootfs, which is the natural — and persistent — lowerdir. An
// init container's rootfs is ephemeral and in a different mount
// namespace, so it can't serve as a durable lower.
//
// Model: overlay(lowerdir=<captured-rootfs>:/, upperdir=<scratch>/upper,
// workdir=<scratch>/work). The captured tree wins over the image
// (captured-over-image == the container's original overlay semantics),
// so nothing is masked. A fresh per-pod scratch upper takes runtime
// writes (e.g. /opt/nim/workspace), keeping the captured source RO.
//
// Requires CAP_SYS_ADMIN (overlay mount, rbind, pivot_root). This is
// NOT `privileged`: SYS_ADMIN grants mount power but does not alter the
// device cgroup or add host device nodes, so the device plugin's
// single-GPU isolation is preserved.
//
// Env contract (all injected by the webhook):
//
//	NVSNAP_CAPTURED_DIR  (required) RO mount of the captured tree; its
//	                   "rootfs" subdir is the captured container rootfs,
//	                   "volumes/<name>" are captured non-rootfs volumes.
//	NVSNAP_SCRATCH_DIR   (required) writable emptyDir for the overlay
//	                   upper+work (per-pod).
//	NVSNAP_ORIG_COMMAND  (required) JSON []string — the workload's original
//	                   command (entrypoint+args, from the capture's
//	                   recorded PID-1 argv or the pod's command+args).
//	                   Execd after pivot_root. Must be non-empty.
//	NVSNAP_ORIG_CWD      (optional) working directory to chdir into before
//	                   exec; default "/".
//	NVSNAP_ROOTFS_VOLUMES(optional) JSON []{name,mountPath} — captured
//	                   non-rootfs volumes to bind into the merged root.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const mergedRoot = "/nvsnap-merged"

type volMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

// config is the fully-parsed, validated shim input. Kept separate from
// the syscall work so parsing is unit-testable without root/mounts.
type config struct {
	capturedDir string
	scratchDir  string
	argv        []string
	cwd         string
	volumes     []volMount
}

// parseConfig reads and validates the env contract. getenv is injected so
// tests don't touch the process environment. Returns a clear error for
// every missing/malformed input — the shim is PID 1 of the workload, so a
// bad config must fail loudly (kubelet shows it as a crashed container)
// rather than silently launch the workload without its warm rootfs.
// runNoOverlay is the cachedir (ember) restore path. kubelet has already
// mounted the rox read-only at NVSNAP_PREWARM_DIR and wired the pod's normal
// mounts (driver libs, /dev/nvidia*, /etc/hosts, serviceaccount), so there
// is NO overlay, NO pivot_root, and NO mount replay. We only prewarm the
// cache tree into the page cache (same cgroup as the engine, fully ahead
// of it) then exec the original entrypoint in place. Reads only — no
// mount(2), no CAP_SYS_ADMIN, device isolation untouched.
func runNoOverlay() error {
	dir := os.Getenv("NVSNAP_PREWARM_DIR")
	if dir == "" {
		return fmt.Errorf("NVSNAP_NO_OVERLAY set but NVSNAP_PREWARM_DIR is empty")
	}
	raw := os.Getenv("NVSNAP_ORIG_COMMAND")
	if raw == "" {
		return fmt.Errorf("NVSNAP_ORIG_COMMAND is required (JSON []string of the workload entrypoint)")
	}
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil {
		return fmt.Errorf("NVSNAP_ORIG_COMMAND is not valid JSON []string: %w", err)
	}
	if len(argv) == 0 || argv[0] == "" {
		return fmt.Errorf("NVSNAP_ORIG_COMMAND decoded to an empty command")
	}
	cwd := os.Getenv("NVSNAP_ORIG_CWD")
	if cwd == "" {
		cwd = "/"
	}

	if prewarmEnabled(os.Getenv) {
		fmt.Fprintf(os.Stderr, "nvsnap-rootfs-restore: cachedir page-cache prewarm starting on %s\n", dir)
		prewarmCapturedRootfs(dir)
	} else {
		fmt.Fprintln(os.Stderr, "nvsnap-rootfs-restore: page-cache prewarm disabled (NVSNAP_PREWARM=0)")
	}

	if err := unix.Chdir(cwd); err != nil {
		if err2 := unix.Chdir("/"); err2 != nil {
			return fmt.Errorf("chdir %q (and / fallback): %w", cwd, err2)
		}
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		bin = argv[0]
	}
	//nolint:gosec // G204: exec target is the workload's own captured entrypoint, not user input
	if err := syscall.Exec(bin, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %v: %w", argv, err)
	}
	return nil
}

func parseConfig(getenv func(string) string) (config, error) {
	var c config
	c.capturedDir = getenv("NVSNAP_CAPTURED_DIR")
	if c.capturedDir == "" {
		return c, fmt.Errorf("NVSNAP_CAPTURED_DIR is required")
	}
	c.scratchDir = getenv("NVSNAP_SCRATCH_DIR")
	if c.scratchDir == "" {
		return c, fmt.Errorf("NVSNAP_SCRATCH_DIR is required")
	}

	raw := getenv("NVSNAP_ORIG_COMMAND")
	if raw == "" {
		return c, fmt.Errorf("NVSNAP_ORIG_COMMAND is required (JSON []string of the workload entrypoint)")
	}
	if err := json.Unmarshal([]byte(raw), &c.argv); err != nil {
		return c, fmt.Errorf("NVSNAP_ORIG_COMMAND is not valid JSON []string: %w", err)
	}
	if len(c.argv) == 0 || c.argv[0] == "" {
		return c, fmt.Errorf("NVSNAP_ORIG_COMMAND decoded to an empty command")
	}

	c.cwd = getenv("NVSNAP_ORIG_CWD")
	if c.cwd == "" {
		c.cwd = "/"
	}

	if raw := getenv("NVSNAP_ROOTFS_VOLUMES"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &c.volumes); err != nil {
			return c, fmt.Errorf("NVSNAP_ROOTFS_VOLUMES is not valid JSON: %w", err)
		}
		for i, v := range c.volumes {
			if v.Name == "" || v.MountPath == "" {
				return c, fmt.Errorf("NVSNAP_ROOTFS_VOLUMES[%d] missing name or mountPath", i)
			}
		}
	}
	return c, nil
}

// mountpoints returns the mount points of the current mount namespace
// from /proc/self/mountinfo, sorted shallowest-first so parents are
// rbound before children. Mount points are field 5 (0-based index 4),
// with octal escapes (\040 space, \011 tab, \012 newline, \134 \) decoded.
func mountpoints() ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	return parseMountpoints(string(data)), nil
}

// parseMountpoints extracts mount points (field 5, 0-based index 4) from
// mountinfo content, decodes octal escapes, and sorts shallowest-first.
// Split out from mountpoints() so it's unit-testable without /proc.
func parseMountpoints(data string) []string {
	rep := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	lines := strings.Split(data, "\n")
	mps := make([]string, 0, len(lines))
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		mps = append(mps, rep.Replace(f[4]))
	}
	sort.SliceStable(mps, func(i, j int) bool {
		return strings.Count(mps[i], "/") < strings.Count(mps[j], "/")
	})
	return mps
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "nvsnap-rootfs-restore: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// cachedir (ember) mode: the rox is already mounted read-only at
	// NVSNAP_PREWARM_DIR by kubelet, alongside all the pod's normal mounts.
	// No overlay, no pivot_root, no mount replay — just prewarm the cache
	// tree (same cgroup as the engine, fully ahead of it) and exec the
	// original entrypoint. See cachedir.go / ember-cache-env.md.
	if os.Getenv("NVSNAP_NO_OVERLAY") != "" {
		return runNoOverlay()
	}

	cfg, err := parseConfig(os.Getenv)
	if err != nil {
		return err
	}
	capturedDir := cfg.capturedDir
	scratchDir := cfg.scratchDir

	capturedRootfs := filepath.Join(capturedDir, "rootfs")
	if st, statErr := os.Stat(capturedRootfs); statErr != nil || !st.IsDir() {
		return fmt.Errorf("captured rootfs not found at %s: %w", capturedRootfs, statErr)
	}

	// The overlay's parent mount (/) must be private, else pivot_root
	// (and shared-propagation) misbehave.
	if mErr := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); mErr != nil {
		return fmt.Errorf("make / private: %w", mErr)
	}

	upper := filepath.Join(scratchDir, "upper")
	work := filepath.Join(scratchDir, "work")
	for _, d := range []string{upper, work, mergedRoot} {
		if mkErr := os.MkdirAll(d, 0o755); mkErr != nil {
			return fmt.Errorf("mkdir %s: %w", d, mkErr)
		}
	}

	// captured-over-image: lowerdir is searched left-to-right, first hit
	// wins, so listing the captured rootfs before "/" makes captured
	// files shadow the image while the image shows through everywhere
	// the capture didn't touch.
	opts := fmt.Sprintf("lowerdir=%s:/,upperdir=%s,workdir=%s", capturedRootfs, upper, work)
	if mErr := unix.Mount("overlay", mergedRoot, "overlay", 0, opts); mErr != nil {
		return fmt.Errorf("overlay mount (%s): %w", opts, mErr)
	}

	// Replay every kubelet/runtime-injected mount into the new root
	// before we switch into it. This is essential: the NVIDIA container
	// runtime bind-mounts the driver userspace libs (libcuda.so,
	// libnvidia-*.so) and binaries (nvidia-smi) into /usr/lib and /usr/bin
	// at create time — they are NOT in the image or the captured tree, and
	// pivot_root would drop them, leaving the workload with no CUDA driver
	// ("Failed to infer device type"). It also preserves /proc, /sys,
	// /dev (incl. the device-plugin /dev/nvidia*), /dev/shm, /etc/hosts,
	// /etc/resolv.conf, and the serviceaccount/secret mounts. We skip our
	// own working dirs and the captured-volume paths (bound from the
	// capture just below, which must win over the empty live volume).
	captureVolPaths := map[string]bool{}
	var capturedVols []volMount
	if v := os.Getenv("NVSNAP_ROOTFS_VOLUMES"); v != "" {
		if jErr := json.Unmarshal([]byte(v), &capturedVols); jErr != nil {
			return fmt.Errorf("NVSNAP_ROOTFS_VOLUMES invalid JSON: %w", jErr)
		}
		for _, vm := range capturedVols {
			captureVolPaths[filepath.Clean(vm.MountPath)] = true
		}
	}
	skip := map[string]bool{"/": true, mergedRoot: true, capturedDir: true, scratchDir: true, "/nvsnap": true}
	mps, err := mountpoints()
	if err != nil {
		return fmt.Errorf("read mountinfo: %w", err)
	}
	for _, mp := range mps {
		if skip[mp] || captureVolPaths[mp] ||
			strings.HasPrefix(mp+"/", mergedRoot+"/") ||
			strings.HasPrefix(mp+"/", capturedDir+"/") ||
			strings.HasPrefix(mp+"/", scratchDir+"/") ||
			strings.HasPrefix(mp+"/", "/nvsnap/") {
			continue
		}
		// The bind target must match the source TYPE: file mounts (the
		// NVIDIA driver libs/binaries, /etc/hosts, /etc/resolv.conf,
		// serviceaccount token files are all single-FILE bind-mounts)
		// need an empty file as the target; directory mounts need a dir.
		// Binding a file onto a mkdir'd directory fails ENOTDIR — which is
		// why libcuda.so went missing.
		fi, statErr := os.Stat(mp)
		if statErr != nil {
			continue
		}
		dst := filepath.Join(mergedRoot, mp)
		if fi.IsDir() {
			if mkErr := os.MkdirAll(dst, 0o755); mkErr != nil {
				continue
			}
		} else {
			if mkErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
				continue
			}
			if f, openErr := os.OpenFile(dst, os.O_CREATE, 0o644); openErr == nil {
				_ = f.Close()
			} else if !os.IsExist(openErr) {
				continue
			}
		}
		_ = unix.Mount(mp, dst, "", unix.MS_BIND|unix.MS_REC, "")
	}

	// Restore captured non-rootfs volumes at their original mount paths,
	// AFTER the replay so the captured data wins over the (empty) live
	// emptyDir mount at the same path. Few (O(1)).
	for _, vm := range capturedVols {
		src := filepath.Join(capturedDir, "volumes", vm.Name)
		if _, statErr := os.Stat(src); statErr != nil {
			continue // volume not captured; leave image/live volume as-is
		}
		dst := filepath.Join(mergedRoot, vm.MountPath)
		if mkErr := os.MkdirAll(dst, 0o755); mkErr != nil {
			return fmt.Errorf("mkdir volume dst %s: %w", dst, mkErr)
		}
		if mErr := unix.Mount(src, dst, "", unix.MS_BIND|unix.MS_REC, ""); mErr != nil {
			return fmt.Errorf("bind captured volume %s: %w", vm.Name, mErr)
		}
	}

	// Page-cache prewarm: read the captured rootfs (the rox lowerdir) into
	// the page cache NOW — before pivot_root detaches it and before we exec
	// the engine — so the engine's mmap weight-load hits RAM instead of the
	// ~350 MB/s random-read HDML path. Same cgroup as the engine, fully
	// ahead of it (no read contention). Gated by NVSNAP_PREWARM (default on).
	// See prewarm.go.
	if prewarmEnabled(os.Getenv) {
		fmt.Fprintf(os.Stderr, "nvsnap-rootfs-restore: page-cache prewarm starting on %s\n", capturedRootfs)
		prewarmCapturedRootfs(capturedRootfs)
	} else {
		fmt.Fprintln(os.Stderr, "nvsnap-rootfs-restore: page-cache prewarm disabled (NVSNAP_PREWARM=0)")
	}

	// pivot_root into the merged tree, then detach the old root.
	oldRoot := filepath.Join(mergedRoot, ".nvsnap-oldroot")
	if mkErr := os.MkdirAll(oldRoot, 0o755); mkErr != nil {
		return fmt.Errorf("mkdir oldroot: %w", mkErr)
	}
	if pErr := unix.PivotRoot(mergedRoot, oldRoot); pErr != nil {
		return fmt.Errorf("pivot_root: %w", pErr)
	}
	if uErr := unix.Unmount("/.nvsnap-oldroot", unix.MNT_DETACH); uErr != nil {
		return fmt.Errorf("detach old root: %w", uErr)
	}
	_ = os.Remove("/.nvsnap-oldroot")

	// chdir into the captured working directory so the entrypoint's
	// relative paths resolve as they did pre-capture. Fall back to "/"
	// if the recorded cwd no longer exists in the merged tree.
	if cdErr := unix.Chdir(cfg.cwd); cdErr != nil {
		if err2 := unix.Chdir("/"); err2 != nil {
			return fmt.Errorf("chdir %q (and / fallback): %w", cfg.cwd, err2)
		}
	}

	// Exec the workload's original entrypoint. Replaces this process so
	// the workload becomes PID 1, exactly as if nvsnap were not involved.
	bin, err := exec.LookPath(cfg.argv[0])
	if err != nil {
		bin = cfg.argv[0]
	}
	//nolint:gosec // G204: exec target is the workload's own captured entrypoint, not user input
	if err := syscall.Exec(bin, cfg.argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %v: %w", cfg.argv, err)
	}
	return nil
}

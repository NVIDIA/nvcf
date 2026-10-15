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

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
)

// excludeArgsForMountpoints converts a list of absolute container-side
// mountpoints into tar `--exclude` patterns rooted at the archive's "."
// prefix. Two patterns per mountpoint: the entry itself and the subtree.
// The mountpoint set comes from /proc/<pid>/mountinfo at the appropriate
// end of the mirror (source pid at dump, placeholder pid at restore) —
// no static path list, runtime-agnostic, hardware-agnostic.
func excludeArgsForMountpoints(mps []string) []string {
	if len(mps) == 0 {
		return nil
	}
	args := make([]string, 0, len(mps)*2)
	for _, mp := range mps {
		// mp is absolute ("/usr/bin/nvidia-smi"); archive is rooted at "."
		// so the in-archive path is "." + mp.
		entry := "." + mp
		args = append(args, "--exclude="+entry, "--exclude="+entry+"/*")
	}
	return args
}

// mirrorOverlayDir copies one overlay directory tree to another, preserving
// everything CRIU's path-based file restoration depends on: regular files
// (with sizes/mtimes/perms), symlinks, hardlinks, sparse holes, special
// files (sockets/fifos/character devices), xattrs, and ACLs.
//
// On the restore side, callers must write through the placeholder's overlay
// (via mirrorIntoMntns), not directly to its diff dir — overlayfs caches
// the merged-view dentries at mount time and doesn't invalidate them when
// the upperdir is modified externally. This function is suitable for
// dump-side capture where we read straight from the source's diff dir.
//
// xattrs in particular are not optional: overlayfs whiteouts are
// represented as char devs with rdev=0 (or trusted.overlay.* xattrs); both
// must travel for the destination to maintain the same effective view.
//
// Used by:
//   - checkpoint flow: snapshots the source container's overlay upperdir
//     into the checkpoint directory's rootfs-diff/ subdir, so everything
//     the container wrote at runtime travels with the dump.
//   - restore flow: replays that snapshot into the placeholder pod's
//     overlay upperdir before CRIU runs, so file paths CRIU recorded by
//     name resolve and stat() with the right size.
//
// rsync is invoked via a tar-pipe rather than direct rsync to avoid
// requiring rsync in the agent image (it isn't in our Debian slim base).
// tar over pipe preserves all the same attributes and is sufficient for
// the contained data set (overlay diff, not a general filesystem).
// rootfsDiffExcludeGlobs returns tar --exclude patterns for large,
// re-fetchable content that must not be double-shipped in the rootfs-diff.
// The dominant case: a model cache written to the container's writable layer
// (e.g. HF_HOME on the rootfs). For GPU workloads the weights are loaded onto
// the device and the on-disk copy is not referenced by any restored VMA or fd
// (verified on a Qwen2.5-32B criu-v2 dump: zero references to the hub cache in
// files.img or the mm images), so shipping it alongside the GPU-memory image
// is pure waste (~62G doubled on that dump). Restore stays correct because
// CRIU never recorded those paths.
//
// Override or disable via NVSNAP_ROOTFS_DIFF_EXCLUDE (colon-separated tar
// globs; an empty value disables exclusion). Disable it for any workload that
// keeps weights mmap'd from the on-disk cache rather than copying to device.
func rootfsDiffExcludeGlobs() []string {
	if v, ok := os.LookupEnv("NVSNAP_ROOTFS_DIFF_EXCLUDE"); ok {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return strings.Split(v, ":")
	}
	// Default: the HuggingFace hub cache (content-addressed, re-fetchable).
	return []string{"*/huggingface/hub", "*/huggingface/hub/*"}
}

func mirrorOverlayDir(src, dst string, excludeMountpoints, excludeGlobs []string, log *logrus.Entry) error {
	if src == "" || dst == "" {
		return fmt.Errorf("mirrorOverlayDir: empty src or dst")
	}
	st, err := os.Stat(src)
	if err != nil {
		// Don't silently treat ENOENT as "empty upperdir" — that hides
		// "agent can't see this path" deployment bugs. Caller decides
		// whether the absence is fatal.
		return fmt.Errorf("stat src %s: %w", src, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("src %s is not a directory", src)
	}
	if mkErr := os.MkdirAll(dst, 0o755); mkErr != nil {
		return fmt.Errorf("mkdir dst %s: %w", dst, mkErr)
	}

	// tar -C src -cf - --xattrs --acls --numeric-owner --sparse . | tar -C dst -xf - --xattrs --acls --numeric-owner
	// Exclude trusted.overlay.* — those xattrs are managed by overlayfs
	// itself; preserving them across mirrors is incorrect and would confuse
	// the destination overlay. Other trusted.* xattrs (none expected in
	// container upperdirs) are intentionally not included.
	srcArgs := []string{
		"-C", src,
		"-cf", "-",
		"--xattrs",
		"--xattrs-exclude=trusted.overlay.*",
		"--acls",
		"--numeric-owner",
		"--sparse",
		"--one-file-system",
	}
	srcArgs = append(srcArgs, excludeArgsForMountpoints(excludeMountpoints)...)
	for _, g := range excludeGlobs {
		srcArgs = append(srcArgs, "--exclude="+g)
	}
	srcArgs = append(srcArgs, ".")
	srcTar := exec.Command("tar", srcArgs...) //nolint:gosec // args are internally constructed (PIDs/paths), not user input

	dstTar := exec.Command("tar",
		"-C", dst,
		"-xf", "-",
		"--xattrs",
		"--xattrs-exclude=trusted.overlay.*",
		"--acls",
		"--numeric-owner",
		"--preserve-permissions",
	)

	pipe, err := srcTar.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	dstTar.Stdin = pipe

	var srcErr, dstErr strings.Builder
	srcTar.Stderr = &srcErr
	dstTar.Stderr = &dstErr

	if err := dstTar.Start(); err != nil {
		return fmt.Errorf("start dst tar: %w", err)
	}
	if err := srcTar.Start(); err != nil {
		return fmt.Errorf("start src tar: %w", err)
	}
	srcWait := srcTar.Wait()
	dstWait := dstTar.Wait()

	if srcWait != nil {
		return fmt.Errorf("src tar (-C %s): %w (stderr: %s)", src, srcWait, srcErr.String())
	}
	if dstWait != nil {
		return fmt.Errorf("dst tar (-C %s): %w (stderr: %s)", dst, dstWait, dstErr.String())
	}

	if log != nil {
		size := dirSizeBytes(dst)
		log.WithFields(logrus.Fields{
			"src":   src,
			"dst":   dst,
			"bytes": size,
		}).Info("Mirrored overlay diff")
	}
	return nil
}

// mirrorIntoMntns replays a previously-captured overlay diff into another
// pod's mount namespace by piping a tar of the diff into a `tar -x` that
// runs INSIDE the target mntns via nsenter. Writing through the merged
// overlay path (placeholder root /) rather than its underlying diff dir
// is what makes the new files visible to the placeholder's processes —
// overlayfs caches its merged-view dentries at mount time and doesn't see
// external mutations to the upperdir.
//
// The tar pipe preserves the same attributes as mirrorOverlayDir
// (xattrs/ACLs/hardlinks/sparse). The target tar runs unprivileged
// from the placeholder's perspective; the agent's privilege provides the
// nsenter capability.
func mirrorIntoMntns(srcDiff string, placeholderPID int, excludeMountpoints []string, log *logrus.Entry) error {
	if srcDiff == "" || placeholderPID <= 0 {
		return fmt.Errorf("mirrorIntoMntns: empty src or invalid pid")
	}
	if _, err := os.Stat(srcDiff); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat src %s: %w", srcDiff, err)
	}

	srcArgs := []string{
		"-C", srcDiff,
		"-cf", "-",
		"--xattrs",
		"--xattrs-exclude=trusted.overlay.*",
		"--acls",
		"--numeric-owner",
		"--sparse",
		"--one-file-system",
	}
	srcArgs = append(srcArgs, excludeArgsForMountpoints(excludeMountpoints)...)
	srcArgs = append(srcArgs, ".")
	srcTar := exec.Command("tar", srcArgs...) //nolint:gosec // args are internally constructed (PIDs/paths), not user input

	mntnsPath := fmt.Sprintf("/proc/%d/ns/mnt", placeholderPID)
	dstTar := exec.Command("nsenter", "--mount="+mntnsPath, "--", //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		"tar",
		"-C", "/",
		"-xf", "-",
		"--xattrs",
		"--xattrs-exclude=trusted.overlay.*",
		"--acls",
		"--numeric-owner",
		"--preserve-permissions",
		"--overwrite",
	)
	pipe, err := srcTar.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	dstTar.Stdin = pipe
	var srcErr, dstErr strings.Builder
	srcTar.Stderr = &srcErr
	dstTar.Stderr = &dstErr

	if err := dstTar.Start(); err != nil {
		return fmt.Errorf("start dst tar: %w", err)
	}
	if err := srcTar.Start(); err != nil {
		return fmt.Errorf("start src tar: %w", err)
	}
	srcWait := srcTar.Wait()
	dstWait := dstTar.Wait()
	if srcWait != nil {
		return fmt.Errorf("src tar (-C %s): %w (stderr: %s)", srcDiff, srcWait, srcErr.String())
	}
	if dstWait != nil {
		return fmt.Errorf("dst tar (nsenter pid=%d): %w (stderr: %s)", placeholderPID, dstWait, dstErr.String())
	}
	if log != nil {
		log.WithFields(logrus.Fields{
			"src": srcDiff,
			"pid": placeholderPID,
		}).Info("Replayed overlay diff into placeholder mntns")
	}
	return nil
}

// tarMount captures the contents of a single mountpoint inside a source
// container's view into a tar file. Used by cross-pod mount replay
// (docs/archive/CROSS-POD-MOUNT-REPLAY-DESIGN.md) for replay-class mounts whose
// contents the placeholder pod cannot reproduce on its own (e.g.
// /dev/shm/psm_* NCCL segments for multi-GPU).
//
// Reads via /proc/<srcPID>/root/<mp> rather than nsenter so the agent
// stays in its own mntns; tar is purely a file-system operation that
// follows the proc magic-link. --one-file-system prevents accidental
// recursion into nested mounts (parity with mirrorOverlayDir).
//
// Returns nil if the source path is missing or empty — a captured-but-
// empty mount on restore is the same as no capture (untar is a no-op).
func tarMount(srcPID int, mp, outFile string, log *logrus.Entry) error {
	if srcPID <= 0 || mp == "" || outFile == "" {
		return fmt.Errorf("tarMount: empty pid/mp/out")
	}
	src := fmt.Sprintf("/proc/%d/root%s", srcPID, mp)
	st, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			if log != nil {
				log.WithFields(logrus.Fields{"pid": srcPID, "mp": mp}).
					Warn("tarMount: source path missing, skipping")
			}
			return nil
		}
		return fmt.Errorf("stat src %s: %w", src, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("src %s is not a directory", src)
	}
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", outFile, err)
	}

	args := []string{
		"-C", src,
		"-cf", outFile,
		"--xattrs",
		"--acls",
		"--numeric-owner",
		"--sparse",
		"--one-file-system",
		".",
	}
	cmd := exec.Command("tar", args...) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar (-C %s): %w (stderr: %s)", src, err, stderr.String())
	}
	if log != nil {
		var size int64
		if fi, err := os.Stat(outFile); err == nil {
			size = fi.Size()
		}
		log.WithFields(logrus.Fields{
			"mp":    mp,
			"out":   outFile,
			"bytes": size,
		}).Info("Captured mount snapshot")
	}
	return nil
}

// untarIntoMntns extracts a tar produced by tarMount into the same path
// inside a placeholder pod's mount namespace. Pre-CRIU restore step:
// makes the captured /dev/shm files (etc.) visible at the same paths
// the source process recorded in the checkpoint, so CRIU's path-based
// file restoration finds them.
//
// The tar file path lives inside the agent's mntns (under
// /var/lib/nvsnap/checkpoints/...) and is NOT visible to nsenter'd
// processes inside the placeholder. We open the file in the agent and
// stream its bytes via stdin into a `tar -xf -` running inside the
// placeholder mntns — same pattern as mirrorIntoMntns.
//
// The placeholder is assumed to already have a mount at the same path
// (e.g., default K8s tmpfs at /dev/shm). We extract INTO that mount.
// If the path doesn't exist or isn't writable in the placeholder, the
// tar -x fails; per Q6 in the design the caller logs warn-and-continue.
//
// Returns nil if the tar file doesn't exist (no replay needed).
func untarIntoMntns(placeholderPID int, mp, tarFile string, log *logrus.Entry) error {
	if placeholderPID <= 0 || mp == "" || tarFile == "" {
		return fmt.Errorf("untarIntoMntns: empty pid/mp/tar")
	}
	f, err := os.Open(tarFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open tar %s: %w", tarFile, err)
	}
	defer func() { _ = f.Close() }()
	mntnsPath := fmt.Sprintf("/proc/%d/ns/mnt", placeholderPID)
	cmd := exec.Command("nsenter", "--mount="+mntnsPath, "--", //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		"tar",
		"-C", mp,
		"-xf", "-",
		"--xattrs",
		"--acls",
		"--numeric-owner",
		"--preserve-permissions",
		"--overwrite",
	)
	cmd.Stdin = f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nsenter+tar (pid=%d, mp=%s): %w (stderr: %s)",
			placeholderPID, mp, err, stderr.String())
	}
	if log != nil {
		log.WithFields(logrus.Fields{
			"pid": placeholderPID,
			"mp":  mp,
			"tar": tarFile,
		}).Info("Replayed mount snapshot into placeholder mntns")
	}
	return nil
}

// SanitizeMountPath converts an absolute mount path into a safe filename
// component for tar storage: leading "/" stripped, internal "/" → "_".
// /dev/shm → dev_shm, /var/run/nvsnap → var_run_nvsnap.
func SanitizeMountPath(mp string) string {
	mp = strings.TrimPrefix(mp, "/")
	return strings.ReplaceAll(mp, "/", "_")
}

// dirSizeBytes returns the cumulative apparent size of regular files under
// path. Best-effort; returns 0 on errors.
func dirSizeBytes(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

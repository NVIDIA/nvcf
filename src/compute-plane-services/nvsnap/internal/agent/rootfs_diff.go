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
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/criu/mountinfo"
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

// The checkpoint's copy of the source container's overlay upperdir. It is
// one archive, rootfsDiffArchive: the ways a checkpoint moves between nodes
// (a peer agent's fetch, the L2 copy) carry regular files' contents and at
// most their modes, so a tree lost what CRIU checks and the workload needs
// on a restore elsewhere: modes, owners, xattrs, whiteouts (seen
// 2026-10-21: a fetched etcd data file came back 0644, not 0600, and its
// restore failed). Checkpoints from before hold the tree in rootfsDiffDir.
const (
	rootfsDiffArchive = "rootfs-diff.tar"
	rootfsDiffDir     = "rootfs-diff"
)

// archiveOverlayDir writes the overlay directory src into the tar outFile,
// keeping everything CRIU's path-based file restoration and the workload
// depend on: modes, owners (numeric), symlinks, hardlinks, sparse holes,
// special files (overlayfs whiteouts are char devices), xattrs and ACLs.
// trusted.overlay.* xattrs are overlayfs's own and stay behind. The
// archive appears only once complete.
func archiveOverlayDir(src, outFile string, excludeMountpoints, excludeGlobs []string, log *logrus.Entry) error {
	if src == "" || outFile == "" {
		return fmt.Errorf("archiveOverlayDir: empty src or out")
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
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", outFile, err)
	}
	partial := outFile + ".partial"
	args := []string{
		"-C", src,
		"-cf", partial,
		"--xattrs",
		"--xattrs-exclude=trusted.overlay.*",
		"--acls",
		"--numeric-owner",
		"--sparse",
		"--one-file-system",
	}
	args = append(args, excludeArgsForMountpoints(excludeMountpoints)...)
	for _, g := range excludeGlobs {
		args = append(args, "--exclude="+g)
	}
	args = append(args, ".")
	cmd := exec.Command("tar", args...) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("tar (-C %s): %w (stderr: %s)", src, err, strings.TrimSpace(stderr.String()))
	}
	if err := os.Rename(partial, outFile); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("rename %s: %w", partial, err)
	}
	if log != nil {
		var size int64
		if fi, err := os.Stat(outFile); err == nil {
			size = fi.Size()
		}
		log.WithFields(logrus.Fields{
			"src":   src,
			"out":   outFile,
			"bytes": size,
		}).Info("Archived overlay diff")
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
// srcDiff is the checkpoint's rootfsDiffArchive, streamed as it is, or
// (checkpoints from before it) a rootfsDiffDir tree, archived on the fly.
// Either way the extract leaves out the placeholder's own mountpoints
// (excludeMountpoints) and the agent's directories. The target tar runs
// unprivileged from the placeholder's perspective; the agent's privilege
// provides the nsenter capability.
func mirrorIntoMntns(srcDiff string, placeholderPID int, excludeMountpoints []string, tools bundleTools, log *logrus.Entry) error {
	if srcDiff == "" || placeholderPID <= 0 {
		return fmt.Errorf("mirrorIntoMntns: empty src or invalid pid")
	}
	st, err := os.Stat(srcDiff)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat src %s: %w", srcDiff, err)
	}

	mntnsPath := fmt.Sprintf("/proc/%d/ns/mnt", placeholderPID)
	dstTar := exec.Command("nsenter", append([]string{"--mount=" + mntnsPath, "--", //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		tools.path("tar")}, replayDiffExtractArgs("/", excludeMountpoints)...)...)
	dstName := fmt.Sprintf("dst tar (nsenter pid=%d)", placeholderPID)
	if !st.IsDir() {
		f, err := os.Open(srcDiff)
		if err != nil {
			return fmt.Errorf("open %s: %w", srcDiff, err)
		}
		defer func() { _ = f.Close() }()
		dstTar.Stdin = f
		var stderr strings.Builder
		dstTar.Stderr = &stderr
		if err := dstTar.Run(); err != nil {
			return fmt.Errorf("%s: %w (stderr: %s)", dstName, err, strings.TrimSpace(stderr.String()))
		}
	} else {
		srcTar := exec.Command("tar", replayDiffSourceArgs(srcDiff, excludeMountpoints)...) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
		if err := runTarPipe(srcTar, dstTar, "src tar (-C "+srcDiff+")", dstName); err != nil {
			return err
		}
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
// (docs/ARCHITECTURE.md section 3) for replay-class mounts whose
// contents the placeholder pod cannot reproduce on its own (e.g.
// /dev/shm/psm_* NCCL segments for multi-GPU).
//
// Reads via /proc/<srcPID>/root/<mp> rather than nsenter so the agent
// stays in its own mntns; tar is purely a file-system operation that
// follows the proc magic-link. --one-file-system prevents accidental
// recursion into nested mounts (parity with archiveOverlayDir).
//
// Returns nil if the source path is missing or empty — a captured-but-
// empty mount on restore is the same as no capture (untar is a no-op).
// tarMount archives the mount at mp (as seen by srcPID) into outFile,
// leaving out the mounts nested in it (exclude: paths relative to mp). A
// nested mount is the restore pod's own volume (the model under nvsnap's
// cachedir); --one-file-system already leaves out its contents, but its
// mountpoint directory stayed in the archive, and extracting that over a
// read-only mount failed the whole replay.
func tarMount(srcPID int, mp, outFile string, exclude []string, log *logrus.Entry) error {
	if srcPID <= 0 || mp == "" || outFile == "" {
		return fmt.Errorf("tarMount: empty pid/mp/out")
	}
	src := fmt.Sprintf("/proc/%d/root%s", srcPID, mp)
	// The workload can write the mount and the directories above it: open
	// it under the container's root, so no component resolves outside it,
	// and have tar read from that directory (confined_fs.go).
	d, err := openWorkloadDir("/proc", srcPID, mp)
	if err != nil {
		if os.IsNotExist(err) {
			if log != nil {
				log.WithFields(logrus.Fields{"pid": srcPID, "mp": mp}).
					Warn("tarMount: source path missing, skipping")
			}
			return nil
		}
		return fmt.Errorf("open src %s: %w", src, err)
	}
	defer func() { _ = d.Close() }()
	dir, err := d.root.Open(".")
	if err != nil {
		return fmt.Errorf("open src %s: %w", src, err)
	}
	defer func() { _ = dir.Close() }()
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", outFile, err)
	}

	args := []string{
		"-C", "/proc/self/fd/3", // dir, the child's fd 3
		"-cf", outFile,
		"--xattrs",
		"--acls",
		"--numeric-owner",
		"--sparse",
		"--one-file-system",
	}
	for _, e := range exclude {
		args = append(args, "--exclude=./"+e)
	}
	args = append(args, ".")
	cmd := exec.Command("tar", args...) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
	cmd.ExtraFiles = []*os.File{dir}
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
func untarIntoMntns(ctx context.Context, placeholderPID int, mp, tarFile string, tools bundleTools, log *logrus.Entry) error {
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
	// The placeholder only has the mountpoints its own spec declares. A
	// replayed mount that was a volume on the source (the nvsnap cachedir at
	// /opt/nvsnap) has no directory here yet, and tar -C refuses a missing
	// one ("Cannot open: No such file or directory", 2026-10-04). Create
	// it on the placeholder's rootfs first; /dev/shm already exists.
	mkdir := exec.CommandContext(ctx, "nsenter", "--mount="+mntnsPath, "--", tools.path("mkdir"), "-p", mp) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
	if out, err := mkdir.CombinedOutput(); err != nil {
		return fmt.Errorf("nsenter+mkdir (pid=%d, mp=%s): %w (output: %s)", placeholderPID, mp, err, strings.TrimSpace(string(out)))
	}
	// The placeholder's own mounts under mp (the model volume under
	// nvsnap's cachedir) are its volumes, not the source's: never extract
	// over them. Archives from before tarMount left them out still carry
	// their mountpoint directory.
	var nested []string
	if mis, err := mountinfo.ParseMountinfo(mountinfo.ProcMountinfoPath(placeholderPID)); err == nil {
		var mps []string
		for _, m := range mis {
			mps = append(mps, m.MountPoint)
		}
		nested = nestedMountPoints(mps, mp)
	}
	cmd := exec.CommandContext(ctx, "nsenter", append([]string{"--mount=" + mntnsPath, "--", tools.path("tar")}, replayExtractArgs(mp, nested)...)...) //nolint:gosec // args are internally constructed (PIDs/paths), not user input
	cmd.Stdin = f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nsenter+tar (pid=%d, mp=%s): %w (stderr: %s)",
			placeholderPID, mp, err, stderr.String())
	}
	n, err := verifyReplayed(placeholderPID, mp, tarFile, nested)
	if err != nil {
		return err
	}
	if log != nil {
		log.WithFields(logrus.Fields{
			"pid":     placeholderPID,
			"mp":      mp,
			"tar":     tarFile,
			"entries": n,
		}).Info("Replayed mount snapshot into placeholder mntns")
	}
	return nil
}

// replayExtractArgs is the tar command line that extracts a replay archive
// from stdin into mp, leaving out the placeholder's nested mounts (relative to
// mp). The excludes are anchored at the archive's root: unanchored, GNU tar
// matches "hf" at any depth and drops every directory of that name, such as
// the inductor cache's hf/ under the cachedir (seen 2026-10-17), and the
// replay check then fails the restore.
func replayExtractArgs(mp string, nested []string) []string {
	args := []string{"-C", mp, "-xf", "-", "--xattrs", "--acls", "--numeric-owner", "--preserve-permissions", "--overwrite"}
	if len(nested) > 0 {
		args = append(args, "--anchored", "--no-wildcards")
	}
	for _, e := range nested {
		args = append(args, "--exclude=./"+e, "--exclude="+e)
	}
	return args
}

// underAny reports whether the relative path name is one of dirs or under one.
func underAny(name string, dirs []string) bool {
	for _, d := range dirs {
		if name == d || strings.HasPrefix(name, d+"/") {
			return true
		}
	}
	return false
}

// nestedMountPoints returns the mountpoints strictly under mp, relative to
// it.
func nestedMountPoints(mountPoints []string, mp string) []string {
	var out []string
	prefix := strings.TrimSuffix(mp, "/") + "/"
	for _, m := range mountPoints {
		if strings.HasPrefix(m, prefix) {
			out = append(out, strings.TrimPrefix(m, prefix))
		}
	}
	return out
}

// verifyReplayed checks that every entry of the archive tarFile exists under
// mp in the placeholder's mount namespace (through /proc/<pid>/root), and
// returns how many it checked. CRIU re-opens the workload's files by path and
// re-creates its deleted ones next to their old path: an entry missing here
// fails the restore inside CRIU, far from its cause.
func verifyReplayed(placeholderPID int, mp, tarFile string, skip []string) (int, error) {
	f, err := os.Open(tarFile)
	if err != nil {
		return 0, fmt.Errorf("reopen %s: %w", tarFile, err)
	}
	defer func() { _ = f.Close() }()
	root := filepath.Join(hostProcBase(), strconv.Itoa(placeholderPID), "root", mp)
	tr := tar.NewReader(f)
	var n int
	var missing []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, fmt.Errorf("read %s: %w", tarFile, err)
		}
		name := filepath.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." || underAny(name, skip) {
			continue
		}
		n++
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		shown := missing
		if len(shown) > 10 {
			shown = shown[:10]
		}
		return n, fmt.Errorf("replay of %s incomplete: %d of %d entries missing in the placeholder, e.g. %s",
			mp, len(missing), n, strings.Join(shown, ", "))
	}
	return n, nil
}

// SanitizeMountPath converts an absolute mount path into a safe filename
// component for tar storage: leading "/" stripped, internal "/" → "_".
// /dev/shm → dev_shm, /var/run/nvsnap → var_run_nvsnap.
func SanitizeMountPath(mp string) string {
	mp = strings.TrimPrefix(mp, "/")
	return strings.ReplaceAll(mp, "/", "_")
}

// agentContainerDirs are the directories the agent stages into a
// container; a replay never writes them.
var agentContainerDirs = []string{v2BinDirInContainer, v2ImagesDirInContainer}

// runTarPipe runs src | dst and waits for both. The agent keeps no end of
// the pipe: a dst that exits early leaves src a broken pipe, not a full
// one it would block on forever (seen 2026-10-18: a restore hung on a
// replay whose extracting tar had died). dst's failure is reported first:
// src's broken pipe follows from it.
func runTarPipe(src, dst *exec.Cmd, srcName, dstName string) error {
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("tar pipe: %w", err)
	}
	src.Stdout, dst.Stdin = w, r
	var srcErr, dstErr strings.Builder
	src.Stderr, dst.Stderr = &srcErr, &dstErr
	if err := dst.Start(); err != nil {
		r.Close()
		w.Close()
		return fmt.Errorf("start %s: %w", dstName, err)
	}
	if err := src.Start(); err != nil {
		r.Close()
		w.Close()
		_ = dst.Wait()
		return fmt.Errorf("start %s: %w", srcName, err)
	}
	r.Close()
	w.Close()
	dstWait := dst.Wait()
	srcWait := src.Wait()
	if dstWait != nil {
		return fmt.Errorf("%s: %w (stderr: %s)", dstName, dstWait, lastLines(dstErr.String(), 8))
	}
	if srcWait != nil {
		return fmt.Errorf("%s: %w (stderr: %s)", srcName, srcWait, lastLines(srcErr.String(), 8))
	}
	return nil
}

// replayDiffSourceArgs is the tar that reads a captured diff for a replay.
// replayDiffExtractArgs is the tar command line that extracts a rootfs diff
// from stdin into root, leaving out the placeholder's mountpoints and the
// agent's directories the same way replayDiffSourceArgs does when it
// archives a diff tree.
func replayDiffExtractArgs(root string, excludeMountpoints []string) []string {
	args := []string{
		"-C", root,
		"-xf", "-",
		"--xattrs",
		"--xattrs-exclude=trusted.overlay.*",
		"--acls",
		"--numeric-owner",
		"--preserve-permissions",
		"--overwrite",
	}
	args = append(args, excludeArgsForMountpoints(excludeMountpoints)...)
	return append(args, excludeArgsForMountpoints(agentContainerDirs)...)
}

func replayDiffSourceArgs(srcDiff string, excludeMountpoints []string) []string {
	args := []string{
		"-C", srcDiff,
		"-cf", "-",
		"--xattrs",
		"--xattrs-exclude=trusted.overlay.*",
		"--acls",
		"--numeric-owner",
		"--sparse",
		"--one-file-system",
	}
	args = append(args, excludeArgsForMountpoints(excludeMountpoints)...)
	// The agent's own directories in the container are never the
	// workload's: a capture taken before they were left out of the diff
	// still carries them, and replaying the bundle would overwrite the
	// libraries of the tar extracting it, which runs from there (seen
	// 2026-10-18: SIGBUS).
	args = append(args, excludeArgsForMountpoints(agentContainerDirs)...)
	return append(args, ".")
}

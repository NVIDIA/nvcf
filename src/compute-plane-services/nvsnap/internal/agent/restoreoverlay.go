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

// OverlayFS-backed rootfs restore (nvsnap#194).
//
// The rootfs capture path snapshots a curated set of cache directories
// from the source pod's overlay upperdir and serves them to restore
// pods via webhook-injected hostPath bind mounts. Today those mounts
// are read-only — vLLM/sglang/etc. that write into the captured tree
// at runtime crash with EROFS.
//
// This module lets the agent prepare, per restore pod, a writable
// OverlayFS union on top of the read-only captured tree. The union
// is mounted on the host and bind-mounted into the restore pod via
// hostPath; the upper layer is ephemeral per-pod scratch.
//
// Tier resolution (hash → lowerDir) lives in the HTTP handler that
// calls Prepare; this module is intentionally a leaf concerned only
// with mount lifecycle so it stays testable without checkpointstore.
//
// See docs/proposals/rootfs-restore-overlayfs.md for the full design.

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/sirupsen/logrus"
)

// Mounter abstracts mount/unmount so unit tests don't need CAP_SYS_ADMIN.
// Production uses syscallMounter; tests inject a recorder.
type Mounter interface {
	Mount(target, lower, upper, work string) error
	Unmount(target string) error
}

type syscallMounter struct{}

func (syscallMounter) Mount(target, lower, upper, work string) error {
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work)
	return syscall.Mount("overlay", target, "overlay", 0, opts)
}

func (syscallMounter) Unmount(target string) error {
	// MNT_DETACH so a pod that holds the bind-mount busy still lets us
	// detach the host-side overlay; cleanup of upper/work proceeds.
	return syscall.Unmount(target, syscall.MNT_DETACH)
}

// OverlayManager owns the per-pod overlay scratch tree under Root.
//
//	Root/
//	  <pod-uid>/
//	    merged/<extract-path>/   <-- the mountpoint, bind-mounted into the pod
//	    upper/<extract-path>/
//	    work/<extract-path>/
type OverlayManager struct {
	Root    string
	Log     *logrus.Logger
	Mounter Mounter

	mu     sync.Mutex
	mounts map[string]map[string]string // pod-uid -> extractPath -> mountpoint
}

// NewOverlayManager constructs an OverlayManager. Root defaults to
// /var/lib/nvsnap/overlays. Pass a nil Mounter to use the real syscall
// implementation.
func NewOverlayManager(root string, log *logrus.Logger, mounter Mounter) *OverlayManager {
	if root == "" {
		root = "/var/lib/nvsnap/overlays"
	}
	if log == nil {
		log = logrus.StandardLogger()
	}
	if mounter == nil {
		mounter = syscallMounter{}
	}
	return &OverlayManager{
		Root:    root,
		Log:     log,
		Mounter: mounter,
		mounts:  make(map[string]map[string]string),
	}
}

// Prepare mounts an OverlayFS union for (podUID, extractPath) with the
// given lowerDir, and returns the merged mountpoint to bind into the pod.
//
// Idempotent: a second call with identical args returns the same path
// without re-mounting. lowerDir must be absolute and must exist.
// extractPath must be absolute (e.g., "/root/.cache/vllm") and must
// not contain "..".
func (m *OverlayManager) Prepare(podUID, lowerDir, extractPath string) (string, error) {
	if err := validatePodUID(podUID); err != nil {
		return "", err
	}
	if err := validateExtractPath(extractPath); err != nil {
		return "", err
	}
	if !filepath.IsAbs(lowerDir) {
		return "", fmt.Errorf("lowerDir must be absolute: %q", lowerDir)
	}
	if st, err := os.Stat(lowerDir); err != nil {
		return "", fmt.Errorf("lowerDir %q: %w", lowerDir, err)
	} else if !st.IsDir() {
		return "", fmt.Errorf("lowerDir %q is not a directory", lowerDir)
	}

	relExtract := strings.TrimPrefix(extractPath, "/")
	merged := filepath.Join(m.Root, podUID, "merged", relExtract)
	upper := filepath.Join(m.Root, podUID, "upper", relExtract)
	work := filepath.Join(m.Root, podUID, "work", relExtract)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Idempotency: same pod-uid + extract → return the existing mount.
	if byExtract, ok := m.mounts[podUID]; ok {
		if existing, ok := byExtract[extractPath]; ok {
			m.Log.WithFields(logrus.Fields{
				"podUID":      podUID,
				"extractPath": extractPath,
				"mountpoint":  existing,
			}).Debug("overlay already prepared, returning existing mountpoint")
			return existing, nil
		}
	}

	for _, d := range []string{merged, upper, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %q: %w", d, err)
		}
	}

	if err := m.Mounter.Mount(merged, lowerDir, upper, work); err != nil {
		return "", fmt.Errorf("overlay mount %q (lower=%q): %w", merged, lowerDir, err)
	}

	if m.mounts[podUID] == nil {
		m.mounts[podUID] = make(map[string]string)
	}
	m.mounts[podUID][extractPath] = merged

	m.Log.WithFields(logrus.Fields{
		"podUID":      podUID,
		"extractPath": extractPath,
		"lowerDir":    lowerDir,
		"mountpoint":  merged,
	}).Info("prepared overlay")

	return merged, nil
}

// Cleanup unmounts every overlay for the given pod key (UID or ns_name —
// the webhook keys by ns_name on CREATE since pod.UID is empty at
// admission time, per the comment on overlayKeyFor in
// internal/webhook/mutate.go) and removes its scratch tree.
//
// Filesystem-driven: parses /proc/self/mountinfo to discover what is
// actually mounted under <Root>/<key>/, instead of relying on the
// in-memory m.mounts map. The in-memory map is empty across agent
// restarts (we redeploy v0.X→v0.Y while restore pods are still
// running), so a Cleanup that only iterates m.mounts would silently
// skip the unmount and leave the overlay live on the host — which is
// exactly what we observed for nvsnap-system_bench-whisper-rootfs-restored
// surviving the v0.0.42→v0.0.45 rollouts on 2026-06-08.
//
// Safe to call repeatedly; missing key is a no-op.
func (m *OverlayManager) Cleanup(key string) error {
	if err := validatePodUID(key); err != nil {
		return err
	}

	// Drop the in-memory tracking entry (best-effort; may be empty
	// post-restart). Snapshot the entries BEFORE deleting so we can
	// unmount them. We do the delete first so concurrent Prepare calls
	// don't race against us.
	m.mu.Lock()
	byExtract := m.mounts[key]
	delete(m.mounts, key)
	m.mu.Unlock()

	podRoot := filepath.Join(m.Root, key)
	var firstErr error

	// Two sources of truth, unioned:
	//   1. In-memory m.mounts entries (what we created in this agent
	//      process). Lets unit tests assert via recordingMounter.
	//   2. /proc/self/mountinfo entries under podRoot. Required for
	//      post-restart cleanup — m.mounts is empty after an agent
	//      rollout but the kernel still holds the overlay mount.
	mounts := map[string]struct{}{}
	for _, mp := range byExtract {
		mounts[mp] = struct{}{}
	}
	if discovered, err := discoverLiveOverlayMounts(podRoot); err != nil {
		m.Log.WithError(err).WithField("podRoot", podRoot).
			Warn("discover overlay mounts failed; relying on in-memory list only")
		if firstErr == nil {
			firstErr = err
		}
	} else {
		for _, mp := range discovered {
			mounts[mp] = struct{}{}
		}
	}
	// Sort longest-first so nested mounts unmount before their parents.
	ordered := make([]string, 0, len(mounts))
	for mp := range mounts {
		ordered = append(ordered, mp)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })

	for _, mountpoint := range ordered {
		if err := m.Mounter.Unmount(mountpoint); err != nil {
			m.Log.WithError(err).WithFields(logrus.Fields{
				"key":        key,
				"mountpoint": mountpoint,
			}).Warn("overlay unmount failed; will still rm scratch tree")
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	if err := os.RemoveAll(podRoot); err != nil {
		m.Log.WithError(err).WithField("podRoot", podRoot).Warn("rm scratch tree failed")
		if firstErr == nil {
			firstErr = err
		}
	}

	m.Log.WithFields(logrus.Fields{
		"key":      key,
		"unmounts": len(ordered),
	}).Info("overlay cleaned up")
	return firstErr
}

// discoverLiveOverlayMounts reads /proc/self/mountinfo and returns every
// mountpoint whose target is at-or-under podRoot (which is
// <m.Root>/<key>). Empty list when podRoot doesn't exist or has no
// mounts under it. Caller decides what to do with the list.
//
// We use mountinfo rather than /proc/mounts because mountinfo is the
// canonical ordered representation (mountinfo(5)), and field 5 is the
// mountpoint as an absolute path — easy to filter by prefix.
func discoverLiveOverlayMounts(podRoot string) ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read mountinfo: %w", err)
	}
	prefix := strings.TrimRight(podRoot, "/") + "/"
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		// mountinfo format: id parent major:minor root mountpoint opts ...
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mp := fields[4]
		if mp == podRoot || strings.HasPrefix(mp, prefix) {
			out = append(out, mp)
		}
	}
	// Sort longest-first so we unmount nested mounts before their parents.
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out, nil
}

// Sweep walks Root and Cleanups any per-pod directory whose key is not
// in activeKeys (a set that may contain UIDs OR ns_name values — the
// caller mixes the two because the webhook keys by ns_name on CREATE
// admission, but the pod-delete watcher learns both forms).
//
// Intended for agent startup recovery from crashes that left mounts
// behind, and for periodic GC of leaked overlays. Filesystem-driven
// so it cleans up even mounts the current agent process didn't create.
func (m *OverlayManager) Sweep(activeKeys map[string]struct{}) error {
	entries, err := os.ReadDir(m.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read overlay root: %w", err)
	}

	var firstErr error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		key := e.Name()
		if _, alive := activeKeys[key]; alive {
			continue
		}
		if err := m.Cleanup(key); err != nil {
			m.Log.WithError(err).WithField("key", key).Warn("sweep cleanup failed")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// MountpointFor returns the merged path the manager would mount for the
// given pod+extract path. Exposed for the webhook so it can build the
// hostPath volume without having to know our layout convention.
func (m *OverlayManager) MountpointFor(podUID, extractPath string) (string, error) {
	if err := validatePodUID(podUID); err != nil {
		return "", err
	}
	if err := validateExtractPath(extractPath); err != nil {
		return "", err
	}
	return filepath.Join(m.Root, podUID, "merged", strings.TrimPrefix(extractPath, "/")), nil
}

func validatePodUID(uid string) error {
	if uid == "" {
		return fmt.Errorf("podUID must not be empty")
	}
	if strings.ContainsAny(uid, "/\\") || strings.Contains(uid, "..") {
		return fmt.Errorf("podUID %q contains illegal characters", uid)
	}
	return nil
}

func validateExtractPath(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("extractPath %q must be absolute", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("extractPath %q must not contain parent traversal", p)
	}
	return nil
}

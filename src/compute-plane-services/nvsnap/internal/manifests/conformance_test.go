/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Conformance checks over the workload manifests.
//
// These exist because the criu-v2 migration was applied per-manifest by hand
// and silently missed three workloads. Nothing detected the drift, so two
// separate incidents (a GLIBC_2.38 load failure and a dump that hung before
// finishing seize) were chased as workload-specific bugs when both were the
// same unmigrated convention. A convention that lives in 28 copies needs a
// check that all 28 still agree.
package manifests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// criu-v2 nsenters CRIU into the container's mount namespace, and
// /etc/ld.so.preload is a property of that namespace rather than of the
// workload process. Anything listed there is force-loaded into CRIU itself,
// which is fatal: it wedges the dump before seize completes. The legacy engine
// ran CRIU from the agent's namespace and never saw the file, which is why
// these manifests used to work. Scope interception to the process (LD_PRELOAD
// on the workload's own env) or, preferably, do not inject at all.
func TestNoManifestWritesLdSoPreload(t *testing.T) {
	for _, path := range manifests(t) {
		body := read(t, path)
		if strings.Contains(body, "ld.so.preload") {
			t.Errorf("%s writes /etc/ld.so.preload: it is mount-namespace wide, so "+
				"criu-v2 force-loads the named library into CRIU and the dump hangs",
				filepath.Base(path))
		}
	}
}

// CRIU resolves several file checks by path against the restored rootfs, so a
// placeholder running a different image than the source can fail a restore in
// ways that surface far from the cause.
func TestRestoreImageMatchesSource(t *testing.T) {
	for _, path := range manifests(t) {
		base := filepath.Base(path)
		if !strings.HasSuffix(base, "-restore.yaml") {
			continue
		}
		// rootfs-path placeholders are built from the capture manifest, not
		// from a sibling source pod, so the pairing does not apply.
		if strings.Contains(base, "rootfs") {
			continue
		}
		srcPath := filepath.Join(filepath.Dir(path),
			strings.TrimSuffix(base, "-restore.yaml")+".yaml")
		if _, err := os.Stat(srcPath); err != nil {
			continue
		}
		src, dst := image(read(t, srcPath)), image(read(t, path))
		if src == "" || dst == "" {
			continue
		}
		if src != dst {
			t.Errorf("%s restores into %q but the source pod runs %q; CRIU's "+
				"path-based checks need an identical rootfs", base, dst, src)
		}
	}
}

// The v2 restore path is a dumb placeholder: the agent drives the restore over
// its API and runs CRIU itself. A placeholder that execs restore-entrypoint is
// still on the legacy engine.
func TestV2RestoreHasNoRestoreEntrypoint(t *testing.T) {
	for _, path := range manifests(t) {
		base := filepath.Base(path)
		if !strings.HasSuffix(base, "-restore.yaml") || strings.Contains(base, "rootfs") {
			continue
		}
		if strings.Contains(read(t, path), "restore-entrypoint") {
			t.Errorf("%s execs restore-entrypoint, which the criu-v2 engine does not use",
				base)
		}
	}
}

// workloadDir is repo-relative: the manifests are data, not Go sources, so
// this package holds only the contract check over them.
const workloadDir = "../../deploy/k8s/workloads"

// Every manifest must carry apiVersion and kind. Trivially true when written
// by hand, easy to lose when a manifest is edited programmatically -- which is
// how it was lost once already, surfacing only as a kubectl validation error
// mid-e2e rather than at build time.
func TestManifestsAreWellFormed(t *testing.T) {
	for _, path := range manifests(t) {
		body := read(t, path)
		for _, required := range []string{"apiVersion:", "kind:"} {
			if !strings.Contains(body, required) {
				t.Errorf("%s is missing %s", filepath.Base(path), required)
			}
		}
	}
}

func manifests(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(workloadDir, "*.yaml"))
	if err != nil {
		t.Fatalf("glob manifests: %v", err)
	}
	if len(found) == 0 {
		t.Fatalf("no workload manifests found under %s", workloadDir)
	}
	return found
}

// read returns the manifest with comment lines removed. Both YAML and the
// embedded shell blocks comment with '#', so one rule covers both. Without
// this the checks match their own explanatory comments -- the prose
// describing why a manifest must not do something looks exactly like the
// manifest doing it.
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // fixed set of in-repo manifests
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var kept []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// image returns the first container image in the manifest. Good enough here:
// these are single-workload pods, and init containers are being removed.
func image(body string) string {
	for _, line := range strings.Split(body, "\n") {
		f := strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(f, "image:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// A malformed restore-failure-threshold must be rejected rather than silently
// falling back to the source's value, which would make a restore give up early
// and present as a workload failure.
func TestRestoreThresholdRejectsBadValues(t *testing.T) {
	base := `apiVersion: v1
kind: Pod
metadata:
  name: w
  namespace: nvsnap-system
  annotations:
    nvsnap.io/path: "criu"
    nvsnap.io/port: "8000"
    nvsnap.io/restore-failure-threshold: %q
spec:
  containers:
    - name: c
      image: img:1
      args: ["nohup setsid srv > /w.out 2>&1 < /dev/null &"]
`
	for _, bad := range []string{"abc", "0", "-5", "1.5", ""} {
		src := []byte(fmt.Sprintf(base, bad))
		_, err := RenderRestore(src)
		if bad == "" {
			if err != nil {
				t.Errorf("empty threshold should fall through to the default, got %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("threshold %q accepted; want an error naming the annotation", bad)
			continue
		}
		// Any unrelated render error would otherwise satisfy this test.
		if !strings.Contains(err.Error(), "restore-failure-threshold") {
			t.Errorf("threshold %q rejected by an unrelated error: %v", bad, err)
		}
	}
	// A good value must actually reach the manifest, not merely render.
	out, err := RenderRestore([]byte(fmt.Sprintf(base, "120")))
	if err != nil {
		t.Fatalf("valid threshold rejected: %v", err)
	}
	if !strings.Contains(string(out), "failureThreshold: 120") {
		t.Error("threshold 120 was accepted but not applied to the placeholder")
	}
}

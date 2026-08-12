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

package rootfsonly

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProc creates a temporary /proc-like layout. pids[pid] = cgroup file content.
// Non-PID dirs and PID dirs without a cgroup file are also created to cover the
// "skip non-PID entries" and "transient ENOENT" paths.
func fakeProc(t *testing.T, pids map[int]string) string {
	return fakeProcWithComm(t, pids, nil)
}

// fakeProcWithComm is fakeProc with an optional pid → /proc/<pid>/comm
// content map. Used to exercise the sandbox-PID skip logic.
func fakeProcWithComm(t *testing.T, pids, comms map[int]string) string {
	t.Helper()
	root := t.TempDir()
	for pid, cgroup := range pids {
		dir := filepath.Join(root, fmt.Sprintf("%d", pid))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if cgroup != "" {
			if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(cgroup), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if comm, ok := comms[pid]; ok {
			if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Mkdir(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolvePodPID_CgroupV1(t *testing.T) {
	uid := "a082a22a-2f66-4f4e-b80c-d2fc8aa4a010"
	cg := fmt.Sprintf("0::/kubepods/burstable/pod%s/c001/process\n", uid)
	root := fakeProc(t, map[int]string{
		1234: cg,
		2345: cg,
		// Higher PID, also in the pod — should NOT win (lowest PID wins).
		9999: cg,
		// Different pod, not in the target — should be ignored.
		3333: "0::/kubepods/burstable/poddeadbeef-0000-0000-0000-000000000000/c002/process\n",
	})
	r := &PIDResolver{ProcRoot: root}
	pid, err := r.ResolvePodPID(uid)
	if err != nil {
		t.Fatalf("ResolvePodPID: %v", err)
	}
	if pid != 1234 {
		t.Fatalf("got pid=%d, want 1234 (lowest in target pod)", pid)
	}
}

func TestResolvePodPID_CgroupV2SystemdSlice(t *testing.T) {
	uid := "a082a22a-2f66-4f4e-b80c-d2fc8aa4a010"
	uidUnderscored := "a082a22a_2f66_4f4e_b80c_d2fc8aa4a010"
	cg := fmt.Sprintf(
		"0::/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod%s.slice/cri-containerd-abc.scope\n",
		uidUnderscored,
	)
	root := fakeProc(t, map[int]string{
		5000: cg,
	})
	r := &PIDResolver{ProcRoot: root}
	pid, err := r.ResolvePodPID(uid)
	if err != nil {
		t.Fatalf("ResolvePodPID: %v", err)
	}
	if pid != 5000 {
		t.Fatalf("got pid=%d, want 5000", pid)
	}
}

func TestResolvePodPID_NotRunning(t *testing.T) {
	root := fakeProc(t, map[int]string{
		7777: "0::/kubepods/.../poddeadbeef-0000-0000-0000-000000000000/c001/process\n",
	})
	r := &PIDResolver{ProcRoot: root}
	_, err := r.ResolvePodPID("a082a22a-2f66-4f4e-b80c-d2fc8aa4a010")
	if !errors.Is(err, ErrPodNotRunning) {
		t.Fatalf("got %v, want ErrPodNotRunning", err)
	}
}

func TestResolvePodPID_EmptyUID(t *testing.T) {
	r := &PIDResolver{ProcRoot: t.TempDir()}
	_, err := r.ResolvePodPID("")
	if err == nil {
		t.Fatal("expected error for empty UID")
	}
}

func TestResolvePodPID_BadProcRoot(t *testing.T) {
	r := &PIDResolver{ProcRoot: "/nonexistent/proc/root/that/does/not/exist"}
	_, err := r.ResolvePodPID("anything")
	if err == nil {
		t.Fatal("expected error for nonexistent proc root")
	}
	if errors.Is(err, ErrPodNotRunning) {
		t.Fatalf("nonexistent proc root should not return ErrPodNotRunning, got %v", err)
	}
}

func TestResolvePodPID_TransientENOENT(t *testing.T) {
	uid := "a082a22a-2f66-4f4e-b80c-d2fc8aa4a010"
	cg := fmt.Sprintf("0::/kubepods/burstable/pod%s/c001/process\n", uid)
	// PID 1111 has no cgroup file (simulates a process exiting between
	// readdir and open). PID 2222 should still resolve.
	root := fakeProc(t, map[int]string{
		1111: "", // dir exists but no cgroup file
		2222: cg,
	})
	r := &PIDResolver{ProcRoot: root}
	pid, err := r.ResolvePodPID(uid)
	if err != nil {
		t.Fatalf("ResolvePodPID: %v", err)
	}
	if pid != 2222 {
		t.Fatalf("got pid=%d, want 2222", pid)
	}
}

func TestResolvePodPID_IgnoresNonPIDEntries(t *testing.T) {
	uid := "a082a22a-2f66-4f4e-b80c-d2fc8aa4a010"
	cg := fmt.Sprintf("0::/kubepods/burstable/pod%s/c001/process\n", uid)
	root := fakeProc(t, map[int]string{
		4242: cg,
	})
	// fakeProc already adds a "self" entry that's not a PID.
	r := &PIDResolver{ProcRoot: root}
	pid, err := r.ResolvePodPID(uid)
	if err != nil {
		t.Fatalf("ResolvePodPID: %v", err)
	}
	if pid != 4242 {
		t.Fatalf("got pid=%d, want 4242", pid)
	}
}

// TestResolvePodPID_SkipsPauseSandbox is the regression test for the bug
// where the K8s pause container's PID (always lowest in the pod cgroup)
// was returned, leading to a 0-file capture (pause's upperdir is empty).
func TestResolvePodPID_SkipsPauseSandbox(t *testing.T) {
	uid := "172ee0ff-8ae5-4baf-9de5-51a6bde95822"
	cg := fmt.Sprintf("0::/kubepods/burstable/pod%s/c001/process\n", uid)
	root := fakeProcWithComm(t,
		map[int]string{
			1000: cg, // pause sandbox (lowest PID, but empty upperdir)
			1500: cg, // bash entrypoint
			1700: cg, // vllm worker
		},
		map[int]string{
			1000: "pause",
			1500: "bash",
			1700: "vllm",
		},
	)
	r := &PIDResolver{ProcRoot: root}
	pid, err := r.ResolvePodPID(uid)
	if err != nil {
		t.Fatalf("ResolvePodPID: %v", err)
	}
	if pid != 1500 {
		t.Fatalf("got pid=%d, want 1500 (lowest non-sandbox PID; pause should be skipped)", pid)
	}
}

func TestResolvePodPID_SkipsContainerdShim(t *testing.T) {
	uid := "172ee0ff-8ae5-4baf-9de5-51a6bde95822"
	cg := fmt.Sprintf("0::/kubepods.slice/.../pod%s.slice/cri-containerd-x.scope\n",
		strings.ReplaceAll(uid, "-", "_"))
	root := fakeProcWithComm(t,
		map[int]string{
			900:  cg, // containerd-shim-runc-v2 (low PID, sandbox)
			2000: cg, // workload
		},
		map[int]string{
			900:  "containerd-shim-runc-v2",
			2000: "vllm",
		},
	)
	r := &PIDResolver{ProcRoot: root}
	pid, err := r.ResolvePodPID(uid)
	if err != nil {
		t.Fatalf("ResolvePodPID: %v", err)
	}
	if pid != 2000 {
		t.Fatalf("got pid=%d, want 2000 (containerd-shim should be skipped)", pid)
	}
}

func TestResolvePodPID_AllSandboxIsNotRunning(t *testing.T) {
	uid := "172ee0ff-8ae5-4baf-9de5-51a6bde95822"
	cg := fmt.Sprintf("0::/kubepods/burstable/pod%s/c001/process\n", uid)
	root := fakeProcWithComm(t,
		map[int]string{1000: cg},
		map[int]string{1000: "pause"},
	)
	r := &PIDResolver{ProcRoot: root}
	_, err := r.ResolvePodPID(uid)
	if !errors.Is(err, ErrPodNotRunning) {
		t.Fatalf("got %v, want ErrPodNotRunning when only sandbox PIDs exist", err)
	}
}

func TestNewPIDResolver_DefaultsToProc(t *testing.T) {
	r := NewPIDResolver()
	if r.ProcRoot != "/proc" && r.ProcRoot != "/host/proc" {
		t.Fatalf("ProcRoot = %q, want /proc or /host/proc", r.ProcRoot)
	}
}

// The bug in nvsnap#788: capture read the entrypoint from the pod-scoped PID,
// which is whichever container started first. On an NVCF function pod the
// `utils` sidecar starts while the main container is still pulling its image,
// so the sidecar always had the lower PID and capture recorded ITS argv.
// Restore then exec'd a binary that does not exist in the main container.
//
// Models that exact layout: sidecar PID 100, main container PID 900, same pod.
func TestResolveContainerPID_PicksTheNamedContainerNotTheLowestPodPID(t *testing.T) {
	const (
		podUID  = "0b4b630f-72b9-4a3b-884a-d630844351c7"
		sidecar = "1111111111111111111111111111111111111111111111111111111111111111"
		mainCtr = "2222222222222222222222222222222222222222222222222222222222222222"
		cgFmt   = "0::/kubepods.slice/kubepods-besteffort.slice/pod%s.slice/cri-containerd-%s.scope"
	)
	uidUnderscored := strings.ReplaceAll(podUID, "-", "_")
	root := fakeProc(t, map[int]string{
		100: fmt.Sprintf(cgFmt, uidUnderscored, sidecar), // starts first -> lowest PID
		900: fmt.Sprintf(cgFmt, uidUnderscored, mainCtr), // main container
	})
	r := &PIDResolver{ProcRoot: root}

	// Pod-scoped resolution returns the sidecar. This is correct for its
	// documented purpose (upperdir) and wrong for the entrypoint.
	if got, err := r.ResolvePodPID(podUID); err != nil || got != 100 {
		t.Fatalf("ResolvePodPID = %d, %v; want 100 (documents the pre-fix behavior)", got, err)
	}

	// Container-scoped resolution must return the main container's PID.
	got, err := r.ResolveContainerPID(mainCtr)
	if err != nil {
		t.Fatalf("ResolveContainerPID: %v", err)
	}
	if got != 900 {
		t.Errorf("ResolveContainerPID = %d, want 900 (the main container, not the lowest pod PID)", got)
	}
}

func TestResolveContainerPID_SkipsSandbox(t *testing.T) {
	const ctr = "3333333333333333333333333333333333333333333333333333333333333333"
	cg := "0::/kubepods.slice/pod_x.slice/cri-containerd-" + ctr + ".scope"
	root := fakeProcWithComm(t,
		map[int]string{10: cg, 42: cg},
		map[int]string{10: "pause"}, // sandbox must not win despite lower PID
	)
	r := &PIDResolver{ProcRoot: root}
	got, err := r.ResolveContainerPID(ctr)
	if err != nil {
		t.Fatalf("ResolveContainerPID: %v", err)
	}
	if got != 42 {
		t.Errorf("ResolveContainerPID = %d, want 42 (pause at PID 10 must be skipped)", got)
	}
}

// Unknown/exited container must be a clean miss so the orchestrator can fall
// back to the pod PID rather than recording nothing.
func TestResolveContainerPID_NotFoundAndEmpty(t *testing.T) {
	r := &PIDResolver{ProcRoot: fakeProc(t, map[int]string{7: "0::/kubepods.slice/other.scope"})}
	if _, err := r.ResolveContainerPID("deadbeef"); !errors.Is(err, ErrPodNotRunning) {
		t.Errorf("unknown container: err = %v, want ErrPodNotRunning", err)
	}
	if _, err := r.ResolveContainerPID(""); err == nil {
		t.Error("empty containerID: want an error")
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func findingFor(fs []PreflightFinding, check string) (PreflightFinding, bool) {
	for _, f := range fs {
		if f.Check == check {
			return f, true
		}
	}
	return PreflightFinding{}, false
}

func TestPodPreflight(t *testing.T) {
	a := &Agent{config: Config{Webhook: WebhookConfig{CaptureLivenessBudget: 300}}}
	share, always := true, corev1.ContainerRestartPolicyAlways
	execProbe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"check"}}}, PeriodSeconds: 10, FailureThreshold: 30}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "w"}, Spec: corev1.PodSpec{
		ShareProcessNamespace: &share,
		InitContainers:        []corev1.Container{{Name: "beacon", RestartPolicy: &always}},
		Containers: []corev1.Container{
			{Name: "main", ReadinessProbe: execProbe, Env: []corev1.EnvVar{{Name: "LD_PRELOAD", Value: webhook.GPUShareLibPath}}},
			{Name: "sidecar", LivenessProbe: execProbe},
		},
	}}
	fs := a.podPreflight(pod, "main", true)
	if len(blocking(fs)) != 0 {
		t.Fatalf("no blocking findings expected: %+v", fs)
	}
	for check, sev := range map[string]string{"shared-pid-namespace": preflightInfo, "native-sidecar": preflightInfo} {
		if f, ok := findingFor(fs, check); !ok || f.Severity != sev {
			t.Errorf("%s: %+v %v", check, f, ok)
		}
	}
	var mainExec, sideExec string
	for _, f := range fs {
		if f.Check == "exec-probe" && f.Container == "main" {
			mainExec = f.Severity
		}
		if f.Check == "exec-probe" && f.Container == "sidecar" {
			sideExec = f.Severity
		}
	}
	if mainExec != preflightWarn || sideExec != preflightInfo {
		t.Errorf("exec probes: dumped container %q, sidecar %q; want warn, info", mainExec, sideExec)
	}

	// A rank without gpushare, a probe too tight for the capture.
	tight := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "w2"}, Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "main", LivenessProbe: &corev1.Probe{PeriodSeconds: 10, FailureThreshold: 6}},
	}}}
	b := blocking(a.podPreflight(tight, "main", true))
	if len(b) != 2 {
		t.Fatalf("want liveness and gpushare to block, got %+v", b)
	}
}

func TestGhostFindings(t *testing.T) {
	ghosts := map[string]ghostFile{
		"1:1": {path: "/dev/shm/kv.mmap", allocated: 600 << 30},
		"1:2": {path: "/dev/shm/small", allocated: 4096},
		"1:3": {path: "/dev/shm/tier", allocated: 2 << 30},
	}
	fs := ghostFindings("w", "main", ghosts, 20<<30, 512<<30)
	var block, warn int
	for _, f := range fs {
		switch {
		case f.Check == "ghost-file" && f.Severity == preflightBlock:
			block++
			if !strings.Contains(f.Detail, "/dev/shm/kv.mmap") {
				t.Errorf("block names the file: %s", f.Detail)
			}
		case f.Check == "ghost-file" && f.Severity == preflightWarn:
			warn++
		}
	}
	if block != 1 || warn != 1 {
		t.Errorf("block=%d warn=%d, want 1 and 1: %+v", block, warn, fs)
	}
	if f, ok := findingFor(fs, "memory"); !ok || !strings.Contains(f.Detail, "20.0 GiB") {
		t.Errorf("memory finding: %+v", f)
	}
}

// A deleted file this process holds open is found, keyed by its inode,
// with its allocated size.
func TestDeletedFiles(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "held"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.Name()); err != nil {
		t.Fatal(err)
	}
	ghosts := map[string]ghostFile{}
	deletedFiles("/proc", os.Getpid(), ghosts)
	found := false
	for _, g := range ghosts {
		if g.path == f.Name() {
			found = true
			if g.apparently != 1<<20 || g.allocated <= 0 {
				t.Errorf("sizes: %+v", g)
			}
		}
	}
	if !found {
		t.Fatalf("held deleted file not found among %v", ghosts)
	}
}

func TestCgroupPIDs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("45\n591\n\n675\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := cgroupPIDs(dir); err != nil || len(got) != 3 || got[0] != 45 || got[2] != 675 {
		t.Errorf("got %v %v", got, err)
	}
}

// The webhook's pid floor is what the agent writes.
func TestPIDReserveFloorMatchesTheAgent(t *testing.T) {
	if webhook.PIDReserveFloor != reservedPIDFloor*2 {
		t.Fatalf("webhook.PIDReserveFloor %d, agent writes %d", webhook.PIDReserveFloor, reservedPIDFloor*2)
	}
	pod := &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{
		{Name: webhook.PIDReserveInit, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	if !initRunning(pod, webhook.PIDReserveInit) || initRunning(pod, "other") {
		t.Error("initRunning")
	}
}

// A deleted file on a read-only mount (a node bundle replaced under a
// running pod) blocks: a restore cannot recreate it there.
func TestGhostOnReadOnlyMountBlocks(t *testing.T) {
	mounts := readMountinfoFrom(t, `22 1 0:1 / / rw,relatime - overlay overlay rw
30 22 259:1 /bundle/nvsnap /nvsnap ro,relatime - ext4 /dev/x rw
31 30 0:2 / /nvsnap/scratch rw - tmpfs tmpfs rw
32 22 0:3 / /dev/shm rw - tmpfs shm rw`)
	for path, want := range map[string]bool{
		"/nvsnap/libnvsnap_gpushare.so": true,
		"/nvsnap/scratch/x":             false,
		"/dev/shm/kv.mmap":              false,
		"/usr/lib/x":                    false,
	} {
		if got := mountReadOnly(mounts, path); got != want {
			t.Errorf("%s: read-only = %v, want %v", path, got, want)
		}
	}
	fs := ghostFindings("w", "main", map[string]ghostFile{"1:1": {path: "/nvsnap/libnvsnap_gpushare.so", allocated: 4 << 20, readOnly: true}}, 0, 1<<40)
	if b := blocking(fs); len(b) != 1 || !strings.Contains(b[0].Detail, "read-only mount") {
		t.Fatalf("want one blocking finding about the read-only mount: %+v", fs)
	}
}

func readMountinfoFrom(t *testing.T, content string) []mountEntry {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return readMountinfo(p)
}

// Every member's findings survive: the spec checks of later members were
// appended while earlier members' host checks appended from their
// goroutines, and findings were lost (review: 255 of 256). Run with -race.
func TestCapturePreflightKeepsEveryFinding(t *testing.T) {
	a := &Agent{config: Config{Webhook: WebhookConfig{CaptureLivenessBudget: 300}}}
	const n = 128
	var ranks []*corev1.Pod
	for i := range n {
		ranks = append(ranks, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("rank-%d", i)},
			Spec: corev1.PodSpec{NodeName: "node", Containers: []corev1.Container{{Name: "main"}}}})
	}
	var spec int
	for _, r := range ranks {
		spec += len(a.podPreflight(r, "main", true))
	}
	// Without a cluster client every host check fails at once: one
	// warning per member, appended as fast as the loop runs.
	fs := a.capturePreflight(context.Background(), ranks, nil, nil, logrus.NewEntry(logrus.New()))
	if want := spec + n; len(fs) != want {
		t.Errorf("%d findings, want %d (%d spec + %d host)", len(fs), want, spec, n)
	}
}

// fakeRDMAProc writes the /proc entries of process pid that the RDMA check
// reads: its fd links, its maps and its comm.
func fakeRDMAProc(t *testing.T, base string, pid int, comm string, fds map[string]string, maps string) {
	t.Helper()
	dir := filepath.Join(base, fmt.Sprint(pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	for fd, target := range fds {
		if err := os.Symlink(target, filepath.Join(dir, "fd", fd)); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"maps": maps, "comm": comm + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// RDMA devices are found open and mapped (a doorbell page), once each;
// the GPU's devices and ordinary files are not RDMA devices.
func TestRDMADevices(t *testing.T) {
	proc := t.TempDir()
	fakeRDMAProc(t, proc, 41, "VllmWorker", map[string]string{
		"3": "/dev/infiniband/uverbs0",
		"4": "/dev/nvidia0",
		"5": "/dev/infiniband/rdma_cm",
		"6": "socket:[123]",
	}, `7f0000000000-7f0000001000 rw-s 00000000 00:06 512 /dev/infiniband/uverbs0
7f0000001000-7f0000002000 rw-s 00001000 00:06 513 /dev/infiniband/uverbs1
7f0000002000-7f0000003000 r-xp 00000000 103:01 77 /usr/lib/libibverbs.so.1
7f0000003000-7f0000004000 rw-p 00000000 00:00 0
`)
	got := strings.Join(rdmaDevices(proc, 41), " ")
	if want := "/dev/infiniband/rdma_cm /dev/infiniband/uverbs0 /dev/infiniband/uverbs1"; got != want {
		t.Errorf("devices %q, want %q", got, want)
	}
	// libibverbs loaded but no device open: the process holds no RDMA
	// state.
	fakeRDMAProc(t, proc, 42, "EngineCore", map[string]string{"3": "/dev/nvidia0", "4": "/dev/shm/pool"},
		"7f0000002000-7f0000003000 r-xp 00000000 103:01 77 /usr/lib/libibverbs.so.1\n")
	if devs := rdmaDevices(proc, 42); len(devs) != 0 {
		t.Errorf("a process without RDMA devices: %v", devs)
	}
	// A process gone by the time it is read holds nothing.
	if devs := rdmaDevices(proc, 43); len(devs) != 0 {
		t.Errorf("a missing process: %v", devs)
	}
}

// Each process holding RDMA devices blocks the capture, named with its
// devices; none, nothing.
func TestRDMAFindingsBlock(t *testing.T) {
	proc := t.TempDir()
	fakeRDMAProc(t, proc, 41, "VllmWorker", nil, "")
	fakeRDMAProc(t, proc, 9, "nixl", nil, "")
	fs := rdmaFindings(proc, "w-0", "main", map[int][]string{
		41: {"/dev/infiniband/uverbs0", "/dev/infiniband/uverbs1"},
		9:  {"/dev/infiniband/uverbs2"},
	})
	if b := blocking(fs); len(b) != 2 {
		t.Fatalf("want 2 blocking findings: %+v", fs)
	}
	for i, want := range []string{"process 9 (nixl) holds RDMA devices /dev/infiniband/uverbs2:",
		"process 41 (VllmWorker) holds RDMA devices /dev/infiniband/uverbs0, /dev/infiniband/uverbs1:"} {
		if f := fs[i]; f.Check != "rdma" || f.Pod != "w-0" || f.Container != "main" || !strings.HasPrefix(f.Detail, want) {
			t.Errorf("finding %d: %+v, want detail starting %q", i, f, want)
		}
	}
	if fs := rdmaFindings(proc, "w-0", "main", map[int][]string{}); len(fs) != 0 {
		t.Errorf("findings without RDMA devices: %+v", fs)
	}
}

// The host checks of a container block its capture when one of its
// processes holds an RDMA device, and only then; the other checks still
// report.
func TestProcessFindingsRDMA(t *testing.T) {
	stubSocketProtocols(t, func(int, int) (int, error) { return unix.IPPROTO_TCP, nil })
	proc := t.TempDir()
	fakeRDMAProc(t, proc, 41, "VllmWorker", map[string]string{"3": "/dev/nvidia0"}, "")
	fakeRDMAProc(t, proc, 42, "frontend", map[string]string{"3": "socket:[9]"}, "")
	fs := processFindings(proc, []int{41, 42}, "w-0", "main", 1<<40)
	if b := blocking(fs); len(b) != 0 {
		t.Fatalf("a container without RDMA devices blocked: %+v", b)
	}
	if _, ok := findingFor(fs, "memory"); !ok {
		t.Errorf("no memory finding: %+v", fs)
	}

	fakeRDMAProc(t, proc, 43, "nixl", map[string]string{"3": "/dev/infiniband/uverbs0"}, "")
	fs = processFindings(proc, []int{41, 42, 43}, "w-0", "main", 1<<40)
	b := blocking(fs)
	if len(b) != 1 || b[0].Check != "rdma" || !strings.Contains(b[0].Detail, "process 43 (nixl)") {
		t.Fatalf("want one rdma block for process 43: %+v", fs)
	}
	if _, ok := findingFor(fs, "memory"); !ok {
		t.Errorf("the RDMA block hid the other findings: %+v", fs)
	}
}

// stubSocketProtocols makes the preflight read socket protocols from f for
// the rest of the test: the fake /proc trees' pids are not real processes.
func stubSocketProtocols(t *testing.T, f func(pid, fd int) (int, error)) {
	t.Helper()
	prev := socketProtocolOf
	socketProtocolOf = f
	t.Cleanup(func() { socketProtocolOf = prev })
}

// A container whose process holds an MPTCP socket blocks its capture,
// named with the process and the fd; TCP, UDP and Unix sockets do not.
// When no socket's protocol can be read, the check says it did not run;
// when some can, the unreadable ones are not reported.
func TestProcessFindingsMPTCP(t *testing.T) {
	proc := t.TempDir()
	fakeRDMAProc(t, proc, 41, "VllmWorker", map[string]string{"3": "socket:[10]", "4": "/dev/nvidia0"}, "")
	fakeRDMAProc(t, proc, 42, "nats-server", map[string]string{"5": "socket:[11]", "6": "socket:[12]", "7": "socket:[13]"}, "")
	protos := map[[2]int]int{{41, 3}: unix.IPPROTO_TCP, {42, 5}: unix.IPPROTO_UDP, {42, 6}: ipprotoMPTCP, {42, 7}: ipprotoMPTCP}
	stubSocketProtocols(t, func(pid, fd int) (int, error) {
		if p, ok := protos[[2]int{pid, fd}]; ok {
			return p, nil
		}
		return 0, fmt.Errorf("no such socket %d/%d", pid, fd)
	})
	fs := processFindings(proc, []int{41, 42}, "nats-0", "nats", 1<<40)
	b := blocking(fs)
	if len(b) != 1 || b[0].Check != "socket" || !strings.Contains(b[0].Detail, "process 42 (nats-server) holds MPTCP sockets (fd 6, fd 7)") {
		t.Fatalf("want one socket block for process 42's fds 6 and 7: %+v", fs)
	}
	if _, ok := findingFor(fs, "memory"); !ok {
		t.Errorf("the socket block hid the other findings: %+v", fs)
	}

	protos[[2]int{42, 6}], protos[[2]int{42, 7}] = unix.IPPROTO_TCP, unix.IPPROTO_TCP
	if b := blocking(processFindings(proc, []int{41, 42}, "nats-0", "nats", 1<<40)); len(b) != 0 {
		t.Errorf("TCP and UDP sockets blocked: %+v", b)
	}

	delete(protos, [2]int{42, 5})
	for _, f := range processFindings(proc, []int{41, 42}, "nats-0", "nats", 1<<40) {
		if f.Check == "socket" {
			t.Errorf("an unreadable socket beside readable ones was reported: %+v", f)
		}
	}

	stubSocketProtocols(t, func(pid, fd int) (int, error) {
		return 0, fmt.Errorf("pidfd_getfd %d/%d: operation not permitted", pid, fd)
	})
	fs = processFindings(proc, []int{41, 42}, "nats-0", "nats", 1<<40)
	if len(blocking(fs)) != 0 {
		t.Fatalf("an unreadable protocol blocked: %+v", fs)
	}
	if f, ok := findingFor(fs, "socket"); !ok || f.Severity != preflightWarn || !strings.Contains(f.Detail, "operation not permitted") {
		t.Errorf("want a warning that the socket check did not run: %+v", fs)
	}
}

// The protocol of a real socket of this process, read through pidfd_getfd:
// TCP reads as TCP, and an MPTCP socket, where the kernel has MPTCP, as
// MPTCP. The process's own fd stays open.
func TestPidfdSocketProtocol(t *testing.T) {
	tcp, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(tcp) }()
	got, err := pidfdSocketProtocol(os.Getpid(), tcp)
	if err != nil {
		t.Skipf("pidfd_getfd unavailable here: %v", err)
	}
	if got != unix.IPPROTO_TCP {
		t.Errorf("TCP socket reads as protocol %d", got)
	}
	if _, err := unix.GetsockoptInt(tcp, unix.SOL_SOCKET, unix.SO_TYPE); err != nil {
		t.Errorf("the socket was closed by the check: %v", err)
	}
	mp, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, ipprotoMPTCP)
	if err != nil {
		t.Skipf("no MPTCP in this kernel: %v", err)
	}
	defer func() { _ = unix.Close(mp) }()
	if got, err := pidfdSocketProtocol(os.Getpid(), mp); err != nil || got != ipprotoMPTCP {
		t.Errorf("MPTCP socket reads as %d, %v", got, err)
	}
}

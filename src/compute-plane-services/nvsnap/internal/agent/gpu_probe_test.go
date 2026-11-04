// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/runtime"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// exitStatus returns the error of a process that exited with code 1 or 2.
func exitStatus(t *testing.T, code string) error {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 1")
	if code == "2" {
		cmd = exec.CommandContext(t.Context(), "sh", "-c", "exit 2")
	}
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("exit %s: %v", code, err)
	}
	return err
}

// Only a hardware error, from a probe that ran, makes a GPU faulty; a busy
// GPU, a caller's error or a probe that could not run cannot tell.
func TestProbeVerdict(t *testing.T) {
	two, one := exitStatus(t, "2"), exitStatus(t, "1")
	for name, c := range map[string]struct {
		out    string
		err    error
		listed bool
		want   gpuProbeVerdict
	}{
		"healthy":              {"ok\n", nil, true, gpuProbeVerdict{result: metrics.ProbeHealthy}},
		"unknown driver error": {"init CUDA_ERROR_UNKNOWN\n", two, true, gpuProbeVerdict{metrics.ProbeFaulty, "init", "CUDA_ERROR_UNKNOWN"}},
		"ecc":                  {"context CUDA_ERROR_ECC_UNCORRECTABLE\n", two, true, gpuProbeVerdict{metrics.ProbeFaulty, "context", "CUDA_ERROR_ECC_UNCORRECTABLE"}},
		"mismatch":             {"read MISMATCH\n", two, true, gpuProbeVerdict{metrics.ProbeFaulty, "read", "MISMATCH"}},
		"listed, no device":    {"count CUDA_ERROR_NO_DEVICE\n", two, true, gpuProbeVerdict{metrics.ProbeFaulty, "count", "CUDA_ERROR_NO_DEVICE"}},
		"unlisted, no device":  {"count CUDA_ERROR_NO_DEVICE\n", two, false, gpuProbeVerdict{result: metrics.ProbeUnknown}},
		"busy":                 {"context CUDA_ERROR_DEVICES_UNAVAILABLE\n", two, true, gpuProbeVerdict{result: metrics.ProbeUnknown}},
		"held":                 {"alloc CUDA_ERROR_OUT_OF_MEMORY\n", two, true, gpuProbeVerdict{result: metrics.ProbeUnknown}},
		"could not ask":        {"", one, true, gpuProbeVerdict{result: metrics.ProbeUnknown}},
		"not run":              {"", errors.New("no driver library"), true, gpuProbeVerdict{result: metrics.ProbeUnknown}},
		"exit 0, no ok":        {"", nil, true, gpuProbeVerdict{result: metrics.ProbeUnknown}},
		"garbled":              {"init CUDA_ERROR_UNKNOWN extra\n", two, true, gpuProbeVerdict{result: metrics.ProbeUnknown}},
	} {
		if got := probeVerdict(c.out, c.err, c.listed); got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}

// The probe as it ships: the tool's probe action, run by the agent, on a
// stand-in driver library failing each way.
func TestGPUProbeTool(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	src := "../../docker/agent/gpushare/nvsnap-gpu-suspend.c"
	if _, serr := os.Stat(src); serr != nil {
		t.Skip("tool source not in the test's tree")
	}
	inc := os.Getenv("CUDA_INC")
	if inc == "" {
		inc = "/usr/local/cuda/include"
	}
	if _, serr := os.Stat(filepath.Join(inc, "cuda.h")); serr != nil { //nolint:gosec // the developer's own CUDA_INC
		t.Skip("no cuda.h (set CUDA_INC)")
	}
	dir := t.TempDir()
	tool, lib := filepath.Join(dir, "nvsnap-gpu-suspend"), filepath.Join(dir, "libcuda.so.1")
	for _, args := range [][]string{
		{"-O2", "-Wall", "-Wextra", "-Werror", "-I", filepath.Dir(src), "-I", inc, "-o", tool, src, "-ldl", "-lpthread"},
		{"-O2", "-Wall", "-Wextra", "-Werror", "-shared", "-fPIC", "-o", lib, "testdata/fake_libcuda.c"},
	} {
		if out, cerr := exec.CommandContext(t.Context(), cc, args...).CombinedOutput(); cerr != nil { //nolint:gosec // the C compiler on this tree's sources
			t.Fatalf("compile: %v\n%s", cerr, out)
		}
	}
	defer func(d time.Duration) { gpuProbeTimeout = d }(gpuProbeTimeout)
	gpuProbeTimeout = 2 * time.Second
	ctx := context.Background()
	for gpu, want := range map[string]gpuProbeVerdict{
		"GPU-ok":       {result: metrics.ProbeHealthy},
		"GPU-init":     {metrics.ProbeFaulty, "init", "CUDA_ERROR_UNKNOWN"},
		"GPU-none":     {metrics.ProbeFaulty, "count", "CUDA_ERROR_NO_DEVICE"},
		"GPU-two":      {result: metrics.ProbeUnknown},
		"GPU-ecc":      {metrics.ProbeFaulty, "context", "CUDA_ERROR_ECC_UNCORRECTABLE"},
		"GPU-busy":     {result: metrics.ProbeUnknown},
		"GPU-oom":      {result: metrics.ProbeUnknown},
		"GPU-mismatch": {metrics.ProbeFaulty, "read", "MISMATCH"},
		"GPU-hang":     {result: metrics.ProbeUnknown},
	} {
		start := time.Now()
		out, perr := runGPUProbe(ctx, tool, lib, gpu)
		if got := probeVerdict(out, perr, true); got != want {
			t.Errorf("%s: %+v (output %q, %v), want %+v", gpu, got, out, perr, want)
		}
		if d := time.Since(start); d > gpuProbeTimeout+5*time.Second {
			t.Errorf("%s: took %s", gpu, d)
		}
	}
	out, err := runGPUProbe(ctx, tool, filepath.Join(dir, "missing.so"), "GPU-ok")
	if got := probeVerdict(out, err, true); got.result != metrics.ProbeUnknown || !strings.Contains(err.Error(), "cannot load") {
		t.Errorf("without a driver library: %+v, %v", got, err)
	}
}

// writeDriverGPU lists GPU uuid with device minor in driver directory dir.
func writeDriverGPU(t *testing.T, dir, bus, uuid, minor string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, bus), 0o755); err != nil {
		t.Fatal(err)
	}
	info := "Model: \t\t NVIDIA GB300\nGPU UUID: \t " + uuid + "\nBus Location: \t " + bus + "\nDevice Minor: \t " + minor + "\n"
	if err := os.WriteFile(filepath.Join(dir, bus, "information"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A container's GPUs, from the runtime hook's UUIDs or from its device
// nodes (CDI) by the driver's minors.
func TestSpecGPUs(t *testing.T) {
	dir := t.TempDir()
	writeDriverGPU(t, dir, "0008:06:00.0", "GPU-aaaa", "0")
	writeDriverGPU(t, dir, "0009:06:00.0", "GPU-bbbb", "1")
	if err := os.MkdirAll(filepath.Join(dir, "0010:06:00.0"), 0o755); err != nil {
		t.Fatal(err) // a GPU without its information: not listed
	}
	driver := driverGPUs(dir)
	if len(driver) != 2 || driver[0] != "GPU-aaaa" || driver[1] != "GPU-bbbb" {
		t.Fatalf("driver GPUs %v", driver)
	}
	devices := func(paths ...string) *specs.Linux {
		l := &specs.Linux{}
		for _, p := range paths {
			l.Devices = append(l.Devices, specs.LinuxDevice{Path: p})
		}
		return l
	}
	for name, c := range map[string]struct {
		spec *specs.Spec
		want string
	}{
		"hook UUIDs":       {&specs.Spec{Process: &specs.Process{Env: []string{"A=1", "NVIDIA_VISIBLE_DEVICES=GPU-x,GPU-y"}}}, "GPU-x,GPU-y"},
		"CDI devices":      {&specs.Spec{Process: &specs.Process{Env: []string{"NVIDIA_VISIBLE_DEVICES=all"}}, Linux: devices("/dev/nvidiactl", "/dev/nvidia1", "/dev/nvidia-uvm", "/dev/nvidia0")}, "GPU-bbbb,GPU-aaaa"},
		"unlisted minor":   {&specs.Spec{Linux: devices("/dev/nvidia0", "/dev/nvidia7")}, ""},
		"no GPU":           {&specs.Spec{Process: &specs.Process{Env: []string{"PATH=/bin"}}, Linux: devices("/dev/null")}, ""},
		"indices, no node": {&specs.Spec{Process: &specs.Process{Env: []string{"NVIDIA_VISIBLE_DEVICES=0,1"}}}, ""},
		"none":             {nil, ""},
	} {
		if got := strings.Join(specGPUs(c.spec, driver), ","); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestContainerIDOf(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
		{Name: "side", ContainerID: "containerd://side1"},
		{Name: "engine", LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ContainerID: "cri-o://last"}}},
	}}}
	if got := containerIDOf(pod, "engine"); got != "last" {
		t.Errorf("crash-looping container: %q, want the one that ended", got)
	}
	pod.Status.ContainerStatuses[1].ContainerID = "cri-o://now"
	if got := containerIDOf(pod, "engine"); got != "now" {
		t.Errorf("running container: %q", got)
	}
	if got := containerIDOf(pod, "missing"); got != "" {
		t.Errorf("no such container: %q", got)
	}
}

// specRuntime answers ContainerSpec from specs; nothing else is used.
type specRuntime struct {
	runtime.Runtime
	specs map[string]*specs.Spec
}

func (r specRuntime) ContainerSpec(_ context.Context, id string) (*specs.Spec, error) {
	if s, ok := r.specs[id]; ok {
		return s, nil
	}
	return nil, runtime.ErrNotFound
}

// stubProbe answers probes from verdicts by UUID: "ok", or a failed
// step and error.
func stubProbe(t *testing.T, verdicts map[string]string) {
	t.Helper()
	prev := gpuProbeRun
	t.Cleanup(func() { gpuProbeRun = prev })
	two := exitStatus(t, "2")
	gpuProbeRun = func(_ context.Context, uuid string) (string, error) {
		v, ok := verdicts[uuid]
		switch {
		case !ok:
			return "", errors.New("not probed in this test")
		case v == "ok":
			return "ok\n", nil
		default:
			return v + "\n", two
		}
	}
}

// onGPUs makes pod one on node n0 whose container engine ran (or runs) as
// container id, given GPUs uuids.
func onGPUs(c *criuAutoRestorer, pod *corev1.Pod, id string, uuids ...string) *corev1.Pod {
	if len(pod.Status.ContainerStatuses) == 0 {
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "engine"}}
	}
	pod.Status.ContainerStatuses[0].ContainerID = "containerd://" + id
	c.a.runtime = specRuntime{specs: map[string]*specs.Spec{id: {Process: &specs.Process{
		Env: []string{"NVIDIA_VISIBLE_DEVICES=" + strings.Join(uuids, ",")}}}}}
	return pod
}

// A failure on a faulty GPU is marked as such and named on the node; one
// on healthy GPUs, or GPUs that cannot be named, stays as it is.
func TestOnPodGPUFault(t *testing.T) {
	c, rec := lifecycleAgent(t, "GB300-probe")
	ctx := context.Background()
	cause := errors.New("restore failed")
	pod := onGPUs(c, enginePod(0), "ctr1", "GPU-good", "GPU-bad")
	stubProbe(t, map[string]string{"GPU-good": "ok", "GPU-bad": "context CUDA_ERROR_UNKNOWN"})
	faulty := func() float64 {
		return metricValue(t, metrics.GPUProbes, map[string]string{"gpu": "GB300-probe", "result": metrics.ProbeFaulty, "error": "CUDA_ERROR_UNKNOWN"})
	}
	before := faulty()
	err := c.a.onPodGPUFault(ctx, pod, "engine", cause, quietEntry())
	f := onFaultyGPU(err)
	if f == nil || len(f.Faults) != 1 || f.Faults[0] != (gpuFault{Node: "n0", UUID: "GPU-bad", Step: "context", Error: "CUDA_ERROR_UNKNOWN"}) || !errors.Is(err, cause) {
		t.Fatalf("err %v, faults %+v", err, f)
	}
	if got := faulty() - before; got != 1 {
		t.Errorf("faulty probes counted %v", got)
	}
	if e := event(rec); !strings.Contains(e, "Warning "+eventGPUFault) || !strings.Contains(e, "GPU-bad") {
		t.Errorf("event %q", e)
	}

	stubProbe(t, map[string]string{"GPU-good": "ok", "GPU-bad": "ok"})
	if err := c.a.onPodGPUFault(ctx, pod, "engine", cause, quietEntry()); err != cause { //nolint:errorlint // the very error, unwrapped
		t.Errorf("healthy GPUs: %v", err)
	}
	unknown := func() float64 {
		return metricValue(t, metrics.GPUProbes, map[string]string{"gpu": "GB300-probe", "result": metrics.ProbeUnknown})
	}
	before = unknown()
	stubProbe(t, map[string]string{"GPU-good": "ok", "GPU-bad": "alloc CUDA_ERROR_OUT_OF_MEMORY"})
	if err := c.a.onPodGPUFault(ctx, pod, "engine", cause, quietEntry()); err != cause { //nolint:errorlint // the very error, unwrapped
		t.Errorf("a GPU held by another process: %v", err)
	}
	c.a.runtime = specRuntime{}
	if err := c.a.onPodGPUFault(ctx, pod, "engine", cause, quietEntry()); err != cause { //nolint:errorlint // the very error, unwrapped
		t.Errorf("GPUs not named: %v", err)
	}
	if got := unknown() - before; got != 2 {
		t.Errorf("probes that could not tell counted %v, want 2", got)
	}
	if e := event(rec); e != "" {
		t.Errorf("an event without a faulty GPU: %q", e)
	}
}

// The faults of a failed restore reach the agent that asked for it,
// from its header, not from the error's text.
func TestRestoreFailureCarriesGPUFaults(t *testing.T) {
	faults := []gpuFault{{Node: "n3", UUID: "GPU-bad", Step: "init", Error: "CUDA_ERROR_UNKNOWN"}}
	for name, c := range map[string]struct {
		err  error
		want []gpuFault
	}{
		"on a faulty GPU": {&gpuFaultError{Faults: faults, cause: errors.New("criu restore: exit status 1")}, faults},
		"plain failure":   {errors.New("criu restore: exit status 1; on faulty GPU GPU-x"), nil},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeRestoreFailure(w, c.err) }))
		_, err := postRestore(context.Background(), srv.Client(), srv.URL, RestoreRequest{CheckpointID: "c0"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "exit status 1") {
			t.Fatalf("%s: %v", name, err)
		}
		f := onFaultyGPU(err)
		switch {
		case c.want == nil && f != nil:
			t.Errorf("%s: faults %+v read from the text", name, f.Faults)
		case len(c.want) > 0 && (f == nil || len(f.Faults) != 1 || f.Faults[0] != c.want[0]):
			t.Errorf("%s: faults %+v, want %+v", name, f, c.want)
		}
	}
	if f := onFaultyGPU(gpuFaultFromHeader("not json", errors.New("x"))); f != nil {
		t.Error("a malformed header made a fault")
	}
}

// One member on a faulty GPU makes the whole group failure the GPU's; it
// is still a restore that ran.
func TestGroupRestoreFailureGPUFault(t *testing.T) {
	inner := errors.New("group restore: 1 of 2 members failed")
	plain := error(&groupRestoreFailure{err: inner})
	if onFaultyGPU(plain) != nil || !errors.As(plain, new(*groupRestoreFailure)) || !errors.Is(plain, inner) {
		t.Errorf("plain failure: %v", plain)
	}
	f := &gpuFaultError{Faults: []gpuFault{{UUID: "GPU-bad"}}, cause: inner}
	faulty := error(&groupRestoreFailure{err: inner, fault: f})
	if onFaultyGPU(faulty) != f || !errors.As(faulty, new(*groupRestoreFailure)) || faulty.Error() != inner.Error() {
		t.Errorf("faulty failure: %v", faulty)
	}
}

func TestRecordGPUFault(t *testing.T) {
	f := &gpuFaultError{Faults: []gpuFault{{Node: "n1", UUID: "GPU-bad", Step: "init", Error: "CUDA_ERROR_UNKNOWN"}}}
	d := map[string]string{"state": "complete", "size": "1", "checkpoint.0": "c0", groupRestoreFailuresKey: "2"}
	for range 2 {
		if !recordGPUFault(d, "c0", f) {
			t.Fatal("not recorded")
		}
	}
	if d[groupGPUFaultsKey] != "2" || !strings.Contains(d[groupLastGPUFaultKey], "GPU-bad on n1") ||
		d[groupRestoreFailuresKey] != "2" || d["state"] != "complete" {
		t.Errorf("record %v", d)
	}
	if recordGPUFault(d, "c9", f) {
		t.Error("recorded on another capture's record")
	}
	if recordGPUFault(map[string]string{"state": "restore-failed", "checkpoint.0": "c0"}, "c0", f) {
		t.Error("recorded on an unusable record")
	}
}

// A verified capture at its last allowed failure whose restore fails on a
// faulty GPU: kept, not blocked, its failures not counted; the pods are
// replaced so they restore from it again.
func TestFailGroup_GPUFaultKeepsTheCapture(t *testing.T) {
	c, kc, leader := groupFixture(t)
	ctx := context.Background()
	setRecord(t, kc, map[string]string{groupRestoreFailuresKey: "2"})
	inner := errors.New("group restore: kimi-1: restore=CUDA_ERROR_UNKNOWN")
	cause := &groupRestoreFailure{err: inner, fault: &gpuFaultError{
		Faults: []gpuFault{{Node: "n2", UUID: "GPU-bad", Step: "init", Error: "CUDA_ERROR_UNKNOWN"}}, cause: inner}}
	outcome := func() float64 {
		return metricValue(t, metrics.RestoreOutcomes, map[string]string{"outcome": metrics.RestoreGPUFault})
	}
	before := outcome()
	c.failGroup(ctx, leader, "k1", true, cause, quietEntry())
	if c.Blocked(ctx, "c0") || c.Blocked(ctx, "c1") {
		t.Error("a restore on a faulty GPU blocked the checkpoints")
	}
	cm, _ := kc.CoreV1().ConfigMaps("nvsnap-system").Get(ctx, criuGroupConfigMapName("k1"), metav1.GetOptions{})
	if cm.Data["state"] != criuGroupStateComplete || cm.Data[groupRestoreFailuresKey] != "2" ||
		cm.Data[groupGPUFaultsKey] != "1" || !strings.Contains(cm.Data[groupLastGPUFaultKey], "GPU-bad") {
		t.Errorf("record = %v, want it complete, its failures as they were, one GPU fault", cm.Data)
	}
	if got := remainingPods(t, kc); len(got) != 1 || got[0] != "other-0" {
		t.Errorf("pods left = %v, want only the other instance", got)
	}
	if got := outcome() - before; got != 1 {
		t.Errorf("gpu-fault outcomes counted %v", got)
	}
	// The same failure without a faulty GPU is the capture's last.
	c, kc, leader = groupFixture(t)
	setRecord(t, kc, map[string]string{groupRestoreFailuresKey: "2"})
	c.failGroup(ctx, leader, "k1", true, &groupRestoreFailure{err: inner}, quietEntry())
	if !c.Blocked(ctx, "c0") {
		t.Error("a failed restore on healthy GPUs did not count")
	}
}

// A restored engine that died on a faulty GPU leaves its checkpoint
// unblocked; on a healthy one, it blocks it.
func TestFailRestored_GPUFault(t *testing.T) {
	ctx := context.Background()
	for _, verdict := range []string{"init CUDA_ERROR_UNKNOWN", "ok"} {
		c, _ := lifecycleAgent(t, "GB300")
		pod := onGPUs(c, enginePod(0), "ctr1", "GPU-a")
		pod.Annotations[webhook.CRIURestoreAnnotation] = "c0"
		pod.Annotations[webhook.CRIURestoreContainerAnnotation] = "engine"
		if _, err := c.a.kubeClient.CoreV1().Pods("fn").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		stubProbe(t, map[string]string{"GPU-a": verdict})
		c.failRestored(ctx, pod, restoredRef{checkpoint: "c0"}, errors.New("the engine died"))
		faulty := verdict != "ok"
		if c.Blocked(ctx, "c0") == faulty {
			t.Errorf("probe %q: blocked %v", verdict, !faulty)
		}
		if _, err := c.a.kubeClient.CoreV1().Pods("fn").Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
			t.Errorf("probe %q: the pod was not replaced", verdict)
		}
	}
}

// A pod that crashes under the library on a faulty GPU does not mark its
// configuration, nor is it replaced; on healthy GPUs it does.
func TestConsiderGPUShareFailure_FaultyGPU(t *testing.T) {
	ctx := context.Background()
	for _, verdict := range []string{"context CUDA_ERROR_UNKNOWN", "ok"} {
		a, cs := fallbackAgent(t)
		c := &criuAutoRestorer{a: a}
		pod := onGPUs(c, withLibrary(crashing(enginePod(0), 2, 1)), "ctr9", "GPU-a")
		if _, err := cs.CoreV1().Pods("fn").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		stubProbe(t, map[string]string{"GPU-a": verdict})
		c.considerFallback(ctx, pod)
		faulty := verdict != "ok"
		cm, err := cs.CoreV1().ConfigMaps(a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(fnKey(t, a)), metav1.GetOptions{})
		if marked := err == nil && cm.Data["state"] == criuGroupStateGPUShareFailed; marked == faulty {
			t.Errorf("probe %q: marked %v", verdict, marked)
		}
		_, err = cs.CoreV1().Pods("fn").Get(ctx, pod.Name, metav1.GetOptions{})
		if kept := err == nil; kept != faulty {
			t.Errorf("probe %q: pod kept %v", verdict, kept)
		}
	}
}

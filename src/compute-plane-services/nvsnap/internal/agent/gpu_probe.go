// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A faulty GPU is not nvsnap's failure. A pod placed on a GPU that needs a
// reset fails whatever it runs, with nvsnap or without. Counted against
// nvsnap, such a failure blames the gpushare library for a whole
// configuration (criu_fallback.go) or gives up a verified checkpoint
// (criu_group.go). So before a failure counts, the agent of the pod's node
// probes the GPUs the pod was given, without nvsnap: `nvsnap-gpu-suspend
// probe` opens a context on one GPU, fills a buffer and reads it back. A
// GPU that fails with a hardware error is faulty: the failure does not
// count, the GPU is named in a Warning event on the node, and nothing is
// cordoned (the cluster's health checks own that). A probe that cannot
// tell (no driver library, the GPU busy, a timeout) leaves the failure
// counted, as before the probe. See docs/design/GPU-FAULT-PROBE.md.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/hostlibs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const (
	// gpuProbeTool is the agent image's copy of the node bundle's GPU tool
	// (docker/agent/Dockerfile.base): the probe runs in the agent, not in
	// the workload.
	gpuProbeTool = "/criu-bundle/" + gpushareToolName
	// eventGPUFault is the reason of the Warning event on the node.
	eventGPUFault = "NvsnapGPUFault"
	// gpuFaultHeader carries a failed restore's GPU faults (JSON) to the
	// agent that asked for it: the error text alone is not to be parsed.
	gpuFaultHeader = "X-Nvsnap-Gpu-Fault"
)

// gpuProbeTimeout bounds one GPU's probe; a probe that does not end
// cannot tell. Tests shorten it.
var gpuProbeTimeout = 30 * time.Second

// gpuFaultErrors are the driver errors that make a GPU faulty: errors of
// the hardware, never of the caller or of a GPU that is busy. MISMATCH is
// the probe's own: the buffer read back differs from what was written.
var gpuFaultErrors = map[string]bool{
	"CUDA_ERROR_UNKNOWN":              true,
	"CUDA_ERROR_ECC_UNCORRECTABLE":    true,
	"CUDA_ERROR_HARDWARE_STACK_ERROR": true,
	"CUDA_ERROR_ILLEGAL_INSTRUCTION":  true,
	"CUDA_ERROR_ILLEGAL_ADDRESS":      true,
	"CUDA_ERROR_LAUNCH_FAILED":        true,
	"CUDA_ERROR_NVLINK_UNCORRECTABLE": true,
	"CUDA_ERROR_SYSTEM_NOT_READY":     true,
	"MISMATCH":                        true,
}

// gpuNoDevice is the driver offering no device for the GPU: a fault only
// when the driver lists the GPU (it is there, and CUDA cannot use it).
const gpuNoDevice = "CUDA_ERROR_NO_DEVICE"

// gpuFault is a GPU the probe found faulty.
type gpuFault struct {
	Node  string `json:"node"`
	UUID  string `json:"uuid"`
	Step  string `json:"step"`
	Error string `json:"error"`
}

func (f gpuFault) String() string {
	return fmt.Sprintf("%s on %s (%s: %s)", f.UUID, f.Node, f.Step, f.Error)
}

// gpuFaultError is a failure that happened on faulty GPUs: it says nothing
// about a checkpoint or the gpushare library.
type gpuFaultError struct {
	Faults []gpuFault
	cause  error
}

func (e *gpuFaultError) Error() string {
	s := make([]string, len(e.Faults))
	for i, f := range e.Faults {
		s[i] = f.String()
	}
	return fmt.Sprintf("%v; on faulty GPU %s", e.cause, strings.Join(s, ", "))
}

func (e *gpuFaultError) Unwrap() error { return e.cause }

// onFaultyGPU returns the GPU faults err happened on, or nil.
func onFaultyGPU(err error) *gpuFaultError {
	var f *gpuFaultError
	if errors.As(err, &f) {
		return f
	}
	return nil
}

// writeRestoreFailure answers a failed restore, with its GPU faults in
// gpuFaultHeader.
func writeRestoreFailure(w http.ResponseWriter, err error) {
	if f := onFaultyGPU(err); f != nil {
		if b, merr := json.Marshal(f.Faults); merr == nil {
			w.Header().Set(gpuFaultHeader, string(b))
		}
	}
	http.Error(w, fmt.Sprintf("restore failed: %v", err), http.StatusInternalServerError)
}

// gpuFaultFromHeader marks cause with the GPU faults another agent sent
// with a failed restore (header value v); cause as it is without them.
func gpuFaultFromHeader(v string, cause error) error {
	if v == "" {
		return cause
	}
	var faults []gpuFault
	if err := json.Unmarshal([]byte(v), &faults); err != nil || len(faults) == 0 {
		return cause
	}
	return &gpuFaultError{Faults: faults, cause: cause}
}

// gpuProbeVerdict is one GPU's probe: its result (metrics.Probe*), and for
// a faulty GPU the step and error.
type gpuProbeVerdict struct {
	result, step, err string
}

// probeVerdict reads the tool's answer for uuid: its first line ("ok", or
// "<step> <error>" with exit status 2) and its error. listed reports
// whether the driver lists the GPU.
func probeVerdict(out string, runErr error, listed bool) gpuProbeVerdict {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if runErr == nil {
		if line == "ok" {
			return gpuProbeVerdict{result: metrics.ProbeHealthy}
		}
		return gpuProbeVerdict{result: metrics.ProbeUnknown}
	}
	var exit *exec.ExitError
	if !errors.As(runErr, &exit) || exit.ExitCode() != 2 {
		return gpuProbeVerdict{result: metrics.ProbeUnknown}
	}
	step, name, ok := strings.Cut(line, " ")
	if !ok || strings.ContainsAny(name, " \t") {
		return gpuProbeVerdict{result: metrics.ProbeUnknown}
	}
	if gpuFaultErrors[name] || (name == gpuNoDevice && listed) {
		return gpuProbeVerdict{result: metrics.ProbeFaulty, step: step, err: name}
	}
	return gpuProbeVerdict{result: metrics.ProbeUnknown}
}

// gpuProbeRun probes the one GPU uuid; tests replace it.
var gpuProbeRun = func(ctx context.Context, uuid string) (string, error) {
	lib := hostLibcuda()
	if lib == "" {
		return "", errors.New("no driver library (libcuda.so.1) on the host")
	}
	// Without these the driver answers CUDA_ERROR_UNKNOWN for any GPU:
	// that would be the agent's own setup, not the GPU.
	for _, dev := range []string{"/dev/nvidiactl", "/dev/nvidia-uvm"} {
		if _, err := os.Stat(dev); err != nil {
			return "", fmt.Errorf("the agent has no %s: %w", dev, err)
		}
	}
	return runGPUProbe(ctx, gpuProbeTool, lib, uuid)
}

// runGPUProbe runs tool's probe of GPU uuid with driver library lib, for
// at most gpuProbeTimeout, and returns its output and its error (with its
// stderr).
func runGPUProbe(ctx context.Context, tool, lib, uuid string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gpuProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, "--libcuda", lib, "probe") //nolint:gosec // the agent's own tool and the host's driver library
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "CUDA_VISIBLE_DEVICES=" + uuid}
	killGroupOnCancel(cmd)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err != nil && errOut.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, lastLines(errOut.String(), 4))
	}
	return out.String(), err
}

// hostLibcuda finds the host's driver library from the agent: under a GPU
// Operator driver root, or the host's own library directories.
func hostLibcuda() string {
	for _, dir := range []string{
		hostlibs.DriverDir("/host/run/nvidia/driver"),
		"/host/run/nvidia/driver/usr/lib64",
		"/host/usr/local/nvidia/lib64",
		"/host" + hostlibs.Dir(),
		"/host/usr/lib64",
	} {
		if p := filepath.Join(dir, "libcuda.so.1"); fileExists(p) {
			return p
		}
	}
	return ""
}

// driverGPUsDir is where the driver lists the node's GPUs, one directory
// per GPU with an "information" file; tests replace it.
var driverGPUsDir = "/proc/driver/nvidia/gpus"

// driverGPUs maps the device minor of each GPU the driver lists to its
// UUID.
func driverGPUs(dir string) map[int]string {
	out := map[int]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		f, err := os.Open(filepath.Join(dir, e.Name(), "information"))
		if err != nil {
			continue
		}
		uuid, minor := "", -1
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), ":")
			if !ok {
				continue
			}
			switch strings.TrimSpace(k) {
			case "GPU UUID":
				uuid = strings.TrimSpace(v)
			case "Device Minor":
				minor, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		_ = f.Close()
		if strings.HasPrefix(uuid, "GPU-") && minor >= 0 {
			out[minor] = uuid
		}
	}
	return out
}

// gpuDeviceMinor matches a GPU's device node, capturing its minor.
var gpuDeviceMinor = regexp.MustCompile(`^/dev/nvidia(\d+)$`)

// specGPUs returns the UUIDs of the GPUs a container was created with:
// from NVIDIA_VISIBLE_DEVICES when it lists UUIDs (the runtime hook), or
// else from the GPU device nodes in the spec (CDI), by the driver's minor
// numbers. nil when the spec does not say, or names a GPU the driver does
// not list.
func specGPUs(spec *specs.Spec, driver map[int]string) []string {
	if spec == nil {
		return nil
	}
	if spec.Process != nil {
		for _, kv := range spec.Process.Env {
			v, ok := strings.CutPrefix(kv, "NVIDIA_VISIBLE_DEVICES=")
			if !ok {
				continue
			}
			uuids := strings.Split(v, ",")
			valid := true
			for i, u := range uuids {
				uuids[i] = strings.TrimSpace(u)
				valid = valid && strings.HasPrefix(uuids[i], "GPU-")
			}
			if valid {
				return uuids
			}
		}
	}
	if spec.Linux == nil {
		return nil
	}
	var uuids []string
	for _, d := range spec.Linux.Devices {
		m := gpuDeviceMinor.FindStringSubmatch(d.Path)
		if m == nil {
			continue
		}
		minor, _ := strconv.Atoi(m[1])
		uuid, ok := driver[minor]
		if !ok {
			return nil
		}
		uuids = append(uuids, uuid)
	}
	return uuids
}

// containerIDOf returns the runtime ID of pod's container name: the one
// running or last ended, else the one before it.
func containerIDOf(pod *corev1.Pod, name string) string {
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if cs.Name != name {
			continue
		}
		id := cs.ContainerID
		if id == "" && cs.LastTerminationState.Terminated != nil {
			id = cs.LastTerminationState.Terminated.ContainerID
		}
		if _, after, ok := strings.Cut(id, "://"); ok {
			return after
		}
		return id
	}
	return ""
}

// probedContainer is the container of pod whose GPUs a failure ran on: a
// placeholder's restore target, else the pod's GPU container.
func probedContainer(pod *corev1.Pod) string {
	if c := pod.Annotations[webhook.CRIURestoreContainerAnnotation]; c != "" {
		return c
	}
	return gpuContainer(pod)
}

// podGPUUUIDs returns the GPUs pod's container was given, from the
// container runtime.
func (a *Agent) podGPUUUIDs(ctx context.Context, pod *corev1.Pod, container string) ([]string, error) {
	if a.runtime == nil {
		return nil, errors.New("no container runtime client")
	}
	id := containerIDOf(pod, container)
	if id == "" {
		return nil, fmt.Errorf("container %s has no runtime ID", container)
	}
	spec, err := a.runtime.ContainerSpec(ctx, id)
	if err != nil {
		return nil, err
	}
	uuids := specGPUs(spec, driverGPUs(driverGPUsDir))
	if len(uuids) == 0 {
		return nil, fmt.Errorf("container %s names no GPU by UUID or device node", container)
	}
	return uuids, nil
}

// probeGPUs probes each of uuids at once, and returns the faulty ones.
// Each probe is counted; a faulty GPU is named in the log and in a Warning
// event on the node.
func (a *Agent) probeGPUs(ctx context.Context, uuids []string, log *logrus.Entry) []gpuFault {
	listed := map[string]bool{}
	for _, u := range driverGPUs(driverGPUsDir) {
		listed[u] = true
	}
	verdicts := make([]gpuProbeVerdict, len(uuids))
	var wg sync.WaitGroup
	for i, u := range uuids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := gpuProbeRun(ctx, u)
			verdicts[i] = probeVerdict(out, err, listed[u])
			if verdicts[i].result == metrics.ProbeUnknown {
				log.WithError(err).WithFields(logrus.Fields{"gpu": u, "output": strings.TrimSpace(out)}).
					Info("GPU probe: cannot tell whether the GPU works; the failure counts")
			}
		}()
	}
	wg.Wait()
	product := a.nodeGPU(ctx, a.config.NodeName)
	var faults []gpuFault
	for i, v := range verdicts {
		metrics.ObserveGPUProbe(product, v.result, v.err)
		if v.result != metrics.ProbeFaulty {
			continue
		}
		f := gpuFault{Node: a.config.NodeName, UUID: uuids[i], Step: v.step, Error: v.err}
		faults = append(faults, f)
		log.WithFields(logrus.Fields{"gpu": f.UUID, "step": f.Step, "cudaError": f.Error}).
			Warn("GPU probe: the GPU is faulty; a failure on it is not counted against nvsnap")
		a.nodeEvent(true, eventGPUFault, "GPU %s failed a probe without nvsnap at %s: %s; pods placed on it fail", f.UUID, f.Step, f.Error)
	}
	return faults
}

// onPodGPUFault probes the GPUs pod's container ran on after it failed
// with cause, and returns cause marked as a GPU fault when one of them is
// faulty; else cause.
func (a *Agent) onPodGPUFault(ctx context.Context, pod *corev1.Pod, container string, cause error, log *logrus.Entry) error {
	if pod == nil || container == "" {
		return cause
	}
	log = log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "container": container})
	uuids, err := a.podGPUUUIDs(ctx, pod, container)
	if err != nil {
		log.WithError(err).Info("GPU probe: cannot name the pod's GPUs; the failure counts")
		metrics.ObserveGPUProbe(a.nodeGPU(ctx, a.config.NodeName), metrics.ProbeUnknown, "")
		return cause
	}
	if faults := a.probeGPUs(ctx, uuids, log); len(faults) > 0 {
		return &gpuFaultError{Faults: faults, cause: cause}
	}
	return cause
}

// restoreProbed is Restore, with a restore that ran and failed checked for
// faulty GPUs under the placeholder.
func (a *Agent) restoreProbed(ctx context.Context, req RestoreRequest) (*RestoreResult, error) {
	res, err := a.Restore(ctx, req)
	if err == nil || ctx.Err() != nil || errors.Is(err, errCheckpointUnavailable) || errors.Is(err, errNotPlaceholder) ||
		a.kubeClient == nil || req.PlaceholderPodName == "" {
		return res, err
	}
	pod, gerr := a.kubeClient.CoreV1().Pods(req.PlaceholderNamespace).Get(ctx, req.PlaceholderPodName, metav1.GetOptions{})
	if gerr != nil {
		return res, err
	}
	container := req.PlaceholderContainerName
	if container == "" {
		container = probedContainer(pod)
	}
	return res, a.onPodGPUFault(ctx, pod, container, err, a.log.WithField("checkpointId", req.CheckpointID))
}

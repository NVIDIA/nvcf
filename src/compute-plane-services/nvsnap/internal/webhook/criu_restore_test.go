// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// criuL2Backend answers Stat with a CRIU capture record.
type criuL2Backend struct {
	stubL2Backend
	man checkpointstore.Manifest
}

func (b *criuL2Backend) Stat(_ context.Context, hash string) (checkpointstore.Manifest, error) {
	m := b.man
	m.Hash = hash
	return m, nil
}

func criuCaptureRecord() checkpointstore.Manifest {
	return checkpointstore.Manifest{
		CaptureMethod:   "criu",
		CapturedOnNodes: []string{"node-a"},
		SourcePodMeta: map[string]string{"engine": "criu", "checkpoint_id": "abc123__20261008-101010",
			checkpointstore.CRIUScopeMetaKey: CRIUNamespaceScope("default")},
	}
}

// An NVCA function pod: a sidecar first, the engine second, with all three
// probes.
func criuFunctionPod() *corev1.Pod {
	probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(8000)}}}
	return &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "utils", Image: "utils"},
			{Name: "inference", Image: "vllm", Command: []string{"vllm"}, Args: []string{"serve", "m"},
				LivenessProbe: probe, ReadinessProbe: probe, StartupProbe: probe},
		}},
	}
}

func criuMutator(t *testing.T, man checkpointstore.Manifest) *Mutator {
	return &Mutator{Backend: newBackend(t), L2Backend: &criuL2Backend{man: man}, MainContainer: 1,
		CheckpointHostRoot: "/var/lib/containers/nvsnap-checkpoints"}
}

func TestCRIURestore_PodBecomesThePlaceholder(t *testing.T) {
	m := criuMutator(t, criuCaptureRecord())
	pod := podWithAnnotation("abc123")
	fn := criuFunctionPod()
	pod.Spec = fn.Spec
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, patches)
	c := got.Spec.Containers[1]
	// Idled by nvsnap-idle, which needs nothing from the engine's image.
	if !reflect.DeepEqual(c.Command, []string{IdleBin}) || len(c.Args) != 0 {
		t.Errorf("engine container not idled: %v %v", c.Command, c.Args)
	}
	helperMounted, helperVolume := false, false
	for _, vm := range c.VolumeMounts {
		helperMounted = helperMounted || vm == helperMount()
	}
	for _, v := range got.Spec.Volumes {
		helperVolume = helperVolume || (v.Name == helperVolumeName && v.HostPath != nil)
	}
	if !helperMounted || !helperVolume {
		t.Errorf("the placeholder must mount the helper read-only from the node bundle: mounted=%v volume=%v", helperMounted, helperVolume)
	}
	if c.LivenessProbe != nil || c.StartupProbe != nil {
		t.Error("liveness/startup probes would kill the placeholder before the restore")
	}
	if c.ReadinessProbe == nil {
		t.Error("readiness probe must stay: the pod turns Ready when the restored engine serves")
	}
	if got.Spec.Containers[0].Command != nil {
		t.Error("the sidecar must run unchanged")
	}
	// The images read-only (later restores on the node use them); what the
	// restore writes goes to the pod's own work volume.
	var mounted, work bool
	for _, vm := range c.VolumeMounts {
		mounted = mounted || (vm.MountPath == "/checkpoints/abc123__20261008-101010" && vm.ReadOnly)
		work = work || (vm.MountPath == RestoreWorkMountPath && vm.Name == restoreWorkVolume && !vm.ReadOnly)
	}
	for _, v := range got.Spec.Volumes {
		if v.Name == restoreWorkVolume && v.EmptyDir == nil {
			t.Errorf("work volume %+v, want an emptyDir", v)
		}
	}
	if !work {
		t.Error("no writable work volume for the restore")
	}
	var hostDir string
	var hostType corev1.HostPathType
	for _, v := range got.Spec.Volumes {
		if v.Name == criuCheckpointsVolume && v.HostPath != nil {
			hostDir, hostType = v.HostPath.Path, *v.HostPath.Type
		}
	}
	// Only this checkpoint, never the node's others; created if missing so
	// the pod starts on a node the checkpoint has yet to be fetched to.
	if !mounted || hostDir != "/var/lib/containers/nvsnap-checkpoints/abc123__20261008-101010" || hostType != corev1.HostPathDirectoryOrCreate {
		t.Errorf("checkpoint not mounted alone at /checkpoints/<id> (mounted=%v dir=%q type=%q)", mounted, hostDir, hostType)
	}
	if got.Annotations[CRIURestoreAnnotation] != "abc123__20261008-101010" || got.Annotations[CRIURestoreContainerAnnotation] != "inference" {
		t.Errorf("restore annotations = %v", got.Annotations)
	}
	na := got.Spec.Affinity.NodeAffinity
	if na == nil || len(na.PreferredDuringSchedulingIgnoredDuringExecution) != 1 ||
		na.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference.MatchExpressions[0].Values[0] != "node-a" ||
		na.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		t.Errorf("want a preferred (not required) affinity to the capture node: %+v", na)
	}
}

func TestCRIURestore_SkipsWhenItCannotRestore(t *testing.T) {
	cases := map[string]func(*Mutator, *corev1.Pod){
		"blocked checkpoint": func(m *Mutator, _ *corev1.Pod) {
			m.CRIURestoreBlocked = func(context.Context, string) bool { return true }
		},
		"not a CRIU capture": func(m *Mutator, _ *corev1.Pod) {
			m.L2Backend = &criuL2Backend{man: checkpointstore.Manifest{CaptureMethod: "cachedir"}}
		},
		"no checkpoint id": func(m *Mutator, _ *corev1.Pod) {
			man := criuCaptureRecord()
			man.SourcePodMeta = map[string]string{"engine": "criu"}
			m.L2Backend = &criuL2Backend{man: man}
		},
		// The restore-from annotation is the pod author's: a hash of
		// another tenant's capture must not restore its memory here.
		"capture of another namespace": func(m *Mutator, _ *corev1.Pod) {
			man := criuCaptureRecord()
			man.SourcePodMeta[checkpointstore.CRIUScopeMetaKey] = CRIUNamespaceScope("tenant-a")
			m.L2Backend = &criuL2Backend{man: man}
		},
		"capture of another function version": func(m *Mutator, _ *corev1.Pod) {
			man := criuCaptureRecord()
			man.SourcePodMeta[checkpointstore.CRIUScopeMetaKey] = "function-version/a"
			m.L2Backend = &criuL2Backend{man: man}
			m.CRIUScope = func(context.Context, string) (string, error) { return "function-version/b", nil }
		},
		"capture records no scope": func(m *Mutator, _ *corev1.Pod) {
			man := criuCaptureRecord()
			delete(man.SourcePodMeta, checkpointstore.CRIUScopeMetaKey)
			m.L2Backend = &criuL2Backend{man: man}
		},
		"scope unreadable": func(m *Mutator, _ *corev1.Pod) {
			m.CRIUScope = func(context.Context, string) (string, error) { return "", errors.New("apiserver unavailable") }
		},
		"pod namespace unknown": func(_ *Mutator, pod *corev1.Pod) {
			pod.Namespace = ""
		},
		// A rank of an instance capture restores only with its instance.
		"rank of an instance capture": func(m *Mutator, _ *corev1.Pod) {
			man := criuCaptureRecord()
			man.SourcePodMeta[checkpointstore.InstanceCaptureMetaKey] = "true"
			m.L2Backend = &criuL2Backend{man: man}
		},
	}
	for name, mod := range cases {
		m := criuMutator(t, criuCaptureRecord())
		pod := podWithAnnotation("abc123")
		pod.Spec = criuFunctionPod().Spec
		mod(m, pod)
		patches, _ := m.Mutate(context.Background(), pod)
		got := applyPatches(t, pod, patches)
		if got.Annotations[CRIURestoreAnnotation] != "" || !reflect.DeepEqual(got.Spec.Containers[1].Command, []string{"vllm"}) {
			t.Errorf("%s: pod was turned into a restore placeholder", name)
		}
	}
}

func TestCRIUNodePreference_KeepsExistingAffinity(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution:  &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{}}},
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{Weight: 1}},
	}}}}
	got := applyPatches(t, pod, criuNodePreference(pod, []string{"n1"}))
	na := got.Spec.Affinity.NodeAffinity
	if na.RequiredDuringSchedulingIgnoredDuringExecution == nil || len(na.PreferredDuringSchedulingIgnoredDuringExecution) != 2 {
		t.Errorf("affinity = %+v; the function's own terms must stay and ours be added", na)
	}
}

// The scope comes from the namespace through CRIUScope: a capture of the
// same function version restores in another namespace.
func TestCRIURestore_SameScopeInAnotherNamespace(t *testing.T) {
	man := criuCaptureRecord()
	man.SourcePodMeta[checkpointstore.CRIUScopeMetaKey] = "function-version/v1"
	m := criuMutator(t, man)
	var asked string
	m.CRIUScope = func(_ context.Context, ns string) (string, error) { asked = ns; return "function-version/v1", nil }
	pod := podWithAnnotation("abc123")
	pod.Spec = criuFunctionPod().Spec
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	if got := applyPatches(t, pod, patches); got.Annotations[CRIURestoreAnnotation] != "abc123__20261008-101010" {
		t.Errorf("capture of the pod's own scope not restored: annotations %v", got.Annotations)
	}
	if asked != "default" {
		t.Errorf("scope asked for namespace %q, want the pod's", asked)
	}
}

// The record the agent writes at capture time lives in the main capture
// store (ConfigMaps), not on L2; the webhook must find it there.
func TestCRIURestore_FindsTheRecordInTheCaptureStore(t *testing.T) {
	m := &Mutator{Backend: &criuL2Backend{man: criuCaptureRecord()}, MainContainer: 1,
		CheckpointHostRoot: "/var/lib/containerd/nvsnap-checkpoints"}
	pod := podWithAnnotation("abc123")
	pod.Spec = criuFunctionPod().Spec
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	if got := applyPatches(t, pod, patches); got.Annotations[CRIURestoreAnnotation] != "abc123__20261008-101010" {
		t.Errorf("record in the capture store not used: annotations %v", got.Annotations)
	}
}

// On a CRIU cluster a GPU pod runs under gpushare without an annotation;
// "false" opts it out.
func TestGPUShareByDefault(t *testing.T) {
	m := &Mutator{GPUShareByDefault: true}
	pod := gpushareTestPod(false, nil)
	if got := applyPatches(t, pod, m.gpusharePatches(context.Background(), pod)); envOf(got.Spec.Containers[1], "LD_PRELOAD") == nil {
		t.Error("CRIU cluster: unannotated GPU pod did not get the library")
	}
	out := gpushareTestPod(false, nil)
	out.Annotations = map[string]string{GPUShareAnnotation: "false"}
	if p := m.gpusharePatches(context.Background(), out); p != nil {
		t.Errorf("opted-out pod patched: %v", p)
	}
	if p := (&Mutator{}).gpusharePatches(context.Background(), gpushareTestPod(false, nil)); p != nil {
		t.Error("cachedir cluster: unannotated pod patched")
	}
	// A Job's or a task's pod runs to completion and is never captured.
	for _, rp := range []corev1.RestartPolicy{corev1.RestartPolicyNever, corev1.RestartPolicyOnFailure} {
		task := gpushareTestPod(false, nil)
		task.Spec.RestartPolicy = rp
		if p := m.gpusharePatches(context.Background(), task); p != nil {
			t.Errorf("restartPolicy %s: a pod that runs to completion got the library", rp)
		}
		task.Annotations = map[string]string{GPUShareAnnotation: "true"}
		if got := applyPatches(t, task, m.gpusharePatches(context.Background(), task)); envOf(got.Spec.Containers[1], "LD_PRELOAD") == nil {
			t.Errorf("restartPolicy %s: the annotation must still opt the pod in", rp)
		}
	}
}

// An engine that spans pods runs under the library with fabric sharing on,
// so its multi-node NVLink memory stays usable; a single-pod engine does
// not get fabric, and the annotation wins either way.
func TestGPUShareByDefault_FabricForMultiPodEngines(t *testing.T) {
	m := &Mutator{GPUShareByDefault: true}
	flags := gpushareTestPod(false, nil)
	flags.Spec.Containers[1].Args = []string{"vllm serve m --tensor-parallel-size 8 --nnodes 2 --node-rank 0 --master-addr x"}
	lws := gpushareTestPod(false, nil)
	lws.Annotations = map[string]string{lwsSizeAnnotation: "2"}
	for name, pod := range map[string]*corev1.Pod{"multi-node flags": flags, "two-pod group": lws} {
		got := applyPatches(t, pod, m.gpusharePatches(context.Background(), pod))
		if envOf(got.Spec.Containers[1], "LD_PRELOAD") == nil {
			t.Errorf("%s: library not placed", name)
		}
		if e := envOf(got.Spec.Containers[1], gpushareFabricEnv); e == nil || e.Value != "1" {
			t.Errorf("%s: fabric sharing not on", name)
		}
		off := pod.DeepCopy()
		off.Annotations = map[string]string{lwsSizeAnnotation: "2", GPUShareFabricAnnotation: "false"}
		if e := envOf(applyPatches(t, off, m.gpusharePatches(context.Background(), off)).Spec.Containers[1], gpushareFabricEnv); e != nil {
			t.Errorf("%s: the annotation did not switch fabric off", name)
		}
	}
	single := applyPatches(t, gpushareTestPod(false, nil), m.gpusharePatches(context.Background(), gpushareTestPod(false, nil)))
	if envOf(single.Spec.Containers[1], gpushareFabricEnv) != nil {
		t.Error("single-pod engine got fabric sharing")
	}
}

// A gpushare checkpoint restores with the checkpoint's chunk store at the
// store path and the library mounted, and without the pod's own store.
func TestCRIURestore_GPUShareCheckpoint(t *testing.T) {
	man := criuCaptureRecord()
	man.SourcePodMeta["gpushare"] = "true"
	m := criuMutator(t, man)
	m.GPUShareByDefault = true
	pod := podWithAnnotation("abc123")
	pod.Spec = criuFunctionPod().Spec
	pod.Spec.Containers[1].Resources.Limits = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, patches)
	vols := map[string]string{}
	for _, v := range got.Spec.Volumes {
		if v.HostPath != nil {
			vols[v.Name] = v.HostPath.Path
		}
	}
	if vols[gpushareStoreVolume] != "/var/lib/containers/nvsnap-checkpoints/abc123__20261008-101010/gpushare" {
		t.Errorf("store = %q, want the checkpoint's chunk store", vols[gpushareStoreVolume])
	}
	if vols[gpushareLibVolume] == "" {
		t.Error("library not mounted")
	}
	var store, lib bool
	for _, vm := range got.Spec.Containers[1].VolumeMounts {
		store = store || (vm.MountPath == GPUShareStorePath && vm.SubPathExpr == "")
		lib = lib || vm.MountPath == nvsnapToolsMountPath
	}
	if !store || !lib {
		t.Errorf("mounts: store=%v lib=%v (store must not be the per-pod subpath)", store, lib)
	}
}

// The placeholder is the container the capture checkpointed: the first
// with GPUs, even when the chart puts it before a sidecar.
func TestCRIURestore_PlaceholderIsTheGPUContainer(t *testing.T) {
	pod := criuFunctionPod()
	pod.Spec.Containers = []corev1.Container{pod.Spec.Containers[1], pod.Spec.Containers[0]}
	pod.Spec.Containers[0].Resources.Limits = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}
	m := criuMutator(t, criuCaptureRecord())
	patches, err := m.criuRestorePatches(pod, "abc123__20261008-101010", criuCaptureRecord())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range patches {
		if strings.HasPrefix(p.Path, "/spec/containers/1/") {
			t.Errorf("patched the sidecar: %s %s", p.Op, p.Path)
		}
	}
	if gpuContainerIndex(criuFunctionPod(), 1) != 1 {
		t.Error("a pod with no GPU limit keeps the configured main container")
	}
}

// A placeholder runs only the init containers whose output the restored
// process can see: those writing a volume the engine mounts. The others
// only wait or check, and a wait on a peer that is itself a placeholder
// (a Dynamo worker waiting for its leader) would never end.
func TestCRIURestore_PlaceholderSkipsInitsWithNoSharedOutput(t *testing.T) {
	alwaysRestart := corev1.ContainerRestartPolicyAlways
	m := criuMutator(t, criuCaptureRecord())
	pod := podWithAnnotation("abc123")
	pod.Spec = criuFunctionPod().Spec
	pod.Spec.Volumes = []corev1.Volume{
		{Name: "models", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "scripts", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}}},
		{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	pod.Spec.Containers[1].VolumeMounts = []corev1.VolumeMount{{Name: "models", MountPath: "/config/models"}, {Name: "scripts", MountPath: "/scripts"}}
	pod.Spec.InitContainers = []corev1.Container{
		{Name: "download-model", Image: "img", Command: []string{"download"}, VolumeMounts: []corev1.VolumeMount{{Name: "models", MountPath: "/config/models"}}},
		{Name: "wait-for-leader", Image: "img", Command: []string{"sh", "-c"}, Args: []string{"wait"}, VolumeMounts: []corev1.VolumeMount{{Name: "scripts", MountPath: "/scripts"}}},
		{Name: "wait-for-nats", Image: "curl", Command: []string{"sh", "-c", "until curl nats; do sleep 5; done"}},
		{Name: "own-scratch", Image: "img", Command: []string{"prep"}, VolumeMounts: []corev1.VolumeMount{{Name: "scratch", MountPath: "/s"}}},
		// A native sidecar serves for the pod's life (a Dynamo model-ready
		// beacon): skipped, it would exit and be restarted for ever.
		{Name: "beacon", Image: "img", Command: []string{"serve"}, RestartPolicy: &alwaysRestart},
	}
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, patches)
	run := map[string]bool{}
	for _, c := range got.Spec.InitContainers {
		skipped := len(c.Command) > 1 && c.Command[0] == HelperBin && c.Command[1] == "skip"
		run[c.Name] = !skipped
		if skipped {
			hasHelper := false
			for _, vm := range c.VolumeMounts {
				hasHelper = hasHelper || vm == helperMount()
			}
			if len(c.Args) != 0 || !hasHelper {
				t.Errorf("%s: a skipped init runs the helper with no args: %v %v", c.Name, c.Args, c.VolumeMounts)
			}
		}
	}
	want := map[string]bool{"download-model": true, "wait-for-leader": false, "wait-for-nats": false, "own-scratch": false, "beacon": true}
	for n, w := range want {
		if run[n] != w {
			t.Errorf("%s runs = %v, want %v", n, run[n], w)
		}
	}
}

// A companion's checkpoint names its container (one of several, picked by
// its connections to the ranks); the placeholder is that container, and the
// pod's other containers run unchanged.
func TestCRIURestore_PlaceholderIsTheRecordedContainer(t *testing.T) {
	pod := criuFunctionPod()
	pod.Spec.Containers[0].Name, pod.Spec.Containers[1].Name = "frontend-readiness-http", "main"
	for i := range pod.Spec.Containers {
		pod.Spec.Containers[i].Resources.Limits = nil
	}
	man := criuCaptureRecord()
	man.SourcePodMeta[restoreContainerMeta] = "main"
	m := criuMutator(t, man)
	patches, err := m.criuRestorePatches(pod, "abc123__20261008-101010", man)
	if err != nil {
		t.Fatal(err)
	}
	var placeholder bool
	for _, p := range patches {
		if strings.HasPrefix(p.Path, "/spec/containers/0/") {
			t.Errorf("patched the sidecar: %s %s", p.Op, p.Path)
		}
		placeholder = placeholder || p.Path == "/spec/containers/1/command"
	}
	if !placeholder {
		t.Error("the recorded container did not become the placeholder")
	}
	man.SourcePodMeta[restoreContainerMeta] = "gone"
	if _, err := m.criuRestorePatches(pod, "abc123__20261008-101010", man); err == nil {
		t.Error("restored into a pod without the recorded container")
	}
}

func TestWithPIDReserve(t *testing.T) {
	share := true
	base := []PatchOp{{Op: "replace", Path: "/spec/initContainers/1/command", Value: []string{"skip"}}}
	plain := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "img"}}}}
	if got := withPIDReserve(plain, base); len(got) != 1 {
		t.Fatalf("a pod with its own pid namespaces needs no reserve step: %+v", got)
	}
	shared := plain.DeepCopy()
	shared.Spec.ShareProcessNamespace = &share
	shared.Spec.InitContainers = []corev1.Container{{Name: "a"}, {Name: "b"}}
	got := withPIDReserve(shared, base)
	last := got[len(got)-1]
	c, ok := last.Value.(corev1.Container)
	if last.Op != "add" || last.Path != "/spec/initContainers/0" || !ok || c.Name != PIDReserveInit || c.Image != "img" {
		t.Fatalf("reserve step must go first, added last: %+v", last)
	}
	if len(c.Command) < 2 || c.Command[0] != HelperBin || c.Command[1] != "await-pid-floor" {
		t.Errorf("command: %v", c.Command)
	}
	// No init containers: the list is created, or joined when another
	// patch created it.
	none := plain.DeepCopy()
	none.Spec.ShareProcessNamespace = &share
	if got := withPIDReserve(none, nil); got[0].Path != "/spec/initContainers" {
		t.Errorf("no list: %+v", got)
	}
	boot := []PatchOp{{Op: "add", Path: "/spec/initContainers", Value: []corev1.Container{{Name: "x"}}}}
	if got := withPIDReserve(none, boot); got[len(got)-1].Path != "/spec/initContainers/0" {
		t.Errorf("a list another patch created must be joined, not replaced: %+v", got)
	}
}

// A placeholder's readiness waits on the agent's restored condition, next
// to any gate the pod already has, and the gate is added once.
func TestCRIURestore_ReadinessWaitsOnTheRestore(t *testing.T) {
	for name, gates := range map[string][]corev1.PodReadinessGate{
		"no gates":   nil,
		"own gate":   {{ConditionType: "example.com/ready"}},
		"gate there": {{ConditionType: CRIURestoredCondition}},
	} {
		pod := criuFunctionPod()
		pod.Spec.ReadinessGates = gates
		got := applyPatches(t, pod, restoredGatePatches(pod)).Spec.ReadinessGates
		n := 0
		for _, g := range got {
			if g.ConditionType == CRIURestoredCondition {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: %d restored gates in %v, want 1", name, n, got)
		}
		if len(gates) == 1 && gates[0].ConditionType != CRIURestoredCondition && (len(got) != 2 || got[0] != gates[0]) {
			t.Errorf("%s: the pod's own gate was not kept: %v", name, got)
		}
	}
	m := criuMutator(t, criuCaptureRecord())
	patches, err := m.criuRestorePatches(criuFunctionPod(), "abc123__20261008-101010", criuCaptureRecord())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range patches {
		found = found || p.Path == "/spec/readinessGates"
	}
	if !found {
		t.Error("the placeholder's readiness does not wait on the restore")
	}
}

// A gpushare restore mounts the checkpoint's chunk store read-only (a
// restored pod is never captured into it), and a pod that uses the
// restore's work path or volume itself is not admitted as a placeholder.
func TestCRIURestore_StoreReadOnlyAndWorkVolumeReserved(t *testing.T) {
	man := criuCaptureRecord()
	man.SourcePodMeta["gpushare"] = "true"
	m := criuMutator(t, man)
	pod := criuFunctionPod()
	patches, err := m.criuRestorePatches(pod, "abc123__20261008-101010", man)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, mergePatchPlan(patches))
	store := false
	for i := range got.Spec.Containers {
		if vm := mountAt(got.Spec.Containers[i], GPUShareStorePath); vm != nil {
			store = true
			if !vm.ReadOnly {
				t.Error("the checkpoint's chunk store is mounted writable")
			}
		}
	}
	if !store {
		t.Fatal("no chunk store mounted")
	}
	declares := criuFunctionPod()
	declares.Spec.Volumes = append(declares.Spec.Volumes, corev1.Volume{Name: restoreWorkVolume})
	mounts := criuFunctionPod()
	mounts.Spec.Volumes = append(mounts.Spec.Volumes, corev1.Volume{Name: "x"})
	mounts.Spec.Containers[0].VolumeMounts = append(mounts.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "x", MountPath: RestoreWorkMountPath})
	for what, p := range map[string]*corev1.Pod{"declares the work volume": declares, "mounts the work path": mounts} {
		if _, err := m.criuRestorePatches(p, "abc123__20261008-101010", man); err == nil {
			t.Errorf("%s: admitted as a placeholder", what)
		}
	}
}

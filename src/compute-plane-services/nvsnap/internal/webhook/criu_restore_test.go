// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
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
		SourcePodMeta:   map[string]string{"engine": "criu", "checkpoint_id": "abc123__20261008-101010"},
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
	if !reflect.DeepEqual(c.Command, []string{"/bin/sh", "-c"}) || len(c.Args) != 1 || c.Args[0] != criuPlaceholderScript {
		t.Errorf("engine container not idled: %v %v", c.Command, c.Args)
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
	var mounted bool
	for _, vm := range c.VolumeMounts {
		mounted = mounted || (vm.MountPath == "/checkpoints" && vm.ReadOnly)
	}
	var hostRoot string
	for _, v := range got.Spec.Volumes {
		if v.Name == criuCheckpointsVolume && v.HostPath != nil {
			hostRoot = v.HostPath.Path
		}
	}
	if !mounted || hostRoot != "/var/lib/containers/nvsnap-checkpoints" {
		t.Errorf("checkpoints not mounted at /checkpoints from the host root (mounted=%v root=%q)", mounted, hostRoot)
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
		"gpushare pod": func(_ *Mutator, p *corev1.Pod) { p.Annotations[GPUShareAnnotation] = "true" },
		"not a CRIU capture": func(m *Mutator, _ *corev1.Pod) {
			m.L2Backend = &criuL2Backend{man: checkpointstore.Manifest{CaptureMethod: "cachedir"}}
		},
		"no checkpoint id": func(m *Mutator, _ *corev1.Pod) {
			man := criuCaptureRecord()
			man.SourcePodMeta = map[string]string{"engine": "criu"}
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

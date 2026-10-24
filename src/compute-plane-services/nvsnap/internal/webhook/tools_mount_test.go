// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// toolsVolumeOf is the pod's bundle tools volume, nil when it has none.
func toolsVolumeOf(pod *corev1.Pod) *corev1.Volume {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == bundleToolsVolume {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

// checkToolsMount fails unless container c mounts version's bundle
// read-only at BundleToolsMountPath.
func checkToolsMount(t *testing.T, what string, pod *corev1.Pod, c int, version string) {
	t.Helper()
	v := toolsVolumeOf(pod)
	if v == nil || v.HostPath == nil || v.HostPath.Path != "/bundle/versions/"+version {
		t.Fatalf("%s: tools volume %+v, want the hostPath of version %s", what, v, version)
	}
	vm := mountAt(pod.Spec.Containers[c], BundleToolsMountPath)
	if vm == nil || vm.Name != bundleToolsVolume || !vm.ReadOnly {
		t.Errorf("%s: container %s mount at %s = %+v, want the tools volume read-only", what, pod.Spec.Containers[c].Name, BundleToolsMountPath, vm)
	}
}

// A GPU pod a capture may dump gets the bundle read-only in its GPU
// containers, beside the library; the sidecar does not.
func TestGPUSharePatches_MountsTheTools(t *testing.T) {
	m := &Mutator{HostBundleRoot: "/bundle", BundleID: "v7", GPUShareHostRoot: "/ckpt/gpushare-pods"}
	pod := gpushareTestPod(true, nil)
	got := applyPatches(t, pod, m.gpusharePatches(context.Background(), pod))
	checkToolsMount(t, "GPU pod", got, 1, "v7")
	if mountAt(got.Spec.Containers[0], BundleToolsMountPath) != nil {
		t.Error("the sidecar got the tools")
	}
}

// A pod that mounts something at the tools path, or declares the volume,
// is left without the tools (the agent then refuses to run them there),
// while its other placement goes ahead.
func TestBundleTools_PodsOwnUseIsLeftAlone(t *testing.T) {
	m := &Mutator{HostBundleRoot: "/bundle", BundleID: "v7", GPUShareHostRoot: "/ckpt/gpushare-pods"}
	mine := gpushareTestPod(true, nil)
	mine.Spec.Volumes = []corev1.Volume{{Name: "mine", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	mine.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "mine", MountPath: BundleToolsMountPath}}
	named := gpushareTestPod(true, nil)
	named.Spec.Volumes = []corev1.Volume{{Name: bundleToolsVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	for what, pod := range map[string]*corev1.Pod{"mounts the path": mine, "declares the volume": named} {
		got := applyPatches(t, pod, m.gpusharePatches(context.Background(), pod))
		if v := toolsVolumeOf(got); v != nil && v.HostPath != nil {
			t.Errorf("%s: the tools were mounted: %+v", what, v)
		}
		if envOf(got.Spec.Containers[1], "LD_PRELOAD") == nil {
			t.Errorf("%s: the library was not placed", what)
		}
	}
}

// A companion a capture may dump gets the bundle in every container; a pod
// no capture dumps gets nothing.
func TestCompanionToolsPatches(t *testing.T) {
	candidate := false
	m := &Mutator{HostBundleRoot: "/bundle", BundleID: "v7",
		CaptureCandidate: func(context.Context, *corev1.Pod) bool { return candidate }}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "etcd", Image: "etcd"}, {Name: "metrics", Image: "m"}}}}
	if p := m.companionToolsPatches(context.Background(), pod); p != nil {
		t.Errorf("a pod no capture dumps was patched: %v", p)
	}
	candidate = true
	got := applyPatches(t, pod, m.companionToolsPatches(context.Background(), pod))
	checkToolsMount(t, "companion", got, 0, "v7")
	checkToolsMount(t, "companion", got, 1, "v7")
	if p := m.companionToolsPatches(context.Background(), gpushareTestPod(true, nil)); p != nil {
		t.Error("a GPU pod got the companion placement (gpusharePatches places its tools)")
	}
}

// A restore placeholder runs the tools of the version its checkpoint
// recorded; a pod that uses the tools path itself is not admitted as one.
func TestCRIURestore_MountsTheRecordedTools(t *testing.T) {
	man := criuCaptureRecord()
	man.SourcePodMeta[BundleMeta] = "v3"
	m := criuMutator(t, man)
	m.HostBundleRoot, m.BundleID = "/bundle", "v7"
	pod := criuFunctionPod()
	patches, err := m.criuRestorePatches(pod, "abc123__20261008-101010", man)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, mergePatchPlan(patches))
	placeholder := -1
	for i := range got.Spec.Containers {
		if mountAt(got.Spec.Containers[i], BundleToolsMountPath) != nil {
			placeholder = i
		}
	}
	if placeholder < 0 {
		t.Fatal("the placeholder has no tools")
	}
	checkToolsMount(t, "placeholder", got, placeholder, "v3")

	own := criuFunctionPod()
	own.Spec.Volumes = append(own.Spec.Volumes, corev1.Volume{Name: "x", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	own.Spec.Containers[0].VolumeMounts = append(own.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "x", MountPath: BundleToolsMountPath})
	if _, err := m.criuRestorePatches(own, "abc123__20261008-101010", man); err == nil {
		t.Error("a pod that mounts the tools path itself was admitted as a placeholder")
	}
}

// A pod that mounts a volume only the webhook adds, without declaring it,
// gets no placement at all: the webhook's volume would give its mount the
// node's directory. Declared by the pod itself, the name is the pod's own.
func TestMutate_UndeclaredReservedMount(t *testing.T) {
	m := criuMutator(t, criuCaptureRecord())
	m.HostBundleRoot, m.BundleID, m.GPUShareHostRoot = "/bundle", "v7", "/ckpt/gpushare-pods"
	// The same pod without the mount is placed: a refusal below is the
	// rule's, not a pod the webhook leaves alone anyway.
	if patches, err := m.Mutate(context.Background(), gpushareTestPod(true, nil)); err != nil || len(patches) == 0 {
		t.Fatalf("the plain pod was not placed: %d patches (%v)", len(patches), err)
	}
	for _, v := range []string{gpushareLibVolume, helperVolumeName, criuCheckpointsVolume, bundleToolsVolume} {
		pod := gpushareTestPod(true, nil)
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: v, MountPath: "/x"})
		if patches, err := m.Mutate(context.Background(), pod); err != nil || patches != nil {
			t.Errorf("%s: mounted undeclared, got %d patches (%v)", v, len(patches), err)
		}
	}
	own := gpushareTestPod(true, nil)
	own.Spec.Volumes = append(own.Spec.Volumes, corev1.Volume{Name: helperVolumeName})
	own.Spec.Containers[0].VolumeMounts = append(own.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: helperVolumeName, MountPath: "/x"})
	if why := undeclaredReservedMount(own); why != "" {
		t.Errorf("a pod that declares the volume itself refused: %s", why)
	}
}

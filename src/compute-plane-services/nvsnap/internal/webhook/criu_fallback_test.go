// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

const fbCheckpoints = "/var/lib/containerd/nvsnap-checkpoints"

// fallbackModelMutator is a CRIU cluster's mutator for a model reader,
// every GPU pod under the library, whose configurations fall back as fb
// says (none when ok is false).
func fallbackModelMutator(t *testing.T, fb CRIUFallback, ok bool) (*Mutator, *corev1.Pod, *int) {
	t.Helper()
	kc := fake.NewSimpleClientset()
	m, _ := mvMutatorReader(t, modelvolume.ModeBlock, "", election.RoleFollower, kc)
	m.CRIUCapture, m.Kube, m.GPUShareByDefault, m.CheckpointHostRoot = true, kc, true, fbCheckpoints
	asked := 0
	m.CRIUFallback = func(_ context.Context, ns, uri string, ordinal int) (CRIUFallback, bool) {
		asked++
		if ns != "sr-fn" || !strings.HasPrefix(uri, "cache://") || ordinal != 0 {
			t.Errorf("asked for %s %s %d", ns, uri, ordinal)
		}
		return fb, ok
	}
	completeModelPV(t, kc, scoped("sr-fn", ngcFunctionKey))
	return m, ngcFunctionPod(), &asked
}

func admit(t *testing.T, m *Mutator, pod *corev1.Pod) *corev1.Pod {
	t.Helper()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	return applyPatches(t, pod, patches)
}

func fbEnv(c corev1.Container) map[string]string {
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	return env
}

func volumeNamed(p *corev1.Pod, name string) *corev1.Volume {
	for i := range p.Spec.Volumes {
		if p.Spec.Volumes[i].Name == name {
			return &p.Spec.Volumes[i]
		}
	}
	return nil
}

func initNamed(p *corev1.Pod, name string) *corev1.Container {
	for i := range p.Spec.InitContainers {
		if p.Spec.InitContainers[i].Name == name {
			return &p.Spec.InitContainers[i]
		}
	}
	return nil
}

// A model reader whose configuration does not start under the library
// goes without it, gets the cachedir, and is seeded from its rank's
// capture once the agent has it on the node.
func TestCRIUFallbackGPUShareFailedReader(t *testing.T) {
	id := FallbackCaptureID("k", 0)
	m, pod, _ := fallbackModelMutator(t, CRIUFallback{Reason: FallbackGPUShareFailed, Capture: id}, true)
	got := admit(t, m, pod)
	main := got.Spec.Containers[0]
	if strings.Contains(fbEnv(main)["LD_PRELOAD"], GPUShareLibPath) {
		t.Error("the library was placed for a configuration that does not start under it")
	}
	if v := volumeNamed(got, cacheDirVolumeName); v == nil || v.EmptyDir == nil {
		t.Fatalf("no cachedir: %v", got.Spec.Volumes)
	}
	if env := fbEnv(main); env["HOME"] != "/opt/nvsnap/cache" {
		t.Errorf("caches not in the cachedir: HOME=%q", env["HOME"])
	}
	a := got.Annotations
	if a[CaptureMethodAnnotation] != "cachedir" || a[FallbackReasonAnnotation] != FallbackGPUShareFailed || a[FallbackCaptureAnnotation] != id {
		t.Errorf("annotations %v", a)
	}
	capture, readyDir := volumeNamed(got, fallbackCaptureVolume), volumeNamed(got, fallbackReadyVolume)
	if capture == nil || capture.HostPath == nil || capture.HostPath.Path != fbCheckpoints+"/"+id {
		t.Fatalf("capture volume %+v", capture)
	}
	if readyDir == nil || readyDir.HostPath == nil || readyDir.HostPath.Path != fbCheckpoints+"/"+FallbackReadySubdir+"/"+id {
		t.Fatalf("ready volume %+v", readyDir)
	}
	seed := initNamed(got, fallbackSeedInit)
	if seed == nil {
		t.Fatal("no seed")
	}
	cmd := strings.Join(seed.Command, " ")
	for _, want := range []string{"seed --from " + fallbackCaptureMount + "/" + FallbackCaptureTree + " --to /opt/nvsnap",
		"--marker " + fallbackReadyMount + "/" + FallbackReadyFile, "--deadline 900",
		"--links " + fallbackCaptureMount + "/" + FallbackManifestFile} {
		if !strings.Contains(cmd, want) {
			t.Errorf("seed %q lacks %q", cmd, want)
		}
	}
	for _, c := range got.Spec.Containers {
		for _, vm := range c.VolumeMounts {
			if vm.Name == fallbackCaptureVolume || vm.Name == fallbackReadyVolume {
				t.Errorf("container %s mounts %s: only the seed may", c.Name, vm.Name)
			}
		}
	}
	if initNamed(got, cacheSeedInitName) != nil || got.Labels[CacheCaptureLabel] != "" {
		t.Error("the compile-cache set ran for a fallback pod")
	}
}

// A capture or restore that failed keeps the library (a later CRIU attempt
// needs it); without a capture of its rank yet the pod is only stamped for
// one.
func TestCRIUFallbackFailedKeepsTheLibrary(t *testing.T) {
	m, pod, _ := fallbackModelMutator(t, CRIUFallback{Reason: "failed"}, true)
	got := admit(t, m, pod)
	if !strings.Contains(fbEnv(got.Spec.Containers[0])["LD_PRELOAD"], GPUShareLibPath) {
		t.Error("the library was dropped for a capture that failed")
	}
	if got.Annotations[FallbackReasonAnnotation] != "failed" || got.Annotations[FallbackCaptureAnnotation] != "" {
		t.Errorf("annotations %v", got.Annotations)
	}
	if volumeNamed(got, cacheDirVolumeName) == nil || initNamed(got, fallbackSeedInit) != nil || volumeNamed(got, fallbackCaptureVolume) != nil {
		t.Error("want the cachedir and no seed")
	}
}

// A configuration CRIU serves is decorated as before: no cachedir.
func TestCRIUFallbackNoneLeavesCRIUAlone(t *testing.T) {
	m, pod, asked := fallbackModelMutator(t, CRIUFallback{}, false)
	got := admit(t, m, pod)
	if *asked != 1 {
		t.Errorf("asked %d times", *asked)
	}
	if volumeNamed(got, cacheDirVolumeName) != nil || got.Annotations[FallbackReasonAnnotation] != "" {
		t.Error("a configuration CRIU serves fell back")
	}
	if !strings.Contains(fbEnv(got.Spec.Containers[0])["LD_PRELOAD"], GPUShareLibPath) {
		t.Error("the library is missing")
	}
}

// A capture name that is not one segment of the capture prefix never
// becomes a host path.
func TestCRIUFallbackRefusesOtherPaths(t *testing.T) {
	for _, bad := range []string{"../etc", "cachedir-k/../../x", "other-0", ".."} {
		m, pod, _ := fallbackModelMutator(t, CRIUFallback{Reason: "failed", Capture: bad}, true)
		got := admit(t, m, pod)
		if volumeNamed(got, fallbackCaptureVolume) != nil || got.Annotations[FallbackCaptureAnnotation] != "" {
			t.Errorf("%q became a capture mount", bad)
		}
	}
}

// A pod whose model is not in a model volume gets the cachedir with the
// model under it, as a labelled pod does without CRIU.
func TestCRIUFallbackWithoutModelVolume(t *testing.T) {
	id := FallbackCaptureID("k", 0)
	m := &Mutator{Backend: newBackend(t), CacheDir: "/opt/nvsnap", Composer: &rootfsonly.HashInputComposer{CUDADriverMajor: 610}, CRIUCapture: true,
		CheckpointHostRoot: fbCheckpoints,
		CRIUFallback: func(context.Context, string, string, int) (CRIUFallback, bool) {
			return CRIUFallback{Reason: "restore-failed", Capture: id}, true
		}}
	pod := gpuRank("engine-0", metav1.OwnerReference{Kind: "StatefulSet", Name: "engine"})
	got := admit(t, m, pod)
	env := fbEnv(got.Spec.Containers[0])
	if env["HOME"] != "/opt/nvsnap/cache" || !strings.HasPrefix(env["HF_HOME"], "/opt/nvsnap/model") {
		t.Errorf("env %v", env)
	}
	if volumeNamed(got, cacheDirVolumeName) == nil || initNamed(got, fallbackSeedInit) == nil || got.Annotations[FallbackCaptureAnnotation] != id {
		t.Errorf("want the cachedir seeded from %s: %v", id, got.Spec.InitContainers)
	}
}

// A rank whose group capture is usable restores from it; the fallback is
// not asked.
func TestCRIUFallbackNotForPlaceholders(t *testing.T) {
	g := CRIUGroup{Checkpoints: []string{"r0__1", "r1__1"}, Nodes: []string{"n0", "n1"}}
	m, _ := groupMutator(t, g, "")
	m.CRIUCapture, m.CacheDir = true, "/opt/nvsnap"
	m.CRIUFallback = func(context.Context, string, string, int) (CRIUFallback, bool) {
		t.Error("asked for a fallback")
		return CRIUFallback{Reason: "failed"}, true
	}
	pod := lwsRank("0")
	pod.Annotations[CRIUGroupAnnotation] = "set-by-a-previous-admission"
	got := admit(t, m, pod)
	if got.Annotations[FallbackReasonAnnotation] != "" {
		t.Error("a placeholder fell back")
	}
}

func TestFallbackCaptureID(t *testing.T) {
	if id := FallbackCaptureID("0123abcd", 3); id != "cachedir-0123abcd-3" || !validFallbackCapture(id) {
		t.Errorf("id %q", id)
	}
	if slices.Contains([]bool{validFallbackCapture("cachedir-a/b"), validFallbackCapture("x")}, true) {
		t.Error("an invalid capture name accepted")
	}
}

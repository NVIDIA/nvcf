// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/election"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
)

func mvMutator(t *testing.T, mode modelvolume.Mode, role election.Role, kc *fake.Clientset) (*Mutator, *fakeElector) {
	t.Helper()
	el := &fakeElector{role: role}
	return &Mutator{
		Backend:     newBackend(t),
		CacheDir:    "/opt/nvsnap",
		Composer:    &rootfsonly.HashInputComposer{CUDADriverMajor: 580},
		Elector:     el,
		ModelVolume: &modelvolume.Provisioner{Kube: kc, Cfg: modelvolume.Config{Mode: mode, StorageClass: "sc", Size: resource.MustParse("512Gi")}},
	}, el
}

// The prd11 function shape: NGC init download into an emptyDir, engine
// from a positional MODEL_PATH, two pods per instance.
func ngcFunctionPod() *corev1.Pod {
	gpu := corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "sr-fn", GenerateName: "mini-service-kimi-k3-"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{
				Name: "download-ngc-model", Image: "nvcr.io/org/ultra:vllm", Command: []string{"/bin/bash", "-c"},
				Args:         []string{"set -euo pipefail\nngc registry model download-version --dest \"${NGC_MODEL_MOUNT}\" \"${NGC_MODEL_NAME}\"\n"},
				Env:          []corev1.EnvVar{{Name: "NGC_MODEL_NAME", Value: "org/team/nemotron3-ultra-genrm:bf16-fixed"}, {Name: "NGC_MODEL_MOUNT", Value: "/config/models"}},
				VolumeMounts: []corev1.VolumeMount{{Name: "ngc-models", MountPath: "/config/models"}},
			}},
			Containers: []corev1.Container{{
				Name: "kimi-k3", Image: "nvcr.io/org/ultra:vllm", Command: []string{"/bin/bash", "/opt/kimi-k3/start.sh"},
				Env:          []corev1.EnvVar{{Name: "MODEL_PATH", Value: "/config/models/nemotron3-ultra-genrm"}, {Name: "HF_HUB_OFFLINE", Value: "1"}},
				Resources:    gpu,
				VolumeMounts: []corev1.VolumeMount{{Name: "dshm", MountPath: "/dev/shm"}, {Name: "ngc-models", MountPath: "/config/models"}},
			}},
			Volumes: []corev1.Volume{
				{Name: "dshm", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "ngc-models", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

func stockVLLMPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "fn", GenerateName: "vllm-"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "vllm", Image: "vllm/vllm-openai:v0.20.0", Command: []string{"/bin/bash", "-lc"},
			Args:      []string{"vllm serve --model Qwen/Qwen2.5-32B-Instruct --tensor-parallel-size 4"},
			Env:       []corev1.EnvVar{{Name: "HF_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: "token"}}}},
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}},
		}}},
	}
}

type mvView struct {
	labels, annotations map[string]string
	volumes             map[string]corev1.Volume // by name, from replace/add ops
	roMounts            []string
	initScripts         map[string]string
	newInits            []corev1.Container
	env                 map[string]string
}

func viewMV(pod *corev1.Pod, patches []PatchOp) mvView {
	v := mvView{labels: map[string]string{}, annotations: map[string]string{}, volumes: map[string]corev1.Volume{}, initScripts: map[string]string{}, env: map[string]string{}}
	for _, p := range patches {
		switch {
		case strings.HasPrefix(p.Path, "/metadata/labels/"):
			v.labels[strings.ReplaceAll(strings.TrimPrefix(p.Path, "/metadata/labels/"), "~1", "/")] = p.Value.(string)
		case strings.HasPrefix(p.Path, "/metadata/annotations/"):
			v.annotations[strings.ReplaceAll(strings.TrimPrefix(p.Path, "/metadata/annotations/"), "~1", "/")] = p.Value.(string)
		case strings.HasSuffix(p.Path, "/readOnly") && p.Value == true:
			v.roMounts = append(v.roMounts, p.Path)
		case strings.HasPrefix(p.Path, "/spec/initContainers/") && strings.HasSuffix(p.Path, "/args"):
			idx := strings.Split(strings.TrimPrefix(p.Path, "/spec/initContainers/"), "/")[0]
			v.initScripts[pod.Spec.InitContainers[atoi(idx)].Name] = p.Value.([]string)[0]
		}
		switch val := p.Value.(type) {
		case corev1.Volume:
			v.volumes[val.Name] = val
		case corev1.Container:
			if strings.HasPrefix(p.Path, "/spec/initContainers") {
				v.newInits = append(v.newInits, val)
			}
		case corev1.EnvVar:
			v.env[val.Name] = val.Value
		}
	}
	return v
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestModelVolume_WriterBlock_NGCInit(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, el := mvMutator(t, modelvolume.ModeBlock, election.RoleLeader, kc)
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	if el.called != 1 || v.annotations[modelvolume.IdentityAnnotation] != uri || v.labels[modelvolume.RoleLabel] != "writer" {
		t.Errorf("writer stamp: elected=%d ann=%v labels=%v", el.called, v.annotations, v.labels)
	}
	vol, ok := v.volumes["ngc-models"]
	if !ok || vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != modelvolume.ClaimName(uri) {
		t.Errorf("landing emptyDir must be replaced by the writer claim, got %+v", vol)
	}
	if pvc, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(context.Background(), modelvolume.ClaimName(uri), metav1.GetOptions{}); err != nil || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("writer claim must exist RWO in the pod namespace: %v", err)
	}
	if len(v.roMounts) != 1 || !strings.Contains(v.roMounts[0], "/spec/containers/0/volumeMounts/1/") {
		t.Errorf("engine's model mount must become read-only, got %v", v.roMounts)
	}
	s := v.initScripts["download-ngc-model"]
	if !strings.Contains(s, "ngc registry model download-version") || !strings.Contains(s, "touch /config/models/.nvsnap-complete") || !strings.Contains(s, "already complete") {
		t.Errorf("writer init must run the original download then touch the marker:\n%s", s)
	}
	if v.env["TORCHINDUCTOR_CACHE_DIR"] != "/opt/nvsnap/cache/torchinductor" || v.env["HF_HOME"] != "" || v.env["NIM_CACHE_PATH"] != "" {
		t.Errorf("Block mode: compile caches to the local cachedir, model env untouched: %v", v.env)
	}
	if _, ok := v.volumes[cacheDirVolumeName]; !ok {
		t.Error("Block mode must add the local cachedir emptyDir for the compile caches")
	}
}

func TestModelVolume_ReaderBlock_PendingBind(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutator(t, modelvolume.ModeBlock, election.RoleFollower, kc)
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	if v.labels[modelvolume.RoleLabel] != "reader" || v.labels[modelvolume.PendingLabel] != "true" || v.annotations[modelvolume.LandingAnnotation] != "/config/models" {
		t.Errorf("Block reader stamp: %v %v", v.labels, v.annotations)
	}
	if _, replaced := v.volumes["ngc-models"]; replaced {
		t.Error("Block reader keeps its emptyDir; the agent binds the volume over it")
	}
	s := v.initScripts["download-ngc-model"]
	if !strings.Contains(s, "while [ ! -f /config/models/.nvsnap-complete ]") || !strings.Contains(s, "ngc registry model download-version") || !strings.Contains(s, "deadline passed") {
		t.Errorf("reader init must wait for the marker and fall back to its own download:\n%s", s)
	}
	for _, p := range patches {
		if strings.HasPrefix(p.Path, "/spec/schedulingGates") {
			t.Fatal("no pod is ever gated on the model volume path")
		}
	}
	if pvcs, _ := kc.CoreV1().PersistentVolumeClaims("").List(context.Background(), metav1.ListOptions{}); len(pvcs.Items) != 0 {
		t.Error("a Block reader must not create claims")
	}
}

func TestModelVolume_ReaderRWX_SharesClaim(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutator(t, modelvolume.ModeRWX, election.RoleFollower, kc)
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	vol := v.volumes["ngc-models"]
	if vol.PersistentVolumeClaim == nil || vol.PersistentVolumeClaim.ClaimName != modelvolume.ClaimName(uri) {
		t.Errorf("RWX reader mounts the shared claim, got %+v", vol)
	}
	if pvc, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(context.Background(), modelvolume.ClaimName(uri), metav1.GetOptions{}); err != nil || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Errorf("shared claim must be RWX: %v", err)
	}
	if v.labels[modelvolume.PendingLabel] != "" {
		t.Error("RWX readers are not pending; the filesystem delivers the marker")
	}
	if !strings.HasPrefix(v.env["TORCHINDUCTOR_CACHE_DIR"], "/config/models/.nvsnap/cache/") || !strings.HasSuffix(v.env["TORCHINDUCTOR_CACHE_DIR"], "/torchinductor") {
		t.Errorf("RWX mode: compile caches live in the shared volume under a config key, got %q", v.env["TORCHINDUCTOR_CACHE_DIR"])
	}
}

func TestModelVolume_EngineDownload_InjectedInit(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutator(t, modelvolume.ModeRWX, election.RoleLeader, kc)
	pod := stockVLLMPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	if len(v.newInits) != 1 || v.newInits[0].Name != "nvsnap-model-download" {
		t.Fatalf("engine-download writer needs an injected download init, got %v", v.newInits)
	}
	init := v.newInits[0]
	s := init.Args[0]
	if !strings.Contains(s, "huggingface-cli download Qwen/Qwen2.5-32B-Instruct") || !strings.Contains(s, "touch /root/.cache/huggingface/.nvsnap-complete") {
		t.Errorf("injected init script:\n%s", s)
	}
	if init.Image != pod.Spec.Containers[0].Image || len(init.VolumeMounts) != 1 || init.VolumeMounts[0].MountPath != "/root/.cache/huggingface" {
		t.Errorf("init must reuse the engine image and mount the model volume at HF_HOME: %+v", init)
	}
	var sawToken bool
	for _, e := range init.Env {
		if e.Name == "HF_TOKEN" && e.ValueFrom != nil {
			sawToken = true
		}
	}
	if !sawToken {
		t.Error("registry credentials must be forwarded to the download init")
	}
	if v.env["HF_HUB_OFFLINE"] != "1" {
		t.Error("engine must start offline and read the volume")
	}
	vol, ok := v.volumes[modelVolumeName]
	if !ok || vol.PersistentVolumeClaim == nil {
		t.Errorf("rootfs landing gets a new claim volume: %+v", vol)
	}
}

func TestModelVolume_CompleteSkipsElection(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, el := mvMutator(t, modelvolume.ModeRWX, election.RoleLeader, kc)
	uri := "hf://Qwen/Qwen2.5-32B-Instruct"
	if _, err := m.ModelVolume.EnsureWriterClaim(context.Background(), uri, "fn"); err != nil {
		t.Fatal(err)
	}
	if err := m.ModelVolume.MarkComplete(context.Background(), uri, "fn"); err != nil {
		t.Fatal(err)
	}
	patches, err := m.Mutate(context.Background(), stockVLLMPod())
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(stockVLLMPod(), patches)
	if el.called != 0 || v.labels[modelvolume.RoleLabel] != "reader" {
		t.Errorf("complete volume: no election, reader role; elected=%d labels=%v", el.called, v.labels)
	}
}

func TestModelVolume_LeavesOthersAlone(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, el := mvMutator(t, modelvolume.ModeRWX, election.RoleLeader, kc)
	customer := stockVLLMPod()
	customer.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "models", MountPath: "/root/.cache/huggingface"}}
	customer.Spec.Volumes = []corev1.Volume{{Name: "models", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "their-nfs"}}}}
	if p, err := m.modelVolumePatches(context.Background(), customer); err != nil || p != nil {
		t.Errorf("customer PVC at HF_HOME must be left alone: %v %v", p, err)
	}
	noGPU := stockVLLMPod()
	noGPU.Spec.Containers[0].Resources = corev1.ResourceRequirements{}
	if p, _ := m.modelVolumePatches(context.Background(), noGPU); p != nil {
		t.Error("a pod without GPUs is not a model worker")
	}
	frontend := stockVLLMPod()
	frontend.Spec.Containers[0].Args = []string{"python3 -m dynamo.frontend --router-mode kv"}
	if p, _ := m.modelVolumePatches(context.Background(), frontend); p != nil {
		t.Error("a pod naming no model is left alone")
	}
	if el.called != 0 {
		t.Error("no election for pods that are left alone")
	}
}

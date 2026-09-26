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

func TestModelVolume_FirstPodBlock_NGCInit_CreatesJob(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, el := mvMutator(t, modelvolume.ModeBlock, election.RoleLeader, kc)
	pod := ngcFunctionPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	uri := "ngc://org/team/nemotron3-ultra-genrm:bf16-fixed"
	if el.called != 0 {
		t.Error("the download step is a Job; no election runs")
	}
	if v.annotations[modelvolume.IdentityAnnotation] != uri || v.labels[modelvolume.RoleLabel] != "reader" || v.labels[modelvolume.PendingLabel] != "true" {
		t.Errorf("every pod is a reader: ann=%v labels=%v", v.annotations, v.labels)
	}
	pvc, err := kc.CoreV1().PersistentVolumeClaims("sr-fn").Get(context.Background(), modelvolume.ClaimName(uri), metav1.GetOptions{})
	if err != nil || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("download claim must exist RWO in the pod namespace: %v", err)
	}
	job, err := kc.BatchV1().Jobs("sr-fn").Get(context.Background(), modelvolume.JobName(uri), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("download Job must be created: %v", err)
	}
	jc := job.Spec.Template.Spec.Containers[0]
	script := jc.Args[0]
	if jc.Image != "nvcr.io/org/ultra:vllm" || !strings.Contains(script, "ngc registry model download-version") || !strings.Contains(script, "touch /config/models/.nvsnap-complete") {
		t.Errorf("job must run the chart's own download then touch the marker:\n%s", script)
	}
	if len(jc.Env) != 2 || jc.VolumeMounts[0].MountPath != "/config/models" || job.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != modelvolume.ClaimName(uri) {
		t.Errorf("job must carry the init's env and mount the claim at the init's path: env=%v mounts=%v", jc.Env, jc.VolumeMounts)
	}
	s := v.initScripts["download-ngc-model"]
	if !strings.Contains(s, "while [ ! -f /config/models/.nvsnap-complete ]") {
		t.Errorf("the pod's own init becomes a wait:\n%s", s)
	}
	if v.env["TORCHINDUCTOR_CACHE_DIR"] != "/opt/nvsnap/cache/torchinductor" || v.env["HF_HOME"] != "" {
		t.Errorf("Block mode: compile caches to the local cachedir, model env untouched: %v", v.env)
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
	vol, replaced := v.volumes["ngc-models"]
	if !replaced || vol.HostPath == nil || vol.HostPath.Path != "/var/lib/containerd/nvsnap-overlays/models/"+modelvolume.Key("ngc://org/team/nemotron3-ultra-genrm:bf16-fixed") || *vol.HostPath.Type != corev1.HostPathDirectoryOrCreate {
		t.Errorf("Block reader lands on a hostPath under the model host root for the agent to bind into, got %+v", vol)
	}
	var propagations int
	for _, p := range patches {
		if strings.HasSuffix(p.Path, "/mountPropagation") && p.Value == corev1.MountPropagationHostToContainer {
			propagations++
		}
	}
	if propagations != 2 {
		t.Errorf("engine and download init mounts must propagate host mounts in, got %d", propagations)
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

func TestModelVolume_EngineDownload_JobRunsHF(t *testing.T) {
	kc := fake.NewSimpleClientset()
	m, _ := mvMutator(t, modelvolume.ModeRWX, election.RoleLeader, kc)
	pod := stockVLLMPod()
	patches, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	v := viewMV(pod, patches)
	uri := "hf://Qwen/Qwen2.5-32B-Instruct"
	job, err := kc.BatchV1().Jobs("fn").Get(context.Background(), modelvolume.JobName(uri), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("engine-download needs a download Job: %v", err)
	}
	jc := job.Spec.Template.Spec.Containers[0]
	s := jc.Args[0]
	if jc.Image != pod.Spec.Containers[0].Image || !strings.Contains(s, "hf download Qwen/Qwen2.5-32B-Instruct") || !strings.Contains(s, "touch /root/.cache/huggingface/.nvsnap-complete") || jc.VolumeMounts[0].MountPath != "/root/.cache/huggingface" {
		t.Errorf("job must run hf download on the engine image into HF_HOME: image=%s mounts=%v\n%s", jc.Image, jc.VolumeMounts, s)
	}
	var sawToken bool
	for _, e := range jc.Env {
		if e.Name == "HF_TOKEN" && e.ValueFrom != nil {
			sawToken = true
		}
	}
	if !sawToken {
		t.Error("registry credentials must be forwarded to the download Job")
	}
	if len(v.newInits) != 1 || v.newInits[0].Name != "nvsnap-model-download" || !strings.Contains(v.newInits[0].Args[0], "while [ ! -f") {
		t.Errorf("the pod gets a wait init, got %v", v.newInits)
	}
	if v.env["HF_HUB_OFFLINE"] != "1" {
		t.Error("engine must start offline and read the volume")
	}
	vol, ok := v.volumes[modelVolumeName]
	if !ok || vol.PersistentVolumeClaim == nil {
		t.Errorf("RWX mode: rootfs landing gets the claim volume: %+v", vol)
	}
}

func TestModelVolume_CompleteCreatesNoJob(t *testing.T) {
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
		t.Errorf("complete volume: reader role; labels=%v", v.labels)
	}
	if _, err := kc.BatchV1().Jobs("fn").Get(context.Background(), modelvolume.JobName(uri), metav1.GetOptions{}); err == nil {
		t.Error("a complete volume needs no download Job")
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

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package modelid

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func emptyDir(name string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
}

// The NVCF Helm function observed on prd11 (2026-09-25): NGC CLI download
// in an init container into an emptyDir, engine started from a positional
// path taken from MODEL_PATH, no --model anywhere.
func prd11Function() *corev1.Pod {
	return &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{
			Name: "download-ngc-model", Image: "nvcr.io/org/llm_nim/ultra:vllm",
			Command: []string{"/bin/bash", "-c"},
			Args:    []string{"set -euo pipefail\n/tmp/ngc-cli/ngc registry model download-version --dest \"${NGC_MODEL_MOUNT}\" \"${NGC_MODEL_NAME}\"\n"},
			Env: []corev1.EnvVar{
				{Name: "NGC_MODEL_NAME", Value: "qc69jvmznzxy/llm_nim/nemotron3-ultra-genrm:bf16-fixed"},
				{Name: "NGC_MODEL_MOUNT", Value: "/config/models"},
				{Name: "NGC_STABLE_MODEL_PATH", Value: "/config/models/nemotron3-ultra-genrm"},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "ngc-models", MountPath: "/config/models"}},
		}},
		Containers: []corev1.Container{{
			Name: "kimi-k3", Image: "nvcr.io/org/llm_nim/ultra:vllm",
			Command: []string{"/bin/bash", "/opt/kimi-k3/start.sh"},
			Env: []corev1.EnvVar{
				{Name: "MODEL_PATH", Value: "/config/models/nemotron3-ultra-genrm"},
				{Name: "HF_HUB_OFFLINE", Value: "1"},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "dshm", MountPath: "/dev/shm"}, {Name: "scripts", MountPath: "/opt/kimi-k3", ReadOnly: true}, {Name: "ngc-models", MountPath: "/config/models"}},
		}},
		Volumes: []corev1.Volume{emptyDir("dshm"), {Name: "scripts", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}}}, emptyDir("ngc-models")},
	}}
}

func TestResolve_NVCFFunction_NGCInitDownload(t *testing.T) {
	r, ok := Resolve(prd11Function(), 0)
	if !ok {
		t.Fatal("prd11 function must resolve")
	}
	if r.Identity.URI() != "ngc://qc69jvmznzxy/llm_nim/nemotron3-ultra-genrm:bf16-fixed" {
		t.Errorf("identity = %q", r.Identity.URI())
	}
	l := r.Landing
	if l.Downloader != DownloaderInit || l.InitContainer != "download-ngc-model" || l.Path != "/config/models" || l.VolumeName != "ngc-models" || l.Kind != VolumeEmptyDir || !l.Substitutable() {
		t.Errorf("landing = %+v", l)
	}
}

func TestResolve_EngineDownloads(t *testing.T) {
	cases := map[string]struct {
		c       corev1.Container
		vols    []corev1.Volume
		wantURI string
		wantSrc string
		path    string
		kind    VolumeKind
	}{
		"stock vllm, default HF_HOME on rootfs": {
			c:       corev1.Container{Image: "vllm/vllm-openai:v0.20.0", Command: []string{"/bin/bash", "-lc"}, Args: []string{"vllm serve --model Qwen/Qwen2.5-32B-Instruct --tensor-parallel-size 4 > /vllm.out 2>&1 &"}},
			wantURI: "hf://Qwen/Qwen2.5-32B-Instruct", wantSrc: "--model", path: "/root/.cache/huggingface", kind: VolumeRootfs,
		},
		"positional model with revision, HF_HOME on emptyDir": {
			c: corev1.Container{Image: "vllm/vllm-openai", Args: []string{"vllm serve meta-llama/Llama-3.1-70B-Instruct --revision abc123 --tensor-parallel-size 4"},
				Env: []corev1.EnvVar{{Name: "HF_HOME", Value: "/models/hf"}}, VolumeMounts: []corev1.VolumeMount{{Name: "hf", MountPath: "/models"}}},
			vols:    []corev1.Volume{emptyDir("hf")},
			wantURI: "hf://meta-llama/Llama-3.1-70B-Instruct@abc123", wantSrc: "vllm serve <model>", path: "/models/hf", kind: VolumeEmptyDir,
		},
		"dynamo list form": {
			c:       corev1.Container{Image: "nvcr.io/nvidia/ai-dynamo/vllm-runtime:1.1.1", Command: []string{"python3", "-m", "dynamo.vllm"}, Args: []string{"--model", "Qwen/Qwen3-0.6B", "--is-prefill-worker"}},
			wantURI: "hf://Qwen/Qwen3-0.6B", wantSrc: "--model", path: "/root/.cache/huggingface", kind: VolumeRootfs,
		},
		"sglang model-path": {
			c:       corev1.Container{Image: "lmsysorg/sglang", Args: []string{"python3 -m sglang.launch_server --model-path google/gemma-4-31B-it --tp 2"}},
			wantURI: "hf://google/gemma-4-31B-it", wantSrc: "--model", path: "/root/.cache/huggingface", kind: VolumeRootfs,
		},
		"customer already mounts a PVC at HF_HOME": {
			c:       corev1.Container{Image: "vllm/vllm-openai", Args: []string{"--model", "Qwen/Qwen3-0.6B"}, VolumeMounts: []corev1.VolumeMount{{Name: "cache", MountPath: "/root/.cache/huggingface"}}},
			vols:    []corev1.Volume{{Name: "cache", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "models"}}}},
			wantURI: "hf://Qwen/Qwen3-0.6B", wantSrc: "--model", path: "/root/.cache/huggingface", kind: VolumePVC,
		},
		"NIM image with profile": {
			c:       corev1.Container{Image: "nvcr.io/nim/meta/llama-3.1-8b-instruct:1.8.3", Env: []corev1.EnvVar{{Name: "NIM_CACHE_PATH", Value: "/opt/nim/.cache"}, {Name: "NIM_MODEL_PROFILE", Value: "tensorrt_llm-h100-fp8"}}},
			wantURI: "nim://nvcr.io/nim/meta/llama-3.1-8b-instruct:1.8.3@tensorrt_llm-h100-fp8", wantSrc: "nim image", path: "/opt/nim/.cache", kind: VolumeRootfs,
		},
		"HF_MODEL_ID env": {
			c:       corev1.Container{Image: "x", Command: []string{"/start.sh"}, Env: []corev1.EnvVar{{Name: "HF_MODEL_ID", Value: "openai/whisper-large-v3"}}},
			wantURI: "hf://openai/whisper-large-v3", wantSrc: "env HF_MODEL_ID", path: "/root/.cache/huggingface", kind: VolumeRootfs,
		},
	}
	for name, tc := range cases {
		pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{tc.c}, Volumes: tc.vols}}
		r, ok := Resolve(pod, 0)
		if !ok {
			t.Errorf("%s: must resolve", name)
			continue
		}
		if r.Identity.URI() != tc.wantURI || r.Source != tc.wantSrc {
			t.Errorf("%s: identity %q via %q, want %q via %q", name, r.Identity.URI(), r.Source, tc.wantURI, tc.wantSrc)
		}
		if r.Landing.Path != tc.path || r.Landing.Kind != tc.kind || r.Landing.Downloader != DownloaderEngine {
			t.Errorf("%s: landing %+v, want path %s kind %s", name, r.Landing, tc.path, tc.kind)
		}
		if tc.kind == VolumePVC && r.Landing.Substitutable() {
			t.Errorf("%s: a customer PVC must not be substitutable", name)
		}
	}
}

func TestResolve_OtherInitDownloaders(t *testing.T) {
	hf := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "fetch", Image: "python:3", Command: []string{"sh", "-c", "huggingface-cli download --revision v2 meta-llama/Llama-3.1-8B-Instruct --local-dir /models/llama"}, VolumeMounts: []corev1.VolumeMount{{Name: "m", MountPath: "/models"}}}},
		Containers:     []corev1.Container{{Name: "vllm", Image: "vllm/vllm-openai", Args: []string{"vllm serve /models/llama"}, VolumeMounts: []corev1.VolumeMount{{Name: "m", MountPath: "/models"}}}},
		Volumes:        []corev1.Volume{emptyDir("m")},
	}}
	r, ok := Resolve(hf, 0)
	if !ok || r.Identity.URI() != "hf://meta-llama/Llama-3.1-8B-Instruct@v2" || r.Landing.Path != "/models/llama" || r.Landing.VolumeName != "m" || r.Landing.InitContainer != "fetch" {
		t.Errorf("huggingface-cli init: ok=%v %+v", ok, r)
	}
	s3 := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "sync", Image: "amazon/aws-cli", Args: []string{"s3", "sync", "s3://my-bucket/models/llama-70b/", "/mnt/models"}, VolumeMounts: []corev1.VolumeMount{{Name: "m", MountPath: "/mnt/models"}}}},
		Containers:     []corev1.Container{{Name: "vllm", Image: "vllm/vllm-openai", Env: []corev1.EnvVar{{Name: "MODEL_PATH", Value: "/mnt/models"}}, Command: []string{"sh", "-c", "vllm serve $MODEL_PATH"}, VolumeMounts: []corev1.VolumeMount{{Name: "m", MountPath: "/mnt/models"}}}},
		Volumes:        []corev1.Volume{emptyDir("m")},
	}}
	if r, ok := Resolve(s3, 0); !ok || r.Identity.URI() != "s3://my-bucket/models/llama-70b" || r.Landing.Path != "/mnt/models" {
		t.Errorf("s3 init: ok=%v %+v", ok, r)
	}
	kserve := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "storage-initializer", Image: "kserve/storage-initializer:v0.15", Args: []string{"hf://Qwen/Qwen3-0.6B", "/mnt/models"}, VolumeMounts: []corev1.VolumeMount{{Name: "kserve-provision-location", MountPath: "/mnt/models"}}}},
		Containers:     []corev1.Container{{Name: "kserve-container", Image: "kserve/vllm", Args: []string{"--model_dir=/mnt/models"}, VolumeMounts: []corev1.VolumeMount{{Name: "kserve-provision-location", MountPath: "/mnt/models", ReadOnly: true}}}},
		Volumes:        []corev1.Volume{emptyDir("kserve-provision-location")},
	}}
	if r, ok := Resolve(kserve, 0); !ok || r.Identity.URI() != "hf://Qwen/Qwen3-0.6B" || r.Landing.Path != "/mnt/models" || r.Landing.Kind != VolumeEmptyDir {
		t.Errorf("kserve hf: ok=%v %+v", ok, r)
	}
	kserve.Spec.InitContainers[0].Args[0] = "pvc://models/qwen"
	if _, ok := Resolve(kserve, 0); ok {
		t.Error("kserve pvc:// means the customer shares already; must not resolve as a downloader")
	}
}

// URIs carry the revision after '@'; engine argv expands the container's
// own env so `vllm serve ${MODEL_PATH}` resolves to the path, not to the
// literal.
func TestResolve_URIRevisionAndEnvExpansion(t *testing.T) {
	id, ok := parseURI("hf://Qwen/Qwen3-0.6B@v1")
	if !ok || id.Ref != "Qwen/Qwen3-0.6B" || id.Revision != "v1" || id.URI() != "hf://Qwen/Qwen3-0.6B@v1" {
		t.Errorf("parseURI revision: ok=%v %+v", ok, id)
	}
	pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Image: "vllm/vllm-openai", Command: []string{"sh", "-c", "exec vllm serve ${MODEL_PATH} --port 8000"},
		Env: []corev1.EnvVar{{Name: "MODEL_PATH", Value: "/models/llama"}},
	}}}}
	r, ok := Resolve(pod, 0)
	if !ok || r.Identity.URI() != "path:///models/llama" {
		t.Errorf("env-expanded positional model: ok=%v %q", ok, r.Identity.URI())
	}
}

// An init's own env is expanded before the argv is read, so `--dest
// ${DEST}` yields the real path when no NGC_MODEL_MOUNT is set.
func TestResolve_InitArgvExpandsOwnEnv(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "dl", Image: "x", Command: []string{"sh", "-c", "ngc registry model download-version --dest ${DEST} org/team/model:1"},
			Env: []corev1.EnvVar{{Name: "DEST", Value: "/data/models"}}, VolumeMounts: []corev1.VolumeMount{{Name: "m", MountPath: "/data"}}}},
		Containers: []corev1.Container{{Name: "engine", Image: "x", Command: []string{"/start.sh"}, VolumeMounts: []corev1.VolumeMount{{Name: "m", MountPath: "/data"}}}},
		Volumes:    []corev1.Volume{emptyDir("m")},
	}}
	r, ok := Resolve(pod, 0)
	if !ok || r.Identity.URI() != "ngc://org/team/model:1" || r.Landing.Path != "/data/models" || r.Landing.VolumeName != "m" {
		t.Errorf("ok=%v %+v", ok, r)
	}
}

func TestResolve_NotADownloader(t *testing.T) {
	for name, c := range map[string]corev1.Container{
		"dynamo frontend": {Command: []string{"python3", "-m", "dynamo.frontend"}, Args: []string{"--router-mode", "kv"}},
		"ray worker":      {Image: "vllm/vllm-openai", Command: []string{"sh", "-c", "ray start --address=$(LWS_LEADER_ADDRESS):6379 --block"}},
		"etcd":            {Image: "quay.io/coreos/etcd", Command: []string{"etcd"}},
	} {
		if _, ok := Resolve(&corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}, 0); ok {
			t.Errorf("%s must not resolve", name)
		}
	}
}

// LWS worker: no model of its own; inherits from the leader template.
func TestResolveWithGroup_LWSWorkerInheritsLeader(t *testing.T) {
	leaderTmpl := map[string]any{
		"spec": map[string]any{"containers": []any{map[string]any{
			"name": "vllm-leader", "image": "vllm/vllm-openai",
			"command": []any{"sh", "-c", "bash multi-node-serving.sh leader --ray_cluster_size=$(LWS_GROUP_SIZE); python3 -m vllm.entrypoints.openai.api_server --port 8080 --model meta-llama/Meta-Llama-3.1-405B-Instruct --tensor-parallel-size 8 --pipeline_parallel_size 2"},
		}}},
	}
	lws := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "leaderworkerset.x-k8s.io/v1", "kind": "LeaderWorkerSet",
		"metadata": map[string]any{"name": "vllm", "namespace": "fn"},
		"spec":     map[string]any{"leaderWorkerTemplate": map[string]any{"leaderTemplate": leaderTmpl}},
	}}
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{lwsGVR: "LeaderWorkerSetList"}, lws)
	worker := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "fn", Labels: map[string]string{lwsNameLabel: "vllm", lwsWorkerIndexLabel: "1"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm-worker", Image: "vllm/vllm-openai",
			Command:      []string{"sh", "-c", "bash multi-node-serving.sh worker --ray_address=$(LWS_LEADER_ADDRESS)"},
			VolumeMounts: []corev1.VolumeMount{{Name: "hf", MountPath: "/root/.cache/huggingface"}}}},
			Volumes: []corev1.Volume{emptyDir("hf")}},
	}
	r, ok, err := ResolveWithGroup(context.Background(), worker, 0, &LWSResolver{Dyn: dyn})
	if err != nil || !ok {
		t.Fatalf("worker must inherit: ok=%v err=%v", ok, err)
	}
	if r.Identity.URI() != "hf://meta-llama/Meta-Llama-3.1-405B-Instruct" || r.Landing.VolumeName != "hf" || r.Landing.Kind != VolumeEmptyDir {
		t.Errorf("inherited %+v", r)
	}
	// The leader itself resolves on its own and never consults the group.
	leader := worker.DeepCopy()
	leader.Labels[lwsWorkerIndexLabel] = "0"
	leader.Spec.Containers[0].Command = []string{"sh", "-c", "vllm serve --model meta-llama/Meta-Llama-3.1-405B-Instruct"}
	if r, ok, _ := ResolveWithGroup(context.Background(), leader, 0, &LWSResolver{Dyn: dynamicfake.NewSimpleDynamicClient(scheme)}); !ok || r.Source != "--model" {
		t.Errorf("leader must resolve itself: ok=%v src=%q", ok, r.Source)
	}
	// A worker of an unknown group fails open (not a downloader).
	unknown := worker.DeepCopy()
	unknown.Labels[lwsNameLabel] = "missing"
	if _, ok, err := ResolveWithGroup(context.Background(), unknown, 0, &LWSResolver{Dyn: dyn}); ok || err == nil {
		t.Errorf("missing group: ok=%v err=%v, want not ok with error", ok, err)
	}
}

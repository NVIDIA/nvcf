/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rootfsonly

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

func TestCompose_Basic(t *testing.T) {
	c := HashInputComposer{CUDADriverMajor: 580}
	p := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "vllm/vllm-openai:v0.11.2",
				Args:  []string{"vllm serve --model meta-llama/Llama-3.1-8B-Instruct --tensor-parallel-size 2"},
				Env: []corev1.EnvVar{
					{Name: "HF_HOME", Value: "/root/.cache/huggingface"},
					{Name: "NVSNAP_LOG_LEVEL", Value: "3"},                  // skipped
					{Name: "LD_PRELOAD", Value: "/nvsnap-lib/libnvsnap.so"}, // skipped
				},
			}},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:    "main",
				ImageID: "docker.io/vllm/vllm-openai@sha256:abc",
			}},
		},
	}
	in := c.Compose(p, 0)

	if in.CUDADriverMajor != 580 {
		t.Errorf("CUDADriverMajor = %d, want 580", in.CUDADriverMajor)
	}
	if in.CaptureFormatVersion != checkpointstore.CaptureFormatVersion {
		t.Errorf("CaptureFormatVersion = %d, want %d", in.CaptureFormatVersion, checkpointstore.CaptureFormatVersion)
	}
	if in.ImageDigest != "docker.io/vllm/vllm-openai@sha256:abc" {
		t.Errorf("ImageDigest = %q, want resolved digest", in.ImageDigest)
	}
	if in.ModelID != "meta-llama/Llama-3.1-8B-Instruct" {
		t.Errorf("ModelID = %q, want %q", in.ModelID, "meta-llama/Llama-3.1-8B-Instruct")
	}

	// HF_HOME should appear; NVSNAP_* and LD_PRELOAD should NOT.
	flags := strings.Join(in.EngineCompatFlags, "|")
	if !strings.Contains(flags, "env:HF_HOME=/root/.cache/huggingface") {
		t.Errorf("missing HF_HOME flag: %v", in.EngineCompatFlags)
	}
	if strings.Contains(flags, "NVSNAP_LOG_LEVEL") {
		t.Errorf("NVSNAP_LOG_LEVEL should be excluded: %v", in.EngineCompatFlags)
	}
	if strings.Contains(flags, "LD_PRELOAD") {
		t.Errorf("LD_PRELOAD should be excluded: %v", in.EngineCompatFlags)
	}

	// args should be included as-is.
	if !strings.Contains(flags, "arg[0]:vllm serve") {
		t.Errorf("args not included: %v", in.EngineCompatFlags)
	}
}

func TestCompose_FallbackToSpecImageWhenStatusEmpty(t *testing.T) {
	c := HashInputComposer{}
	p := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "img:tag"}},
		},
	}
	in := c.Compose(p, 0)
	if in.ImageDigest != "img:tag" {
		t.Errorf("ImageDigest = %q, want fallback to spec image", in.ImageDigest)
	}
}

func TestCompose_NIMModelIDFromImage(t *testing.T) {
	c := HashInputComposer{}
	p := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "nvcr.io/nim/meta/llama-3.3-70b-instruct:1.15.5",
			}},
		},
	}
	in := c.Compose(p, 0)
	if in.ModelID != "nvcr.io/nim/meta/llama-3.3-70b-instruct:1.15.5" {
		t.Errorf("NIM ModelID should be the image name; got %q", in.ModelID)
	}
}

func TestCompose_ModelEqualsForm(t *testing.T) {
	c := HashInputComposer{}
	p := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "vllm/vllm-openai:v0.11.2",
				Args:  []string{"vllm serve --model=meta-llama/Llama-3.1-70B-Instruct"},
			}},
		},
	}
	in := c.Compose(p, 0)
	if in.ModelID != "meta-llama/Llama-3.1-70B-Instruct" {
		t.Errorf("--model=foo form not parsed; got %q", in.ModelID)
	}
}

func TestCompose_ModelPathForm(t *testing.T) {
	c := HashInputComposer{}
	p := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "stg.nvcr.io/zq9tgrjzrfpo/sglang:v0.5.9",
				Args:  []string{"python3 -m sglang.launch_server --model-path meta-llama/Llama-3.1-8B-Instruct --tp-size 2"},
			}},
		},
	}
	in := c.Compose(p, 0)
	if in.ModelID != "meta-llama/Llama-3.1-8B-Instruct" {
		t.Errorf("--model-path form not parsed; got %q", in.ModelID)
	}
}

func TestCompose_NilPodAndOutOfRange(t *testing.T) {
	c := HashInputComposer{CUDADriverMajor: 580}
	got := c.Compose(nil, 0)
	if got.CUDADriverMajor != 580 || got.CaptureFormatVersion != checkpointstore.CaptureFormatVersion {
		t.Errorf("nil pod: lost driver/format-version: %+v", got)
	}
	if got.ImageDigest != "" || got.ModelID != "" || len(got.EngineCompatFlags) > 0 {
		t.Errorf("nil pod: should produce empty fields: %+v", got)
	}

	out := c.Compose(&corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{}}}}, 5)
	if out.ImageDigest != "" {
		t.Errorf("out-of-range main: should produce empty fields: %+v", out)
	}
}

func TestCompose_DistinguishesByConfig(t *testing.T) {
	// Two pods with same image but different --tensor-parallel-size MUST
	// produce different hashes. This is the production-critical
	// invariant: a TP=2 cache is incompatible with a TP=4 pod.
	c := HashInputComposer{CUDADriverMajor: 580}
	mk := func(args string) *corev1.Pod {
		return &corev1.Pod{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Image: "vllm/vllm-openai:v0.11.2",
				Args:  []string{args},
			}}},
		}
	}
	a := c.Compose(mk("vllm serve --model m --tensor-parallel-size 2"), 0)
	b := c.Compose(mk("vllm serve --model m --tensor-parallel-size 4"), 0)
	hashA := checkpointstore.ComputeHash(a)
	hashB := checkpointstore.ComputeHash(b)
	if hashA == hashB {
		t.Fatalf("TP=2 and TP=4 must hash differently; both = %s", hashA)
	}
}

func TestCompose_DistinguishesByDriverMajor(t *testing.T) {
	mk := func() *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Image: "vllm/vllm-openai:v0.11.2",
			Args:  []string{"vllm serve --model m --tp 2"},
		}}}}
	}
	a := (&HashInputComposer{CUDADriverMajor: 580}).Compose(mk(), 0)
	b := (&HashInputComposer{CUDADriverMajor: 555}).Compose(mk(), 0)
	if checkpointstore.ComputeHash(a) == checkpointstore.ComputeHash(b) {
		t.Fatalf("driver major mismatch must hash differently")
	}
}

func TestCompose_EnvFromValueRefRecordedByName(t *testing.T) {
	c := HashInputComposer{}
	p := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Image: "img",
			Env: []corev1.EnvVar{
				{Name: "HF_TOKEN", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "hf-token"},
						Key:                  "token",
					},
				}},
			},
		}}},
	}
	in := c.Compose(p, 0)
	flags := strings.Join(in.EngineCompatFlags, "|")
	if !strings.Contains(flags, "env:HF_TOKEN=<from-ref>") {
		t.Errorf("ValueFrom env should be recorded by name; got %v", in.EngineCompatFlags)
	}
}

func TestCompose_EnvSortedDeterministically(t *testing.T) {
	c := HashInputComposer{}
	mk := func(envs []corev1.EnvVar) *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Image: "img",
			Env:   envs,
		}}}}
	}
	a := c.Compose(mk([]corev1.EnvVar{{Name: "Z", Value: "1"}, {Name: "A", Value: "2"}}), 0)
	b := c.Compose(mk([]corev1.EnvVar{{Name: "A", Value: "2"}, {Name: "Z", Value: "1"}}), 0)
	if checkpointstore.ComputeHash(a) != checkpointstore.ComputeHash(b) {
		t.Fatalf("env-order shouldn't change hash: a.flags=%v b.flags=%v",
			a.EngineCompatFlags, b.EngineCompatFlags)
	}
}

func TestCompose_CommandIncluded(t *testing.T) {
	c := HashInputComposer{}
	p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Image:   "img",
		Command: []string{"/bin/bash", "-lc"},
		Args:    []string{"echo hi"},
	}}}}
	in := c.Compose(p, 0)
	flags := strings.Join(in.EngineCompatFlags, "|")
	if !strings.Contains(flags, "cmd[0]:/bin/bash") || !strings.Contains(flags, "cmd[1]:-lc") {
		t.Fatalf("Command not included: %v", in.EngineCompatFlags)
	}
}

// The model identity is what makes a pod a downloader, and charts spell it
// several ways: Dynamo lists "--model" and the value as separate items,
// stock charts wrap "vllm serve --model X" in one bash string, NIMs use
// env or the image. Frontend pods have none of them.
func TestInferModelID_Forms(t *testing.T) {
	cases := map[string]struct {
		c    corev1.Container
		want string
	}{
		"dynamo list form": {corev1.Container{
			Command: []string{"python3", "-m", "dynamo.vllm"},
			Args:    []string{"--model", "Qwen/Qwen3-0.6B", "--is-decode-worker"},
		}, "Qwen/Qwen3-0.6B"},
		"sglang list form": {corev1.Container{
			Command: []string{"python3", "-m", "dynamo.sglang"},
			Args:    []string{"--model-path", "google/gemma-4-31B-it", "--tp", "2"},
		}, "google/gemma-4-31B-it"},
		"equals form": {corev1.Container{Args: []string{"--model=meta-llama/Llama-3.1-8B-Instruct"}}, "meta-llama/Llama-3.1-8B-Instruct"},
		"bash wrapper": {corev1.Container{
			Command: []string{"/bin/bash", "-lc"},
			Args:    []string{"set -e\nnohup setsid vllm serve --model TinyLlama/TinyLlama-1.1B-Chat-v1.0 --tensor-parallel-size 2 &\nwhile true; do sleep 30; done"},
		}, "TinyLlama/TinyLlama-1.1B-Chat-v1.0"},
		"env HF_MODEL_ID": {corev1.Container{Env: []corev1.EnvVar{{Name: "HF_MODEL_ID", Value: "openai/whisper-large-v3"}}}, "openai/whisper-large-v3"},
		"nim image":       {corev1.Container{Image: "nvcr.io/nim/meta/llama-3.1-8b-instruct:1.8.3"}, "nvcr.io/nim/meta/llama-3.1-8b-instruct:1.8.3"},
		"dangling flag":   {corev1.Container{Args: []string{"--model"}}, ""},
		"flag then flag":  {corev1.Container{Args: []string{"--model", "--port"}}, ""},
		"dynamo frontend": {corev1.Container{Command: []string{"python3", "-m", "dynamo.frontend"}, Args: []string{"--router-mode", "kv"}}, ""},
	}
	for name, tc := range cases {
		if got := InferModelID(tc.c); got != tc.want {
			t.Errorf("%s: InferModelID = %q, want %q", name, got, tc.want)
		}
	}
}

func TestIsModelWorkload(t *testing.T) {
	gpu := corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")}}
	worker := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Command: []string{"python3", "-m", "dynamo.vllm"}, Args: []string{"--model", "Qwen/Qwen3-0.6B", "--is-prefill-worker"}, Resources: gpu,
	}}}}
	frontend := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Command: []string{"python3", "-m", "dynamo.frontend"},
	}}}}
	gpuNoModel := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Command: []string{"python3", "train.py"}, Resources: gpu,
	}}}}
	modelNoGPU := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Args: []string{"--model", "Qwen/Qwen3-0.6B"},
	}}}}
	if id, ok := IsModelWorkload(worker, 0); !ok || id != "Qwen/Qwen3-0.6B" {
		t.Errorf("dynamo worker: (%q,%v), want downloader", id, ok)
	}
	for name, p := range map[string]*corev1.Pod{"frontend": frontend, "gpu without model": gpuNoModel, "model without gpu": modelNoGPU} {
		if _, ok := IsModelWorkload(p, 0); ok {
			t.Errorf("%s must not be classified as a downloader", name)
		}
	}
	if _, ok := IsModelWorkload(worker, 3); ok {
		t.Error("out-of-range main container must not classify")
	}
}

// Prefill and decode workers of one disaggregated deployment must share a
// hash: same image, same model, same download. Only the role flags and the
// Dynamo/etcd/NATS wiring differ, and none of that changes the cache tree.
func TestCompose_RoleNeutralAcrossPrefillAndDecode(t *testing.T) {
	c := &HashInputComposer{CUDADriverMajor: 580}
	hashOf := func(cmd, args []string, env ...corev1.EnvVar) string {
		p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "main", Image: "vllm/vllm-openai:v0.20.0", Command: cmd, Args: args, Env: env,
		}}}}
		return checkpointstore.ComputeHash(c.Compose(p, 0))
	}
	dyn := []string{"python3", "-m", "dynamo.vllm"}
	prefill := hashOf(dyn, []string{"--model", "Qwen/Qwen3-0.6B", "--is-prefill-worker"},
		corev1.EnvVar{Name: "DYN_NAMESPACE", Value: "dgd-a"}, corev1.EnvVar{Name: "ETCD_ENDPOINTS", Value: "etcd-a:2379"})
	decode := hashOf(dyn, []string{"--model", "Qwen/Qwen3-0.6B", "--is-decode-worker"},
		corev1.EnvVar{Name: "DYN_NAMESPACE", Value: "dgd-b"}, corev1.EnvVar{Name: "NATS_SERVER", Value: "nats://x:4222"})
	if prefill != decode {
		t.Error("dynamo.vllm prefill and decode workers must hash the same")
	}
	sgl := []string{"python3", "-m", "dynamo.sglang"}
	sp := hashOf(sgl, []string{"--model-path", "google/gemma-4-31B-it", "--disaggregation-mode", "prefill", "--disaggregation-bootstrap-port", "8998"})
	sd := hashOf(sgl, []string{"--model-path", "google/gemma-4-31B-it", "--disaggregation-mode=decode", "--disaggregation-transfer-backend", "nixl"})
	if sp != sd {
		t.Error("dynamo.sglang prefill and decode workers must hash the same")
	}
	bash := []string{"/bin/bash", "-lc"}
	bp := hashOf(bash, []string{`vllm serve --model Qwen/Qwen3-0.6B --is-prefill-worker --kv-transfer-config '{"kv_connector":"NixlConnector","kv_role":"kv_producer"}' > /out 2>&1`})
	bd := hashOf(bash, []string{`vllm serve --model Qwen/Qwen3-0.6B --is-decode-worker --kv-transfer-config '{"kv_connector":"NixlConnector","kv_role":"kv_consumer"}' > /out 2>&1`})
	if bp != bd {
		t.Error("shell-string prefill and decode workers must hash the same")
	}
	// What changes the download or the engine still separates hashes.
	if hashOf(dyn, []string{"--model", "Qwen/Qwen3-0.6B", "--is-prefill-worker"}) == hashOf(dyn, []string{"--model", "Qwen/Qwen3-1.7B", "--is-prefill-worker"}) {
		t.Error("different models must hash differently")
	}
	if hashOf(dyn, []string{"--model", "Qwen/Qwen3-0.6B", "--revision", "abc"}) == hashOf(dyn, []string{"--model", "Qwen/Qwen3-0.6B", "--revision", "def"}) {
		t.Error("different revisions download different files and must hash differently")
	}
	if hashOf(dyn, []string{"--model", "Qwen/Qwen3-0.6B"}, corev1.EnvVar{Name: "HF_HUB_OFFLINE", Value: "1"}) == hashOf(dyn, []string{"--model", "Qwen/Qwen3-0.6B"}) {
		t.Error("cache-relevant env must still participate in the hash")
	}
}

func TestStripRoleFlags(t *testing.T) {
	got := stripRoleFlags([]string{"--model", "m", "--is-decode-worker", "--disaggregation-mode", "decode", "--disaggregation-strategy=prefill_first", "--tp", "2", "--kv-transfer-config"})
	want := []string{"--model", "m", "--tp", "2"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("stripRoleFlags = %v, want %v", got, want)
	}
	// A dangling value-flag at the end must not eat a following flag.
	got = stripRoleFlags([]string{"--disaggregation-mode", "--port", "8000"})
	if strings.Join(got, " ") != "--port 8000" {
		t.Errorf("value flag followed by a flag: %v", got)
	}
}

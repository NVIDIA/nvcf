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

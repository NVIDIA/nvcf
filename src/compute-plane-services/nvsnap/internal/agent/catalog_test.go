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

package agent

import (
	"context"
	"reflect"
	"regexp"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

func TestExtractModelID(t *testing.T) {
	cases := []struct {
		name string
		c    corev1.Container
		want string
	}{
		{
			name: "NIM env var wins",
			c: corev1.Container{
				Env: []corev1.EnvVar{
					{Name: "OTHER", Value: "ignored"},
					{Name: "NIM_MODEL_NAME", Value: "meta/llama-3.1-8b-instruct"},
				},
			},
			want: "meta/llama-3.1-8b-instruct",
		},
		{
			name: "HF_MODEL_ID env var",
			c: corev1.Container{
				Env: []corev1.EnvVar{{Name: "HF_MODEL_ID", Value: "TinyLlama/TinyLlama-1.1B-Chat-v1.0"}},
			},
			want: "TinyLlama/TinyLlama-1.1B-Chat-v1.0",
		},
		{
			name: "vLLM --model in args (separate token)",
			c: corev1.Container{
				Args: []string{"--port", "8000", "--model", "meta-llama/Llama-3.1-8B", "--tp", "1"},
			},
			want: "meta-llama/Llama-3.1-8B",
		},
		{
			name: "--model=value form",
			c: corev1.Container{
				Args: []string{"--model=foo/bar", "--port=8000"},
			},
			want: "foo/bar",
		},
		{
			name: "--model-path",
			c: corev1.Container{
				Args: []string{"--model-path", "/models/local"},
			},
			want: "/models/local",
		},
		{
			name: "env beats args",
			c: corev1.Container{
				Env:  []corev1.EnvVar{{Name: "MODEL_NAME", Value: "from-env"}},
				Args: []string{"--model", "from-args"},
			},
			want: "from-env",
		},
		{
			name: "empty env value falls through",
			c: corev1.Container{
				Env:  []corev1.EnvVar{{Name: "NIM_MODEL_NAME", Value: ""}},
				Args: []string{"--model", "from-args"},
			},
			want: "from-args",
		},
		{
			name: "--model with no value",
			c: corev1.Container{
				Args: []string{"--model"},
			},
			want: "",
		},
		{
			name: "no model anywhere",
			c:    corev1.Container{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractModelID(&tc.c)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCanonicalArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "strips --model token + sorts",
			in:   []string{"--port", "8000", "--model", "foo/bar", "--tp", "1"},
			want: []string{"--port", "--tp", "1", "8000"},
		},
		{
			name: "strips --model= form",
			in:   []string{"--model=foo/bar", "--port=8000"},
			want: []string{"--port=8000"},
		},
		{
			name: "strips --model-path",
			in:   []string{"--model-path", "/m", "--port", "8000"},
			want: []string{"--port", "8000"},
		},
		{
			name: "order-stable: same flags different order → same canonical",
			in:   []string{"--b", "--a"},
			want: []string{"--a", "--b"},
		},
		{
			name: "empty in → nil out",
			in:   nil,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalArgs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGPURequestCount(t *testing.T) {
	mustQ := resource.MustParse
	cases := []struct {
		name string
		c    corev1.Container
		want int
	}{
		{
			name: "requests has gpu",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{"nvidia.com/gpu": mustQ("4")},
				},
			},
			want: 4,
		},
		{
			name: "falls back to limits",
			c: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{"nvidia.com/gpu": mustQ("8")},
				},
			},
			want: 8,
		},
		{
			name: "neither → 0",
			c:    corev1.Container{},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gpuRequestCount(&tc.c)
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDigestFromImageID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"docker-pullable://nvcr.io/foo@sha256:abc", "sha256:abc"},
		{"nvcr.io/foo@sha256:abc", "sha256:abc"},
		{"sha256:abc", "sha256:abc"},
		{"", ""},
	}
	for _, tc := range cases {
		got := digestFromImageID(tc.in)
		if got != tc.want {
			t.Errorf("digestFromImageID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestComputeHash_Determinism(t *testing.T) {
	a := CatalogInfo{
		ImageDigest:   "sha256:abc",
		ModelID:       "meta/llama",
		EngineFlags:   []string{"--port", "8000"},
		DriverVersion: "550.90.07",
	}
	// Same logical input, different flag order, missing irrelevant
	// fields — must hash equal because canonical hash inputs match.
	b := CatalogInfo{
		ImageDigest:   "sha256:abc",
		ModelID:       "meta/llama",
		EngineFlags:   []string{"8000", "--port"},
		DriverVersion: "550.91.99", // same major
		GPUType:       "ignored-by-hash",
	}
	if computeHash(a) != computeHash(b) {
		t.Errorf("expected equal hashes; a=%s b=%s", computeHash(a), computeHash(b))
	}
}

func TestComputeHash_DriverMajorParse(t *testing.T) {
	cases := []struct {
		drv  string
		want int
	}{
		{"550.90.07", 550},
		{"535", 535},
		{"", 0},
		{"notanumber", 0},
	}
	for _, tc := range cases {
		got := computeHash(CatalogInfo{DriverVersion: tc.drv})
		want := checkpointstore.ComputeHash(checkpointstore.HashInput{
			CUDADriverMajor:      tc.want,
			CaptureFormatVersion: catalogFormatVersion,
		})
		if got != want {
			t.Errorf("driver=%q produced wrong hash; got %s want %s", tc.drv, got, want)
		}
	}
}

func TestCollectCatalogInfo_NilKubeClient(t *testing.T) {
	a := &Agent{}
	info := a.CollectCatalogInfo(context.Background(), "ns", "pod", "inference", "node-1", "cluster-x")
	if info.Hash == "" || info.ShortHash == "" {
		t.Errorf("Hash/ShortHash should always populate; got %+v", info)
	}
	if info.CapturedOnNode != "node-1" || info.ClusterName != "cluster-x" {
		t.Errorf("node/cluster not carried through: %+v", info)
	}
}

func TestCollectCatalogInfo_HappyPath(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "infer-7",
			Namespace: "nvcf-backend",
			Annotations: map[string]string{
				"function-name": "fastapi-echo-sample",
			},
			Labels: map[string]string{
				"function-version-id":     "v123",
				"nvsnap.io/source-engine": "vllm",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "inference",
					Image: "nvcr.io/foo:latest",
					Env:   []corev1.EnvVar{{Name: "NIM_MODEL_NAME", Value: "meta/llama"}},
					Args:  []string{"--port", "8000"},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "inference", ImageID: "docker-pullable://nvcr.io/foo@sha256:deadbeef"},
			},
		},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
			Labels: map[string]string{
				"nvidia.com/gpu.product":               "NVIDIA-H100-80GB-HBM3",
				"nvidia.com/cuda.driver-version.full":  "550.90.07",
				"nvidia.com/cuda.runtime-version.full": "12.4",
				"kubernetes.io/arch":                   "amd64",
			},
		},
	}
	a := &Agent{kubeClient: fake.NewSimpleClientset(pod, node)}
	info := a.CollectCatalogInfo(context.Background(), "nvcf-backend", "infer-7", "inference", "node-1", "cluster-x")

	checks := []struct {
		field, got, want string
	}{
		{"FunctionName", info.FunctionName, "fastapi-echo-sample"},
		{"FunctionVersionID", info.FunctionVersionID, "v123"},
		{"Engine", info.Engine, "vllm"},
		{"ImageRef", info.ImageRef, "nvcr.io/foo:latest"},
		{"ImageDigest", info.ImageDigest, "sha256:deadbeef"},
		{"ModelID", info.ModelID, "meta/llama"},
		{"GPUType", info.GPUType, "NVIDIA-H100-80GB-HBM3"},
		{"DriverVersion", info.DriverVersion, "550.90.07"},
		{"CUDAVersion", info.CUDAVersion, "12.4"},
		{"CPUArchitecture", info.CPUArchitecture, "amd64"},
		{"ClusterName", info.ClusterName, "cluster-x"},
		{"CapturedOnNode", info.CapturedOnNode, "node-1"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.field, c.got, c.want)
		}
	}
	if info.GPUCount != 1 {
		t.Errorf("GPUCount: got %d, want 1", info.GPUCount)
	}
	if len(info.EngineFlags) != 2 || info.EngineFlags[0] != "--port" || info.EngineFlags[1] != "8000" {
		t.Errorf("EngineFlags wrong: %v", info.EngineFlags)
	}
	if info.Hash == "" || info.ShortHash == "" {
		t.Errorf("Hash/ShortHash not populated")
	}
}

func TestCollectCatalogInfo_PodNotFound(t *testing.T) {
	a := &Agent{kubeClient: fake.NewSimpleClientset()}
	info := a.CollectCatalogInfo(context.Background(), "ns", "missing", "inference", "", "")
	// All best-effort fields stay empty, but Hash is always set.
	if info.Hash == "" {
		t.Errorf("Hash should populate even when pod lookup fails")
	}
	if info.FunctionName != "" || info.ImageRef != "" {
		t.Errorf("non-empty source/image despite missing pod: %+v", info)
	}
}

// nvsnap#58: the on-disk + API checkpoint id used to be
// "<podName>__<namespace>__<timestamp>" — pod-name leaked into the
// catalog, which made content-addressed lookup impossible. The new
// format is "<shortHash>__<timestamp>": same content always sorts
// next to itself, and the id never names a specific pod-instance.
func TestBuildCheckpointID(t *testing.T) {
	ts := time.Date(2026, 5, 31, 17, 46, 4, 0, time.UTC)

	cases := []struct {
		name    string
		catalog CatalogInfo
		wantRe  string
	}{
		{
			name: "shortHash + timestamp",
			catalog: CatalogInfo{
				ShortHash: "85ec4d75ee57c1be444dd19733f63cfd",
			},
			wantRe: `^85ec4d75ee57c1be444dd19733f63cfd__20260531-174604$`,
		},
		{
			name:    "no shortHash (degraded) still produces a parseable id",
			catalog: CatalogInfo{},
			wantRe:  `^__20260531-174604$`,
		},
		{
			name: "no pod name leaks into id",
			catalog: CatalogInfo{
				ShortHash: "deadbeefdeadbeefdeadbeefdeadbeef",
			},
			// Negative assertion: must NOT contain any ephemeral
			// identifier. Just match the format strictly.
			wantRe: `^[a-f0-9]{32}__\d{8}-\d{6}$`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildCheckpointID(tc.catalog, ts)
			if matched, _ := regexp.MatchString(tc.wantRe, got); !matched {
				t.Errorf("id = %q, want regex %q", got, tc.wantRe)
			}
		})
	}
}

// Sanity: two captures with the same canonical content (same hash) at
// different times produce different IDs (timestamp suffix differs),
// but the IDs sort lexicographically next to each other — operators
// can find all captures of one logical workload by hash-prefix.
func TestBuildCheckpointID_TimestampOrdering(t *testing.T) {
	cat := CatalogInfo{ShortHash: "85ec4d75ee57c1be444dd19733f63cfd"}
	first := buildCheckpointID(cat, time.Date(2026, 5, 31, 17, 46, 4, 0, time.UTC))
	later := buildCheckpointID(cat, time.Date(2026, 5, 31, 17, 47, 19, 0, time.UTC))
	if first == later {
		t.Fatalf("ids should differ for different timestamps: %q == %q", first, later)
	}
	if first >= later {
		t.Errorf("lexicographic order should match chronological: %q !< %q", first, later)
	}
}

func TestCollectCatalogInfo_GKELabelFallbacks(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
			Labels: map[string]string{
				// Only GKE labels, not GPU-operator ones.
				"cloud.google.com/gke-accelerator": "nvidia-h100-mega-80gb",
				"nvidia.com/cuda.driver.major":     "550",
				"nvidia.com/cuda.driver.minor":     "90",
				"nvidia.com/cuda.driver.rev":       "07",
			},
		},
	}
	a := &Agent{kubeClient: fake.NewSimpleClientset(node)}
	info := a.CollectCatalogInfo(context.Background(), "ns", "x", "inference", "node-1", "")
	if info.GPUType != "nvidia-h100-mega-80gb" {
		t.Errorf("GPUType fallback: got %q", info.GPUType)
	}
	if info.DriverVersion != "550.90.07" {
		t.Errorf("DriverVersion composed: got %q", info.DriverVersion)
	}
}

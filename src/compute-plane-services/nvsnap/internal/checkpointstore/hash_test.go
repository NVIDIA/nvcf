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

package checkpointstore

import (
	"strings"
	"testing"
)

func TestComputeHash_Deterministic(t *testing.T) {
	in := HashInput{
		ImageDigest:          "sha256:deadbeef",
		ModelID:              "meta-llama/Llama-3.1-8B-Instruct",
		EngineCompatFlags:    []string{"--tensor-parallel-size=2", "--dtype=bfloat16"},
		CUDADriverMajor:      580,
		CaptureFormatVersion: 1,
	}
	h1 := ComputeHash(in)
	h2 := ComputeHash(in)
	if h1 != h2 {
		t.Fatalf("ComputeHash not deterministic: %s != %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("hash should be 64 hex chars (sha256), got %d: %q", len(h1), h1)
	}
}

func TestComputeHash_FlagOrderInvariant(t *testing.T) {
	a := HashInput{
		ImageDigest:          "sha256:1",
		ModelID:              "m",
		EngineCompatFlags:    []string{"a", "b", "c"},
		CUDADriverMajor:      580,
		CaptureFormatVersion: 1,
	}
	b := a
	b.EngineCompatFlags = []string{"c", "a", "b"}
	if ComputeHash(a) != ComputeHash(b) {
		t.Fatalf("flag-order invariant broken: %s vs %s", ComputeHash(a), ComputeHash(b))
	}
}

func TestComputeHash_DistinguishesInputs(t *testing.T) {
	base := HashInput{
		ImageDigest:          "sha256:1",
		ModelID:              "m",
		EngineCompatFlags:    []string{"--tp=2"},
		CUDADriverMajor:      580,
		CaptureFormatVersion: 1,
	}

	// Each variation MUST produce a different hash; otherwise the cache
	// would return semantically incompatible captures across pods.
	mutators := []struct {
		name string
		mut  func(*HashInput)
	}{
		{"image_digest", func(h *HashInput) { h.ImageDigest = "sha256:2" }},
		{"model_id", func(h *HashInput) { h.ModelID = "other" }},
		{"add_flag", func(h *HashInput) { h.EngineCompatFlags = append(h.EngineCompatFlags, "--dtype=fp16") }},
		{"driver_major", func(h *HashInput) { h.CUDADriverMajor = 555 }},
		{"format_version", func(h *HashInput) { h.CaptureFormatVersion = 2 }},
	}
	baseHash := ComputeHash(base)
	for _, m := range mutators {
		t.Run(m.name, func(t *testing.T) {
			variant := base
			variant.EngineCompatFlags = append([]string(nil), base.EngineCompatFlags...)
			m.mut(&variant)
			if ComputeHash(variant) == baseHash {
				t.Fatalf("hash collision on mutating %s", m.name)
			}
		})
	}
}

func TestComputeHash_DoesNotMutateInput(t *testing.T) {
	in := HashInput{EngineCompatFlags: []string{"c", "a", "b"}}
	want := []string{"c", "a", "b"}
	_ = ComputeHash(in)
	if !equalSlice(in.EngineCompatFlags, want) {
		t.Fatalf("ComputeHash mutated caller's slice: got %v, want %v", in.EngineCompatFlags, want)
	}
}

func TestShortHash(t *testing.T) {
	h := strings.Repeat("a", 64)
	want := strings.Repeat("a", ShortHashLen)
	if got := ShortHash(h); got != want || len(got) != ShortHashLen {
		t.Fatalf("ShortHash = %q (len=%d), want %d a's", got, len(got), ShortHashLen)
	}
	if got := ShortHash("ab"); got != "ab" {
		t.Fatalf("ShortHash on short input: got %q, want %q", got, "ab")
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

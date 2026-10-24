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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// HashInput is the canonical set of fields a capture's content-address
// is derived from. Two pods that produce the same HashInput must produce
// captures that are semantically interchangeable: same model weights,
// same compiled engine artifacts, same JIT caches.
//
// EngineCompatFlags is the per-engine list of flags that affect what's in
// the cache. Order is normalized in ComputeHash so callers don't need to
// pre-sort. Examples:
//
//	vLLM:    --tensor-parallel-size, --dtype, --max-model-len,
//	         --gpu-memory-utilization, --quantization
//	SGLang:  --tp-size, --mem-fraction-static, --dtype
//	TRT-LLM: --tp_size, --kv_cache_free_gpu_memory_fraction
//	NIM:     NIM_TAGS_SELECTOR profile id (e.g. "tp:2|precision:fp8|profile:latency")
//
// CUDADriverMajor is e.g. 580 for driver 580.95.05. Captures don't migrate
// across major driver versions because driver libs in the rootfs upperdir
// (and trusted.overlay xattrs) won't match.
type HashInput struct {
	ImageDigest       string   `json:"image_digest"`
	ModelID           string   `json:"model_id"`
	EngineCompatFlags []string `json:"engine_compat_flags"`
	CUDADriverMajor   int      `json:"cuda_driver_major"`

	// GPUComputeCapability is the CUDA compute capability of the capture
	// node's GPU as "<major>.<minor>" (e.g. "9.0" for H100, "8.0" for
	// A100, "8.9" for L40S), read from the node's
	// nvidia.com/gpu.compute.{major,minor} labels. It is part of the
	// content identity because both capture paths bake arch-specific
	// state into the artifact — the CRIU GPU dump targets one SM arch,
	// and the rootfs tree carries arch-compiled caches (Triton autotune,
	// torch.compile, CUDA program cache, TRT engines). Reusing a capture
	// across compute capabilities would replay incompatible kernels.
	// Keyed on compute capability (NOT the product string) so SKUs of the
	// same arch — H100 SXM vs PCIe, both sm_90 — still share a capture.
	// Empty when the node lookup failed (degrades to the old behavior).
	GPUComputeCapability string `json:"gpu_compute_capability,omitempty"`

	CaptureFormatVersion int `json:"capture_format_version"`
}

// ComputeHash returns the full sha256 hex digest of a HashInput. EngineCompatFlags
// is sorted before hashing so callers can pass flags in any order.
func ComputeHash(in HashInput) string {
	flags := append([]string(nil), in.EngineCompatFlags...)
	sort.Strings(flags)
	in.EngineCompatFlags = flags

	// json.Marshal of a struct with named fields is deterministic in field
	// order (tag order). Slices preserve their order. Sorting flags above
	// is what makes the result canonical.
	data, err := json.Marshal(in)
	if err != nil {
		// HashInput has no unmarshalable fields; this should be unreachable.
		panic("checkpointstore: ComputeHash json.Marshal failed: " + err.Error())
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ShortHashLen is the prefix length ShortHash returns. 32 hex chars =
// 128 bits, birthday-collision at ~2^64 — crypto-strength uniqueness.
//
// Sized so every K8s resource name we emit fits its respective name limit:
//
//	PVC          (DNS-1123 subdomain, ≤253):  "nvsnap-capture-w-" + 32  = 47 chars
//	ConfigMap    (DNS-1123 subdomain, ≤253):  "nvsnap-capture-"   + 32  = 45 chars
//	Job          (DNS-1123 label,     ≤63):   "nvsnap-copy-"      + 32  = 42 chars
//	Label values (≤63):                                          32   = 32 chars
//
// 12 hex was the original prefix and gave only 48 bits — birthday collision
// at ~16M items, real risk in long-lived production fleets. 32 hex pushes
// that horizon out past anything we'd plausibly accumulate.
const ShortHashLen = 32

// ShortHash returns the first ShortHashLen hex characters of a hash,
// suitable for K8s resource names, label values, and human display.
func ShortHash(h string) string {
	if len(h) < ShortHashLen {
		return h
	}
	return h[:ShortHashLen]
}

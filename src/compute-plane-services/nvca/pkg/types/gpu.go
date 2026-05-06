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

package types //nolint:revive

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// TODO: constants for well-known GPUs ex. "A100"

const (
	GPUResourceKey = "nvidia.com/gpu"
)

type GPUName string

type GPUResourceSet map[GPUName]GPUResource

type GPUResource struct {
	Capacity  uint64
	Allocated uint64
}

// MarshalJSON serves to add the calculated field "available" to the JSON output
func (gpuRes GPUResource) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Available uint64 `json:"available"`
		Capacity  uint64 `json:"capacity"`
		Allocated uint64 `json:"allocated"`
	}{
		Available: gpuRes.Available(),
		Capacity:  gpuRes.Capacity,
		Allocated: gpuRes.Allocated,
	})
}

func (gpuRes GPUResource) HasCapacityForRequest(request uint64) bool {
	return gpuRes.Allocated+request <= gpuRes.Capacity
}

func (gpuRes GPUResource) Available() uint64 {
	// Prevent negative overflow
	if gpuRes.Allocated > gpuRes.Capacity {
		return 0
	}
	return gpuRes.Capacity - gpuRes.Allocated
}

type GPUResourceList map[string]resource.Quantity

// Only generically named GPU resources are supported for now.
// Non-generically named GPUs, ex. "GA102GL_A10", and those with high cardinality
// like MIG, ex. "mig-1g.5gb", are not supported.
func (l GPUResourceList) GetGPUCount(names ...string) (v uint64, anyFound bool) {
	for _, k := range names {
		if q, ok := l[k]; ok {
			v += uint64(q.AsApproximateFloat64())
			anyFound = true
		}
	}
	return v, anyFound
}

func (l GPUResourceList) GPU() (gpu *resource.Quantity, anyFound bool) {
	gpu = resource.NewQuantity(0, resource.DecimalSI)
	for k, v := range l {
		if k == "gpu" || k == "pgpu" {
			gpu.Add(v)
			anyFound = true
		}
	}
	return gpu, anyFound
}

// MIG resource names will have the format "mig-<N>g.<M>gb".
func (l GPUResourceList) HasMIG() bool {
	for k := range l {
		if strings.HasPrefix(k, "mig-") {
			return true
		}
	}
	return false
}

// Detects generic passthrough GPU resource types.
func (l GPUResourceList) HasGenericPGPU() bool {
	for k := range l {
		if k == "pgpu" {
			return true
		}
	}
	return false
}

func ParseGPUResourceList(rl corev1.ResourceList) GPUResourceList {
	out := GPUResourceList{}
	for rn, q := range rl {
		if s := strings.TrimPrefix(rn.String(), "nvidia.com/"); s != rn.String() {
			out[s] = q
		}
	}
	return out
}

func GetGPUNameFromInstanceType(instanceType string) (GPUName, error) {
	split := strings.SplitN(instanceType, ".", 3)
	if len(split) != 3 {
		return "", fmt.Errorf("expected 3 parts to instance type, got: %d", len(split))
	}
	return GPUName(split[2]), nil
}

var memStrRe = regexp.MustCompile("[1-9][0-9]*(T|G|M|K)B$")
var extractRTXGTX = regexp.MustCompile(`(?:RTX|GTX)(.+)`)

// migSuffixRe matches MIG profile suffixes in GPU product names.
// Examples: -MIG-2g.48gb, -MIG-1g.24gb, -MIG-7g.141gb
var migSuffixRe = regexp.MustCompile(`(?i)-MIG-(\d+g[.\-]\d+gb)$`)

// ParseGPUName parses a GPU product name into a standardized GPUName.
// MIG profile suffixes (e.g., "-MIG-2g.48gb") are detected and preserved
// in normalized form (hyphens, lowercase) to produce unique names per profile.
func ParseGPUName(s string) (GPUName, error) {
	// Extract MIG profile suffix before parsing, so it is not lost during
	// name normalization. Appended back after base GPU name is resolved.
	var migSuffix string
	if match := migSuffixRe.FindStringSubmatch(s); len(match) == 2 {
		s = s[:len(s)-len(match[0])]
		migSuffix = "-MIG-" + strings.ToLower(strings.ReplaceAll(match[1], ".", "-"))
	}

	// sanitize against RTX/GTX
	ss := extractRTXGTX.FindAllString(s, 1)
	// expect a single match on this else continue
	if len(ss) == 1 {
		s = ss[0]
	}
	if len(s) == 0 {
		return "", fmt.Errorf("empty GPU name")
	}
	split := strings.Split(strings.ToUpper(s), "-")
	// Remove suffixes denoting memory, which is reported elsewhere.
	if memStrRe.MatchString(split[len(split)-1]) {
		split = split[:len(split)-1]
	}
	// NVIDIA prefix is redundant.
	if split[0] == "NVIDIA" {
		split = split[1:]
	}
	if split[0] == "GEFORCE" {
		split = split[1:]
	}
	var name string
	switch {
	case len(split) > 1 && (split[0] == "RTX" || split[0] == "GTX"):
		// Ex. "RTX-A3000"
		name = split[0] + split[1]
		if len(split) > 2 {
			if split[2] == "TI" {
				// Ex. "GTX-3060-TI"
				name += "Ti"
			} else if split[1] == "PRO" &&
				// Must be a whole number following "PRO"
				regexp.MustCompile(`^[0-9]+$`).MatchString(split[2]) {
				// Ex. "RTXPRO6000"
				name += split[2]
			}
		}
	default:
		// Ex. "A100"
		name = split[0]
	}

	return GPUName(name + migSuffix), nil
}

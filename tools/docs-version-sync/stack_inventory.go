// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
)

const (
	selfManagedStackKey   = "self-managed"
	computePlaneStackKey  = "compute-plane"
	observabilityStackKey = "observability"
)

type stackInventorySpec struct {
	Key          string
	ResourceName string
	TagPrefix    string
	AssetName    string
	ConfigPath   string
}

var stackInventorySpecs = []stackInventorySpec{
	{
		Key:          selfManagedStackKey,
		ResourceName: controlStackResourceName,
		TagPrefix:    "deploy/stacks/self-managed/v",
		AssetName:    "nvcf-self-managed-stack-inventory.json",
		ConfigPath:   "deploy/stacks/self-managed/release-inventory.yaml",
	},
	{
		Key:          computePlaneStackKey,
		ResourceName: computeStackResourceName,
		TagPrefix:    "deploy/stacks/nvcf-compute-plane/v",
		AssetName:    "nvcf-compute-plane-stack-inventory.json",
		ConfigPath:   "deploy/stacks/nvcf-compute-plane/release-inventory.yaml",
	},
	{
		Key:          observabilityStackKey,
		ResourceName: observabilityStackResourceName,
		TagPrefix:    "deploy/stacks/observability/v",
		AssetName:    "nvcf-observability-stack-inventory.json",
		ConfigPath:   "deploy/stacks/observability/release-inventory.yaml",
	},
}

func stackInventorySpecByKey(key string) (stackInventorySpec, error) {
	for _, spec := range stackInventorySpecs {
		if spec.Key == key {
			return spec, nil
		}
	}
	return stackInventorySpec{}, fmt.Errorf("unknown stack %q; want self-managed, compute-plane, or observability", key)
}

func stackInventorySpecByTag(tag string) (stackInventorySpec, error) {
	for _, spec := range stackInventorySpecs {
		if strings.HasPrefix(tag, spec.TagPrefix) {
			return spec, nil
		}
	}
	return stackInventorySpec{}, fmt.Errorf("stack source tag %q does not match a released stack", tag)
}

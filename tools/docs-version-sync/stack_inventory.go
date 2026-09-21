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

func stackKeyForPlane(plane string) (string, error) {
	switch plane {
	case "control-plane":
		return selfManagedStackKey, nil
	case "compute-plane":
		return computePlaneStackKey, nil
	case "observability":
		return observabilityStackKey, nil
	default:
		return "", fmt.Errorf("unknown inventory plane %q", plane)
	}
}

// releaseSetFromInventories records the current release of each stack as
// development documentation. Freezing a stack train marks that stack qualified.
func releaseSetFromInventories(inventories map[string]resolvedStackInventory) (ReleaseSetMetadata, error) {
	metadata := func(key string) (StackReleaseMetadata, error) {
		spec, err := stackInventorySpecByKey(key)
		if err != nil {
			return StackReleaseMetadata{}, err
		}
		inventory, exists := inventories[key]
		if !exists {
			return StackReleaseMetadata{}, fmt.Errorf("%s inventory is required for release set metadata", key)
		}
		return StackReleaseMetadata{
			Version:              inventory.Source.Version,
			SourceTag:            inventory.Source.Tag,
			SourceCommit:         inventory.Source.Commit,
			InventoryAsset:       spec.AssetName,
			DocumentationVersion: "dev",
			Status:               ReleaseSetDevelopment,
		}, nil
	}
	controlPlane, err := metadata(selfManagedStackKey)
	if err != nil {
		return ReleaseSetMetadata{}, err
	}
	computePlane, err := metadata(computePlaneStackKey)
	if err != nil {
		return ReleaseSetMetadata{}, err
	}
	observability, err := metadata(observabilityStackKey)
	if err != nil {
		return ReleaseSetMetadata{}, err
	}
	return ReleaseSetMetadata{
		Stacks: ReleaseSetStacks{
			ControlPlane:  controlPlane,
			ComputePlane:  computePlane,
			Observability: observability,
		},
	}, nil
}

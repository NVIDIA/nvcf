// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package v1alpha1

type GangSchedulingInstruction struct {
	// PodGroups defines the groups of pods that should be scheduled together
	// +listType=map
	// +listMapKey=name
	PodGroups []PodGroupDefinition `json:"podGroups"`
}

// PodGroupDefinition defines a group of pods that should be scheduled together.
type PodGroupDefinition struct {
	// Name is the unique identifier for this pod group
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Members defines which components belong to this pod group
	// +listType=map
	// +listMapKey=componentName
	Members []PodGroupMemberDefinition `json:"members"`
}

// PodGroupMemberDefinition defines how to select and filter components for grouping instructions.
type PodGroupMemberDefinition struct {
	// ComponentName references a component defined in the Karta's structureDefinition
	// +kubebuilder:validation:Required
	ComponentName string `json:"componentName"`

	// GroupByKeyPaths are JQ paths to values used for grouping (e.g., owner name, replica key)
	// If empty, grouping is done via owner reference traversal
	// Every path must return a single, non-empty value - otherwise grouping will fail
	// JQ paths are evaluated against individual pod objects, not the root resource spec
	// +kubebuilder:validation:Optional
	// +listType=set
	GroupByKeyPaths []string `json:"groupByKeyPaths,omitempty" jq:"validate"`

	// Filters are JQ filter expressions to select specific components (expressions are ANDed)
	// Example: '(.spec.containers[0].resources.limits["nvidia.com/gpu"] // 0) > 0'
	// JQ filters are evaluated against individual pod objects, not the root resource spec
	// +kubebuilder:validation:Optional
	// +listType=set
	Filters []string `json:"filters,omitempty" jq:"validate"`
}

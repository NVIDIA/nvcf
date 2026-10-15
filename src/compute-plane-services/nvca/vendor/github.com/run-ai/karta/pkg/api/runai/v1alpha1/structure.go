// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package v1alpha1

// GroupVersionKind represents a Kubernetes API object's group, version, and kind.
type GroupVersionKind struct {
	// Group is the API group of the resource
	Group string `json:"group"`

	// Version is the API version of the resource
	Version string `json:"version"`

	// Kind is the API kind of the resource
	Kind string `json:"kind"`
}

// ComponentDefinition defines a single component in the workload hierarchy.
// Components represent logical units of computation that can be optimized independently.
type ComponentDefinition struct {
	// Name is the unique identifier for this component within the Karta
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Kind specifies the Kubernetes GroupVersionKind for this component
	// +kubebuilder:validation:Optional
	Kind *GroupVersionKind `json:"kind,omitempty"`

	// OwnerRef references the parent component in the hierarchy by name (nil for root component)
	// +kubebuilder:validation:Optional
	OwnerRef *string `json:"ownerRef,omitempty"`

	// SpecDefinition defines how to extract pod specifications from this component
	// +kubebuilder:validation:Optional
	SpecDefinition *SpecDefinition `json:"specDefinition,omitempty"`

	// ScaleDefinition defines how to extract scaling information from this component
	// +kubebuilder:validation:Optional
	ScaleDefinition *ScaleDefinition `json:"scaleDefinition,omitempty"`

	// StatusDefinition defines how to interpret the status of this component
	// should be added only for the root component
	// +kubebuilder:validation:Optional
	StatusDefinition *StatusDefinition `json:"statusDefinition,omitempty"`

	// InstanceIdPath is the JQ path to the instance id, for components that hold multiple pod definitions (in array or map)
	// +kubebuilder:validation:Optional
	InstanceIdPath *string `json:"instanceIdPath,omitempty" jq:"validate"`

	// PodSelector defines how to identify pods belonging to this component
	// +kubebuilder:validation:Optional
	PodSelector *PodSelector `json:"podSelector,omitempty"`
}

// SpecDefinition defines how to extract pod specifications from a component.
// Only one of the three options should be provided (PodTemplateSpec, FragmentedPodSpec, PodSpec + Metadata).
type SpecDefinition struct {
	// PodTemplateSpecPath is the JQ path to a complete PodTemplateSpec object
	// +kubebuilder:validation:Optional
	PodTemplateSpecPath *string `json:"podTemplateSpecPath,omitempty" jq:"validate"`

	// PodSpecPath is the JQ path to a complete PodSpec object
	// +kubebuilder:validation:Optional
	PodSpecPath *string `json:"podSpecPath,omitempty" jq:"validate"`

	// MetadataPath is the JQ path to the component metadata
	// May be used only with PodSpecPath, in cases where pod spec and metadata are separated
	// +kubebuilder:validation:Optional
	MetadataPath *string `json:"metadataPath,omitempty" jq:"validate"`

	// FragmentedPodSpecDefinition defines how to extract individual pod spec fields
	// when they are scattered across different paths in the component
	// +kubebuilder:validation:Optional
	FragmentedPodSpecDefinition *FragmentedPodSpecDefinition `json:"fragmentedPodSpecDefinition,omitempty"`
}

// FragmentedPodSpecDefinition defines JQ paths to individual pod spec fields
// when they are scattered across different locations in the component YAML.
type FragmentedPodSpecDefinition struct {
	// SchedulerNamePath is the JQ path to the scheduler name
	// +kubebuilder:validation:Optional
	SchedulerNamePath *string `json:"schedulerNamePath,omitempty" jq:"validate"`

	// LabelsPath is the JQ path to pod labels
	// +kubebuilder:validation:Optional
	LabelsPath *string `json:"labelsPath,omitempty" jq:"validate"`

	// AnnotationsPath is the JQ path to pod annotations
	// +kubebuilder:validation:Optional
	AnnotationsPath *string `json:"annotationsPath,omitempty" jq:"validate"`

	// ResourcesPath is the JQ path to resource requirements
	// +kubebuilder:validation:Optional
	ResourcesPath *string `json:"resourcesPath,omitempty" jq:"validate"`

	// ResourceClaimsPath is the JQ path to DRA resource claims
	// +kubebuilder:validation:Optional
	ResourceClaimsPath *string `json:"resourceClaimsPath,omitempty" jq:"validate"`

	// PodAffinityPath is the JQ path to pod affinity rules
	// +kubebuilder:validation:Optional
	PodAffinityPath *string `json:"podAffinityPath,omitempty" jq:"validate"`

	// NodeAffinityPath is the JQ path to node affinity rules
	// +kubebuilder:validation:Optional
	NodeAffinityPath *string `json:"nodeAffinityPath,omitempty" jq:"validate"`

	// ContainersPath is the JQ path to containers specifications
	// +kubebuilder:validation:Optional
	ContainersPath *string `json:"containersPath,omitempty" jq:"validate"`

	// ContainesPath is the JQ path to a single container specifications
	// Used when the component has only one container
	// +kubebuilder:validation:Optional
	ContainerPath *string `json:"containerPath,omitempty" jq:"validate"`

	// PriorityClassNamePath is the JQ path to the priority class name
	// +kubebuilder:validation:Optional
	PriorityClassNamePath *string `json:"priorityClassNamePath,omitempty" jq:"validate"`

	// ImagePath is the JQ path to the container image
	// +kubebuilder:validation:Optional
	ImagePath *string `json:"imagePath,omitempty" jq:"validate"`
}

// ScaleDefinition defines how to extract scaling information from a component.
type ScaleDefinition struct {
	// ReplicasPath is the JQ path to the current replica count
	// +kubebuilder:validation:Optional
	ReplicasPath *string `json:"replicasPath,omitempty" jq:"validate"`

	// MinReplicasPath is the JQ path to the minimum replica count
	// +kubebuilder:validation:Optional
	MinReplicasPath *string `json:"minReplicasPath,omitempty" jq:"validate"`

	// MaxReplicasPath is the JQ path to the maximum replica count
	// +kubebuilder:validation:Optional
	MaxReplicasPath *string `json:"maxReplicasPath,omitempty" jq:"validate"`
}

// PodSelector defines how to identify pods belonging to a specific component.
type PodSelector struct {
	// ComponentTypeSelector determines whether a pod belongs to this component type
	// by matching a pod label or annotation value via a JQ path.
	// This is the primary mechanism for pod-to-component membership.
	// For example, LWS "leader" uses worker-index="0" to identify leader pods,
	// and LWS "worker" checks for the existence of the leader-name annotation.
	// +kubebuilder:validation:Optional
	ComponentTypeSelector *ComponentTypeSelector `json:"componentTypeSelector,omitempty"`

	// ComponentInstanceSelector splits a single ComponentDefinition into multiple
	// component instances, each identified by a unique value extracted from the pod.
	// Use this when instances of the same component type represent fundamentally
	// different roles or services (e.g., Dynamo "service" definition produces
	// separate "Frontend", "PrefillWorker", "DecodeWorker" instances).
	// This is distinct from ReplicaSelector: ComponentInstanceSelector creates
	// instances with potentially different specs/behavior, while ReplicaSelector
	// creates replicas of an identical sub-structure.
	// +kubebuilder:validation:Optional
	ComponentInstanceSelector *ComponentInstanceSelector `json:"componentInstanceSelector,omitempty"`

	// ReplicaSelector identifies which replica index or group the pod belongs to.
	// Use this to distinguish between multiple replicas of the same component / component instance
	// sub-structure (e.g., LWS Group 0 vs Group 1, where each group has identical
	// leader/worker descendants). Descendant components automatically inherit the replica
	// context from their ancestor (parent), so ReplicaSelector should only be defined at
	// the level where replicas are created, not repeated in descendants.
	// +kubebuilder:validation:Optional
	ReplicaSelector *ReplicaSelector `json:"replicaSelector,omitempty"`
}

type ComponentTypeSelector struct {
	// KeyPath is the JQ path to the identifying key/label on the pod
	// JQ path is evaluated against individual pod objects, not the root resource spec
	// +kubebuilder:validation:Required
	KeyPath string `json:"keyPath" jq:"validate"`

	// Value is the expected value for the key (optional - if nil, only key existence is checked)
	// +kubebuilder:validation:Optional
	Value *string `json:"value,omitempty"`
}

type ComponentInstanceSelector struct {
	// IdPath is the JQ path to the component instance identifier on the pod
	// JQ path is evaluated against individual pod objects, not the root resource spec
	// +kubebuilder:validation:Required
	IdPath string `json:"idPath" jq:"validate"`
}

// ReplicaSelector identifies the replica index/group a pod belongs to.
type ReplicaSelector struct {
	// KeyPath is the JQ path to the replica identifier on the pod.
	// The evaluated result should be the replica index or group identifier (e.g., "0", "1").
	// +kubebuilder:validation:Required
	KeyPath string `json:"keyPath" jq:"validate"`
}

// ResourceStatus represents the high-level status of a component.
// +kubebuilder:validation:Enum=Initializing;Running;Completed;Failed;Degraded;Undefined
type ResourceStatus string

const (
	// InitializingStatus indicates the component has been created or starting up or preparing to run (pre Running status)
	InitializingStatus ResourceStatus = "Initializing"

	// RunningStatus indicates the component is actively running
	RunningStatus ResourceStatus = "Running"

	// CompletedStatus indicates the component has finished successfully
	CompletedStatus ResourceStatus = "Completed"

	// FailedStatus indicates the component has failed
	FailedStatus ResourceStatus = "Failed"

	// DegradedStatus indicates the component is running but in a degraded state
	DegradedStatus ResourceStatus = "Degraded"

	// UndefinedStatus is used when status was not defined or cannot be determined
	UndefinedStatus ResourceStatus = "Undefined"
)

// StatusDefinition defines how to interpret the status of a component.
type StatusDefinition struct {
	// PhaseDefinition defines how to extract a simple phase/state string
	// +kubebuilder:validation:Optional
	PhaseDefinition *PhaseDefinition `json:"phaseDefinition,omitempty"`

	// ConditionsDefinition defines how to extract Kubernetes-style conditions
	// +kubebuilder:validation:Optional
	ConditionsDefinition *ConditionsDefinition `json:"conditionsDefinition,omitempty"`

	// StatusMappings define how to map extracted status to ResourceStatus values
	// +kubebuilder:validation:Required
	StatusMappings StatusMappings `json:"statusMappings"`
}

// PhaseDefinition defines how to extract a simple phase/state string from the component.
type PhaseDefinition struct {
	// Path is the JQ path to the phase/state field
	// +kubebuilder:validation:Required
	Path string `json:"path" jq:"validate"`
}

// ConditionsDefinition defines how to extract Kubernetes-style conditions from the component.
type ConditionsDefinition struct {
	// Path is the JQ path to the conditions array
	// +kubebuilder:validation:Required
	Path string `json:"path" jq:"validate"`

	// TypeFieldName is the field name for the condition type
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=type
	TypeFieldName string `json:"typeFieldName"`

	// StatusFieldName is the field name for the condition status
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=status
	StatusFieldName string `json:"statusFieldName"`

	// MessageFieldName is the field name for the condition text message
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=message
	MessageFieldName *string `json:"messageFieldName"`

	// ReasonFieldName is the field name for the condition reason
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=reason
	ReasonFieldName *string `json:"reasonFieldName"`
}

// StatusMappings define how to map extracted status information to ResourceStatus values.
// Each status field contains an array of matchers evaluated with OR logic:
// if ANY matcher in the array succeeds, that status is matched.
type StatusMappings struct {
	// Initializing defines matchers for the Initializing status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Initializing []StatusMatcher `json:"initializing,omitempty"`

	// Running defines matchers for the Running status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Running []StatusMatcher `json:"running,omitempty"`

	// Completed defines matchers for the Completed status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Completed []StatusMatcher `json:"completed,omitempty"`

	// Failed defines matchers for the Failed status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Failed []StatusMatcher `json:"failed,omitempty"`

	// Degraded defines matchers for the Degraded status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Degraded []StatusMatcher `json:"degraded,omitempty"`
}

// StatusMatchEntry pairs a ResourceStatus with its matchers.
type StatusMatchEntry struct {
	Status   ResourceStatus
	Matchers []StatusMatcher
}

// Entries returns all status-to-matchers pairs defined in the mappings.
// When adding a new ResourceStatus, add a corresponding entry here.
func (m StatusMappings) Entries() []StatusMatchEntry {
	return []StatusMatchEntry{
		{RunningStatus, m.Running},
		{FailedStatus, m.Failed},
		{CompletedStatus, m.Completed},
		{InitializingStatus, m.Initializing},
		{DegradedStatus, m.Degraded},
	}
}

// StatusMatcher defines criteria for matching a specific status.
// If both ByPhase and ByConditions are provided, ALL must match (AND logic).
type StatusMatcher struct {
	// ByPhase matches against a specific phase value
	// +kubebuilder:validation:Optional
	ByPhase string `json:"byPhase,omitempty"`

	// ByConditions matches against specific condition combinations (ANDed together) at least one of status or reason must be used.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	ByConditions []ExpectedCondition `json:"byConditions,omitempty"`

	// ByExpression is a JQ expression that matches against the object for status matching
	// +kubebuilder:validation:Optional
	ByExpression *ExpressionMatcher `json:"byExpression,omitempty"`
}

// ExpressionMatcher defines a JQ expression and its expected result for status matching.
type ExpressionMatcher struct {
	// Expression is the JQ expression to evaluate
	// +kubebuilder:validation:Required
	Expression string `json:"expression" jq:"validate"`

	// ExpectedResult is the expected result value in string format from the expression evaluation
	// +kubebuilder:validation:Required
	ExpectedResult string `json:"expectedResult"`
}

// ExpectedCondition defines a condition type and status that must be present.
type ExpectedCondition struct {
	// Type is the condition type to match
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Status is the expected condition status
	// +kubebuilder:validation:Optional
	Status *string `json:"status,omitempty"`

	// Reason is the expected condition reason
	// +kubebuilder:validation:Optional
	Reason *string `json:"reason,omitempty"`
}

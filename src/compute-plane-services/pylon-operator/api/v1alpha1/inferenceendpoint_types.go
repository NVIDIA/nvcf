/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InferenceAPIFormatType names the inference API surface a backend serves.
// +kubebuilder:validation:Enum=chat
type InferenceAPIFormatType string

const (
	// InferenceAPIFormatChat is POST /v1/chat/completions with the model
	// listing at GET /v1/models.
	InferenceAPIFormatChat InferenceAPIFormatType = "chat"
)

// InferenceAPIFormat describes the inference API the backend serves.
type InferenceAPIFormat struct {
	// Type selects the API surface. chat is the only value in v1alpha1.
	// +required
	Type InferenceAPIFormatType `json:"type"`
}

// ServiceReference names the ClusterIP Service in the endpoint's namespace
// that serves the inference API.
type ServiceReference struct {
	// Name of the Service in the same namespace as the InferenceEndpoint.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// Port is the Service port that serves the inference API.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +required
	Port int32 `json:"port"`
}

// HealthCheck describes the backend health endpoint.
type HealthCheck struct {
	// Path is the HTTP path probed on service.port, for example
	// /v1/health/ready. Pylon and the router probe the same path.
	// +kubebuilder:validation:Pattern=`^/`
	// +required
	Path string `json:"path"`
}

// GPUSpec carries an explicit GPU type for status.gpu.
type GPUSpec struct {
	// Product is the GPU product name reported in status.gpu, for example
	// NVIDIA-GB10. When empty, the operator derives it from the
	// nvidia.com/gpu.product label of the nodes behind the Service.
	// +optional
	Product string `json:"product,omitempty"`
}

// InferenceEndpointSpec is the desired state of an InferenceEndpoint.
type InferenceEndpointSpec struct {
	// ModelName is the exact string clients send in the OpenAI model field.
	// +kubebuilder:validation:MinLength=1
	// +required
	ModelName string `json:"modelName"`

	// InferenceAPIFormat selects the inference API surface.
	// +required
	InferenceAPIFormat InferenceAPIFormat `json:"inferenceAPIFormat"`

	// Service is the Service that fronts the backend.
	// +required
	Service ServiceReference `json:"service"`

	// Health is the backend health endpoint, served on service.port.
	// +required
	Health HealthCheck `json:"health"`

	// MaxEngineConcurrency is a concurrency hint for Pylon's queue estimate.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxEngineConcurrency *int32 `json:"maxEngineConcurrency,omitempty"`

	// GPU optionally overrides the GPU type derived from node labels.
	// +optional
	GPU *GPUSpec `json:"gpu,omitempty"`
}

// GPUSource records where status.gpu.product came from.
// +kubebuilder:validation:Enum=Spec;NodeLabels;Unknown
type GPUSource string

const (
	// GPUSourceSpec means spec.gpu.product was set.
	GPUSourceSpec GPUSource = "Spec"
	// GPUSourceNodeLabels means the product was derived from node labels.
	GPUSourceNodeLabels GPUSource = "NodeLabels"
	// GPUSourceUnknown means no endpoint has a node yet or no node carries
	// the nvidia.com/gpu.product label.
	GPUSourceUnknown GPUSource = "Unknown"
)

// GPUStatus is the effective GPU type of the endpoint. It is informational
// and never affects conditions.
type GPUStatus struct {
	// Product is the effective GPU product. Derived values from several
	// nodes are sorted and comma-joined.
	// +optional
	Product string `json:"product,omitempty"`

	// Source records where Product came from.
	// +optional
	Source GPUSource `json:"source,omitempty"`

	// NodeProducts lists the distinct nvidia.com/gpu.product label values of
	// the nodes behind the Service, sorted.
	// +listType=atomic
	// +optional
	NodeProducts []string `json:"nodeProducts,omitempty"`
}

// RegistrationStatus summarises the transport's registration with the
// router, as observed from Pylon's metrics.
type RegistrationStatus struct {
	// ClusterID is the operator's --cluster-id.
	// +optional
	ClusterID string `json:"clusterId,omitempty"`

	// RoutersConnected is the number of routers with an open registration
	// stream from at least one transport pod.
	// +optional
	RoutersConnected int32 `json:"routersConnected"`

	// LastRegisteredTime is when a registration stream was last observed
	// open.
	// +optional
	LastRegisteredTime *metav1.Time `json:"lastRegisteredTime,omitempty"`
}

// ServerStatus is the per-transport-pod view of one inference server.
type ServerStatus struct {
	// InferenceServerID is <clusterId>.<namespace>.<name>.<replica>.
	// +required
	InferenceServerID string `json:"inferenceServerId"`

	// Pod is the transport pod name.
	// +optional
	Pod string `json:"pod,omitempty"`

	// RegistrationStreams is the number of open registration streams.
	// +optional
	RegistrationStreams int32 `json:"registrationStreams"`

	// ReverseTunnels is the number of connected reverse tunnels.
	// +optional
	ReverseTunnels int32 `json:"reverseTunnels"`
}

// InferenceEndpointStatus is the observed state of an InferenceEndpoint.
type InferenceEndpointStatus struct {
	// ObservedGeneration is the metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions are Ready, TransportReady and Registered.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Registration summarises the transport's registration with the router.
	// +optional
	Registration *RegistrationStatus `json:"registration,omitempty"`

	// GPU is the effective GPU type.
	// +optional
	GPU *GPUStatus `json:"gpu,omitempty"`

	// Servers lists one entry per transport pod.
	// +listType=map
	// +listMapKey=inferenceServerId
	// +optional
	Servers []ServerStatus `json:"servers,omitempty"`
}

// The SERVERS printer column shows status.registration.routersConnected,
// because a printer column JSONPath cannot count the entries of
// status.servers.

// InferenceEndpoint publishes a model served by a Service in this namespace
// to the LLM invocation plane.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.modelName`
// +kubebuilder:printcolumn:name="GPU",type=string,JSONPath=`.status.gpu.product`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Registered",type=string,JSONPath=`.status.conditions[?(@.type=="Registered")].status`
// +kubebuilder:printcolumn:name="Servers",type=integer,JSONPath=`.status.registration.routersConnected`,description="Routers with an open registration stream"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type InferenceEndpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec InferenceEndpointSpec `json:"spec"`

	// +optional
	Status InferenceEndpointStatus `json:"status,omitempty"`
}

// InferenceEndpointList is a list of InferenceEndpoint.
// +kubebuilder:object:root=true
type InferenceEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InferenceEndpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InferenceEndpoint{}, &InferenceEndpointList{})
}

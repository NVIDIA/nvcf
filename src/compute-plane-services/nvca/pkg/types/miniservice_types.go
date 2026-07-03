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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	corev1 "k8s.io/api/core/v1"
)

const (
	// MiniserviceMetadataConfigMapName is the name of the ConfigMap that the MiniService
	// controller writes to every operator-mode namespace before workload objects are created.
	// The operator webhook reads it at admission time to resolve NVCF metadata without
	// making additional API calls against control-plane services.
	//
	// The controller must create this ConfigMap before OAuth-applying operator CRD manifests
	// so that the webhook's Fail-policy CREATE rule never fires against an unpopulated namespace.
	MiniserviceMetadataConfigMapName = "nvcf-miniservice-metadata"

	MiniserviceMetadataDataKey = "metadata"

	// MiniserviceNameLabel is the label key whose value is the owning MiniService name.
	// It must be set on every object in a MiniService namespace so the controller can map
	// secondary objects back to their owner via a label event handler.
	MiniserviceNameLabel = labelFQDNPrefix + "/miniservice-name"
)

// MiniserviceMetadata is the parsed in-memory representation of the nvcf-miniservice-metadata
// ConfigMap. The struct is JSON-serialized into a single ConfigMap data key.
type MiniserviceMetadata struct {
	// NVCF metadata fields for all objects.

	MessageAction common.MessageAction `json:"messageAction,omitempty"`
	Annotations   map[string]string    `json:"annotations,omitempty"`
	Labels        map[string]string    `json:"labels,omitempty"`

	// Pod-spec fields the webhook applies to admitted Pods.

	PodAnnotations                map[string]string   `json:"podAnnotations,omitempty"`
	PodLabels                     map[string]string   `json:"podLabels,omitempty"`
	EnvVars                       []corev1.EnvVar     `json:"envVars,omitempty"`
	OTelCollectorEnvVars          []corev1.EnvVar     `json:"otelCollectorEnvVars,omitempty"`
	NodeAffinityKey               string              `json:"nodeAffinityKey,omitempty"`
	NodeAffinityValue             string              `json:"nodeAffinityValue,omitempty"`
	ServiceAccountName            string              `json:"serviceAccountName,omitempty"`
	Tolerations                   []corev1.Toleration `json:"tolerations,omitempty"`
	ImagePullSecretNames          []string            `json:"imagePullSecretNames,omitempty"`
	TerminationGracePeriodSeconds *int64              `json:"terminationGracePeriodSeconds,omitempty"`
	SchedulerName                 string              `json:"schedulerName,omitempty"`
}

// ToConfigMapData serializes m into ConfigMap-compatible flat string data.
// Simple fields are stored directly; complex fields are JSON-encoded.
func (m MiniserviceMetadata) ToConfigMapData() (map[string]string, error) {
	jsonData, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode miniservice metadata: %w", err)
	}
	return map[string]string{
		MiniserviceMetadataDataKey: string(jsonData),
	}, nil
}

// FromConfigMapData deserializes ConfigMap data into MiniserviceMetadata.
// This is the inverse of ToConfigMapData and handles JSON-encoded complex fields.
func FromConfigMapData(cmData map[string]string) (MiniserviceMetadata, error) {
	data, ok := cmData[MiniserviceMetadataDataKey]
	if !ok || data == "" {
		return MiniserviceMetadata{}, fmt.Errorf("missing or empty %s key in ConfigMap data", MiniserviceMetadataDataKey)
	}
	var m MiniserviceMetadata
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return MiniserviceMetadata{}, fmt.Errorf("decode miniservice metadata: %w", err)
	}
	return m, nil
}

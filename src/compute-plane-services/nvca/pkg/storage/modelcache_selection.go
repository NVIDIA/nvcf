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

package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// ModelCacheStorageSelectionAnnotationKey persists the storage decision on
	// the ICMSRequest before any cache storage side effect.
	ModelCacheStorageSelectionAnnotationKey = "storage.nvcf.nvidia.io/model-cache-selection"

	// ModelCacheStorageSelectionVersion is the annotation contract version.
	ModelCacheStorageSelectionVersion = "v1alpha1"
	modelCacheStorageSelectionVersion = ModelCacheStorageSelectionVersion
)

// ModelCacheSelectionMode is the request-level cache behavior.
type ModelCacheSelectionMode string

const (
	ModelCacheSelectionNone      ModelCacheSelectionMode = "none"
	ModelCacheSelectionEphemeral ModelCacheSelectionMode = "ephemeral"
	ModelCacheSelectionDurable   ModelCacheSelectionMode = "durable"
)

// PersistedModelCacheStorageSelection is intentionally small. It is an
// immutable request snapshot, not a general CSI capability matrix.
type PersistedModelCacheStorageSelection struct {
	Version              string                              `json:"version"`
	Workflow             ModelCacheWorkflow                  `json:"workflow"`
	Mode                 ModelCacheSelectionMode             `json:"mode"`
	StorageClassName     string                              `json:"storageClassName,omitempty"`
	StorageClassUID      types.UID                           `json:"storageClassUID,omitempty"`
	StorageClassDigest   string                              `json:"storageClassDigest,omitempty"`
	ProfileDigest        string                              `json:"profileDigest,omitempty"`
	CatalogRevision      string                              `json:"catalogRevision,omitempty"`
	Provider             string                              `json:"provider,omitempty"`
	Provisioner          string                              `json:"provisioner,omitempty"`
	Transition           string                              `json:"transition,omitempty"`
	RequiredAccessModes  []corev1.PersistentVolumeAccessMode `json:"requiredAccessModes,omitempty"`
	RequiredMountOptions []string                            `json:"requiredMountOptions,omitempty"`
	EncryptionRequired   bool                                `json:"encryptionRequired,omitempty"`
	BindingName          string                              `json:"bindingName,omitempty"`
	BindingUID           types.UID                           `json:"bindingUID,omitempty"`
}

// NewPersistedModelCacheStorageSelection creates and validates a request
// snapshot. resolved may be nil for a non-durable fallback caused by an absent
// nvcf-sc.
func NewPersistedModelCacheStorageSelection(
	workflow ModelCacheWorkflow,
	mode ModelCacheSelectionMode,
	resolved *ModelCacheStorageSelection,
) (*PersistedModelCacheStorageSelection, error) {
	selection := &PersistedModelCacheStorageSelection{
		Version:  ModelCacheStorageSelectionVersion,
		Workflow: workflow,
		Mode:     mode,
	}
	if resolved != nil {
		selection.StorageClassName = resolved.StorageClassName
		selection.StorageClassUID = resolved.StorageClassUID
		selection.StorageClassDigest = resolved.StorageClassDigest
		selection.ProfileDigest = resolved.ProfileDigest
		selection.CatalogRevision = resolved.CatalogRevision
		selection.Provider = resolved.Provider
		selection.Provisioner = resolved.Provisioner
		selection.Transition = resolved.Transition
		selection.RequiredAccessModes = append(
			[]corev1.PersistentVolumeAccessMode(nil), resolved.RequiredAccessModes...)
		selection.RequiredMountOptions = append([]string(nil), resolved.RequiredMountOptions...)
	}
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	return selection, nil
}

// Marshal returns the canonical annotation payload.
func (s *PersistedModelCacheStorageSelection) Marshal() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshal model cache storage selection: %w", err)
	}
	return string(raw), nil
}

// ParsePersistedModelCacheStorageSelection strictly parses an annotation.
func ParsePersistedModelCacheStorageSelection(raw string) (*PersistedModelCacheStorageSelection, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()

	selection := &PersistedModelCacheStorageSelection{}
	if err := decoder.Decode(selection); err != nil {
		return nil, fmt.Errorf("parse model cache storage selection: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	return selection, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("parse model cache storage selection: multiple JSON values")
		}
		return fmt.Errorf("parse model cache storage selection: %w", err)
	}
	return nil
}

// Validate rejects partial or invented decisions.
func (s *PersistedModelCacheStorageSelection) Validate() error {
	if s == nil {
		return fmt.Errorf("model cache storage selection is nil")
	}
	if s.Version != ModelCacheStorageSelectionVersion {
		return fmt.Errorf("unsupported model cache storage selection version %q", s.Version)
	}
	switch s.Workflow {
	case ModelCacheWorkflowRegular, ModelCacheWorkflowHelm:
	default:
		return fmt.Errorf("invalid model cache workflow %q", s.Workflow)
	}
	switch s.Mode {
	case ModelCacheSelectionNone, ModelCacheSelectionEphemeral, ModelCacheSelectionDurable:
	default:
		return fmt.Errorf("invalid model cache selection mode %q", s.Mode)
	}
	if s.Mode == ModelCacheSelectionEphemeral && s.Workflow != ModelCacheWorkflowHelm {
		return fmt.Errorf("ephemeral model cache selection requires Helm workflow")
	}
	if (s.BindingName == "") != (s.BindingUID == "") {
		return fmt.Errorf("model cache selection has incomplete binding reference")
	}
	if s.Mode != ModelCacheSelectionDurable {
		if s.BindingName != "" {
			return fmt.Errorf("non-durable model cache selection has a binding reference")
		}
		if s.EncryptionRequired {
			return fmt.Errorf("non-durable model cache selection requires encryption")
		}
	}

	hasResolvedFields := s.StorageClassName != "" || s.StorageClassUID != "" || s.StorageClassDigest != "" ||
		s.ProfileDigest != "" || s.Provider != "" || s.Provisioner != "" || s.Transition != "" ||
		len(s.RequiredAccessModes) != 0 || len(s.RequiredMountOptions) != 0
	if !hasResolvedFields {
		if s.Mode == ModelCacheSelectionDurable {
			return fmt.Errorf("durable model cache selection has no resolved storage")
		}
		return nil
	}
	if s.StorageClassName != DefaultModelCacheStorageClassName {
		return fmt.Errorf("model cache selection StorageClass must be %q", DefaultModelCacheStorageClassName)
	}
	if s.StorageClassUID == "" || strings.TrimSpace(s.StorageClassDigest) == "" ||
		strings.TrimSpace(s.ProfileDigest) == "" || strings.TrimSpace(s.Provider) == "" ||
		strings.TrimSpace(s.Provisioner) == "" || strings.TrimSpace(s.Transition) == "" {
		return fmt.Errorf("model cache selection has incomplete resolved storage")
	}

	switch s.Mode {
	case ModelCacheSelectionDurable:
		switch s.Transition {
		case ModelCacheTransitionROXReadOnly:
			// The provider and provisioner are recorded, not constrained: the
			// catalog decides which drivers may run this transition. See the
			// roxReadOnly case in validateStorageCapabilityCatalog.
			for _, required := range []string{"ro"} {
				if !slices.Contains(s.RequiredMountOptions, required) {
					return fmt.Errorf("model cache transition %q requires mount option %q",
						s.Transition, required)
				}
			}
		case ModelCacheTransitionRWXReadOnly:
			if s.EncryptionRequired {
				return fmt.Errorf("model cache transition %q does not support encryption",
					s.Transition)
			}
			if len(s.RequiredMountOptions) != 0 {
				return fmt.Errorf("model cache transition %q does not create a reader PV and cannot require mount options",
					s.Transition)
			}
		default:
			return fmt.Errorf("durable model cache selection has unsupported transition %q", s.Transition)
		}
		if !slices.Equal(s.RequiredAccessModes, requiredAccessModesForTransition(s.Transition)) {
			return fmt.Errorf("model cache transition %q requires access modes %v, got %v",
				s.Transition, requiredAccessModesForTransition(s.Transition), s.RequiredAccessModes)
		}
	case ModelCacheSelectionNone, ModelCacheSelectionEphemeral:
		if s.Transition != ModelCacheTransitionDisabled {
			return fmt.Errorf("non-durable model cache selection has transition %q", s.Transition)
		}
		if len(s.RequiredAccessModes) != 0 {
			return fmt.Errorf("non-durable model cache selection has required access modes")
		}
		if len(s.RequiredMountOptions) != 0 {
			return fmt.Errorf("non-durable model cache selection has required mount options")
		}
	}
	seenMountOptions := make(map[string]struct{}, len(s.RequiredMountOptions))
	for i, option := range s.RequiredMountOptions {
		if strings.TrimSpace(option) == "" || strings.TrimSpace(option) != option {
			return fmt.Errorf("model cache selection has invalid required mount option %q", option)
		}
		if _, found := seenMountOptions[option]; found {
			return fmt.Errorf("model cache selection has duplicate required mount option %q", option)
		}
		for _, previous := range s.RequiredMountOptions[:i] {
			if negatesMountOption(previous, option) {
				return fmt.Errorf("model cache selection required mount options %q and %q conflict",
					previous, option)
			}
		}
		seenMountOptions[option] = struct{}{}
	}
	return nil
}

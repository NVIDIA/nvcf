/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package types

import (
	"crypto/sha256"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// ControlPlaneIDLabel identifies resources owned by one control plane when
	// multiple NVCF control planes share a Kubernetes cluster.
	ControlPlaneIDLabel = "nvcf.nvidia.com/control-plane-id"
	// MaxControlPlaneIDLength matches the supported self-managed stack prefix.
	MaxControlPlaneIDLength = 20
)

// ValidateControlPlaneID validates the optional stable control-plane identity.
// Empty is valid and selects the legacy single-control-plane behavior.
func ValidateControlPlaneID(id string) error {
	if id == "" {
		return nil
	}
	if id == "default" {
		return fmt.Errorf("control plane ID %q is reserved for legacy mode", id)
	}
	if len(id) > MaxControlPlaneIDLength {
		return fmt.Errorf("control plane ID must be at most %d characters", MaxControlPlaneIDLength)
	}
	if errs := validation.IsDNS1123Label(id); len(errs) != 0 {
		return fmt.Errorf("control plane ID must be a DNS-1123 label: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ControlPlaneResourceName returns the legacy name for an empty ID and a
// deterministic, DNS-safe prefixed name in named mode.
func ControlPlaneResourceName(id, legacyName string) string {
	if id == "" {
		return legacyName
	}
	name := id + "-" + legacyName
	if len(name) <= validation.DNS1123LabelMaxLength {
		return name
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:8]
	prefixLen := validation.DNS1123LabelMaxLength - len(hash) - 1
	return strings.TrimRight(name[:prefixLen], "-") + "-" + hash
}

// IsOwnedByControlPlane returns whether an object belongs to the supplied
// identity. Legacy mode deliberately accepts unlabelled objects.
func IsOwnedByControlPlane(obj metav1.Object, id string) bool {
	if id == "" {
		return true
	}
	return obj != nil && obj.GetLabels()[ControlPlaneIDLabel] == id
}

// AddControlPlaneLabel stamps the stable identity without changing legacy
// labels when the ID is empty.
func AddControlPlaneLabel(labels map[string]string, id string) map[string]string {
	if id == "" {
		return labels
	}
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ControlPlaneIDLabel] = id
	return labels
}

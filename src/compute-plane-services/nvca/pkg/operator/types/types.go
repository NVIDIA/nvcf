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
	"fmt"
	"slices"
	"strings"

	nvcfv1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

type EventCategory string

const (
	NVCAModuleName   string = "nvca"
	NVCAOperatorName string = "nvca-operator"

	// NVCAOperatorFinalizer is the finalizer added to NVCFBackend resources
	NVCAOperatorFinalizer = "nvca-operator.finalizers.nvidia.io"

	// SentinelFinalizer is the finalizer added to the shutdown sentinel ConfigMap
	SentinelFinalizer = "nvca.nvcf.nvidia.io/operator-cleanup"

	// ShutdownSentinelConfigMapName is the name of the shutdown sentinel ConfigMap.
	// When this ConfigMap is deleted, the operator will enter shutdown mode.
	ShutdownSentinelConfigMapName = "nvca-operator-shutdown-sentinel"

	// Default namespace names
	DefaultNVCASystemNamespace   = "nvca-system"
	DefaultNVCARequestsNamespace = "nvcf-backend"

	// CRD names
	StorageRequestCRDName = "storagerequests.nvca.nvcf.nvidia.io"
	MiniServicesCRDName   = "miniservices.nvca.nvcf.nvidia.io"
)

const (
	EventCategoryInstall EventCategory = "Install"
	EventCategoryUpgrade EventCategory = "Upgrade"
	EventCategoryHealth  EventCategory = "Health"
)

const (
	DefaultCacheCSIMountOptions = "ro,norecovery,nouuid"
)

type ClusterStatus string

const (
	ClusterStatusNotReady  = "NOT_READY"
	ClusterStatusReady     = "READY"
	ClusterStatusUnhealthy = "UNHEALTHY"
	ClusterStatusDeleted   = "DELETED"
)

type CloudProviderType string

const (
	CloudProviderOther    CloudProviderType = "ONPREM"
	CloudProviderDGXCloud CloudProviderType = "DGXCLOUD"
	CloudProviderGCP      CloudProviderType = "GCP"
	CloudProviderAzure    CloudProviderType = "AZURE"
	CloudProviderAWS      CloudProviderType = "AWS"
	CloudProviderOCI      CloudProviderType = "OCI"
	CloudProviderNCP      CloudProviderType = "NCP"
)

// ClusterSource is an alias for the canonical type in pkg/apis/nvcf/v1.
type ClusterSource = nvcfv1.ClusterSource

const (
	ClusterSourceNGCManaged  = nvcfv1.ClusterSourceNGCManaged
	ClusterSourceHelmManaged = nvcfv1.ClusterSourceHelmManaged
	ClusterSourceSelfHosted  = nvcfv1.ClusterSourceSelfHosted
	ClusterSourceSelfManaged = nvcfv1.ClusterSourceSelfManaged
)

var ValidClusterSources = []ClusterSource{
	ClusterSourceNGCManaged,
	ClusterSourceHelmManaged,
	ClusterSourceSelfHosted,
	"self-managed",
}

func ValidateClusterSource(s string) (ClusterSource, error) {
	cs, valid := ParseClusterSource(s)
	if !valid {
		return "", fmt.Errorf("invalid cluster source: %q (valid: %v)", s, ValidClusterSources)
	}
	return cs, nil
}

func ParseClusterSource(s string) (ClusterSource, bool) {
	cs := ClusterSource(strings.ToLower(s))
	if cs == "self-managed" {
		return ClusterSourceSelfHosted, true
	}
	if slices.Contains(ValidClusterSources, cs) {
		return cs, true
	}
	return ClusterSourceNGCManaged, false
}

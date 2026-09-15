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
	"context"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// ensure SMBCSIDriverHealthCheck implements ComponentStatusGetter
var _ health.ComponentStatusGetter = &SMBCSIDriverHealthCheck{}

const (
	SMBCSIDriverName = "smb.csi.k8s.io"
)

type csiDriverGetter interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*storagev1.CSIDriver, error)
}

// SMBCSIDriverHealthCheck is a component status getter for the CSI driver.
type SMBCSIDriverHealthCheck struct {
	csiDriverGetter csiDriverGetter
}

// NewSMBCSIDriverHealthCheck creates a new SMBCSIDriverHealthCheck.
func NewSMBCSIDriverHealthCheck(csiDriverGetter csiDriverGetter) *SMBCSIDriverHealthCheck {
	return &SMBCSIDriverHealthCheck{
		csiDriverGetter: csiDriverGetter,
	}
}

// GetComponentStatus returns the health status of the CSI driver.
func (c *SMBCSIDriverHealthCheck) GetComponentStatus(ctx context.Context) (nvcatypes.AgentHealth, error) {
	log := core.GetLogger(ctx).WithField("component", SMBCSIDriverName)
	_, err := c.csiDriverGetter.Get(ctx, SMBCSIDriverName, metav1.GetOptions{})

	// Track K8s API call metrics
	if m := metrics.FromContext(ctx); m != nil {
		m.TrackK8sAPICall("csidriver", err)
	}

	if err != nil {
		retErr := err
		if k8serrors.IsNotFound(err) {
			log.WithError(err).Debug("CSI driver not found, not a failure for health check")
			err = fmtSMBCSIDriverNotFoundError(err)
			retErr = nil
		} else {
			log.WithError(err).Error("failed to get CSI driver")
		}
		return nvcatypes.AgentHealth{
			Status: nvcatypes.HealthStatusUnhealthy,
			Components: map[string]nvcatypes.ComponentHealth{
				SMBCSIDriverName: {
					Status:      nvcatypes.HealthStatusUnhealthy,
					Errors:      []string{err.Error()},
					StatusLevel: nvcatypes.StatusLevelWarn,
				},
			},
		}, retErr
	}

	log.Debug("CSI driver found, healthy")
	return nvcatypes.AgentHealth{
		Status: nvcatypes.HealthStatusHealthy,
		Components: map[string]nvcatypes.ComponentHealth{
			SMBCSIDriverName: {
				Status:      nvcatypes.HealthStatusHealthy,
				StatusLevel: nvcatypes.StatusLevelWarn,
			},
		},
	}, nil
}

func fmtSMBCSIDriverNotFoundError(err error) error {
	return fmt.Errorf("%s driver must be installed when %s feature flag is enabled. "+
		"See https://github.com/kubernetes-csi/csi-driver-smb/tree/master/charts#install-csi-driver-with-helm-3 for installation instructions. error: %w",
		SMBCSIDriverName, featureflag.HelmSharedStorage.Key, err)
}
